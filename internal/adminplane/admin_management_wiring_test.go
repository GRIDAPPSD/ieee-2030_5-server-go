package adminplane

import (
	"context"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/activity"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

// fakeManagementStore satisfies store.EndDeviceManagementStore without being
// the concrete *memory.EndDeviceManagementStore newAdminManagementHandler
// requires for RekeyManager/RekeyManaged, so it drives the type-assertion
// failure branch (#677 fix round, item 3 / H5 / LOW-6).
type fakeManagementStore struct{}

func (fakeManagementStore) ManagerOf(context.Context, string) (string, error) {
	return "", store.ErrNotFound
}
func (fakeManagementStore) ManagedBy(context.Context, string) ([]string, error) { return nil, nil }
func (fakeManagementStore) Assign(context.Context, string, string) error        { return nil }
func (fakeManagementStore) Unassign(context.Context, string) error              { return nil }
func (fakeManagementStore) Release(context.Context, string, string) error       { return nil }

var _ store.EndDeviceManagementStore = fakeManagementStore{}

// Not run with t.Parallel(): each redirects the shared stdlib log.Writer(),
// the same reason admin_client_ca_pool_test.go's multi-CA log test gives.

// TestNewAdminManagementHandler_NilStoreIsSilent is the control for the
// wiring-bug case below: Stores.EndDeviceManagers left unset is a
// deliberate, expected embedder choice, and must log nothing.
func TestNewAdminManagementHandler_NilStoreIsSilent(t *testing.T) {
	var buf strings.Builder
	prevOut := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(prevOut) })

	if h := newAdminManagementHandler(&Stores{}, nil); h != nil {
		t.Fatalf("newAdminManagementHandler(nil EndDeviceManagers) = %v, want nil", h)
	}
	if buf.Len() != 0 {
		t.Errorf("log output = %q, want none: an unset store is a deliberate wiring choice, not a bug", buf.String())
	}
}

// TestNewAdminManagementHandler_WrongConcreteTypeLogsOnce is item 3's
// decisive assertion: a store that satisfies store.EndDeviceManagementStore
// but fails the concrete-type assertion is a wiring bug that used to
// unmount all four management-pair routes with nothing logged, the only
// symptom a 404. It must now say so, the way the protocol-side ownership
// gate already logs the comparable condition at construction.
func TestNewAdminManagementHandler_WrongConcreteTypeLogsOnce(t *testing.T) {
	var buf strings.Builder
	prevOut := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(prevOut) })

	h := newAdminManagementHandler(&Stores{EndDeviceManagers: fakeManagementStore{}}, nil)
	if h != nil {
		t.Fatalf("newAdminManagementHandler(wrong concrete type) = %v, want nil", h)
	}
	got := buf.String()
	if !strings.Contains(got, "EndDeviceManagers") || !strings.Contains(got, "fakeManagementStore") {
		t.Errorf("log output = %q, want it to name Stores.EndDeviceManagers and the wrong concrete type", got)
	}
}

// TestNewAdminManagementHandler_ConcreteTypeIsSilent is the second control:
// the production shape (the concrete memory store) both mounts the handler
// and logs nothing, so the new log line does not fire on every boot.
func TestNewAdminManagementHandler_ConcreteTypeIsSilent(t *testing.T) {
	var buf strings.Builder
	prevOut := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(prevOut) })

	h := newAdminManagementHandler(&Stores{EndDeviceManagers: memory.NewEndDeviceManagementStore()}, nil)
	if h == nil {
		t.Fatal("newAdminManagementHandler(concrete type) = nil, want a handler")
	}
	if buf.Len() != 0 {
		t.Errorf("log output = %q, want none: the production wiring shape must not log", buf.String())
	}
}

// TestNewAdminManagementHandler_WiresRecordAndActivity pins that the pair
// listing reads the same EndDevice store and activity recorder the rest of
// the plane holds (#1087); without them it would report no health at all.
func TestNewAdminManagementHandler_WiresRecordAndActivity(t *testing.T) {
	devs := memory.NewEndDeviceStore()
	rec := activity.New()
	h := newAdminManagementHandler(&Stores{EndDeviceManagers: memory.NewEndDeviceManagementStore(), EndDevices: devs}, rec)
	if h == nil {
		t.Fatal("newAdminManagementHandler = nil, want a handler")
	}
	if h.EndDevices != store.EndDeviceReader(devs) {
		t.Errorf("EndDevices = %v, want the Stores.EndDevices value", h.EndDevices)
	}
	if h.Activity != rec {
		t.Errorf("Activity = %p, want the recorder passed in (%p)", h.Activity, rec)
	}
}

// TestNewAdminManagementHandler_RekeyRefusesWhileMirrorSurvives drives the
// production-wired handler through the reviewer's sequence: the old LFDI's
// record is gone but a mirror naming it remains, so the managed rekey must
// answer 409 and move nothing.
func TestNewAdminManagementHandler_RekeyRefusesWhileMirrorSurvives(t *testing.T) {
	const oldLFDI = "C001000000000000000000000000000000000001"
	const newLFDI = "C002000000000000000000000000000000000002"
	const mgr = "AAAA000000000000000000000000000000000001"
	managers := memory.NewEndDeviceManagementStore()
	mups := memory.NewStore[sep2.MirrorUsagePoint]()
	if err := mups.Create(context.Background(), "1", sep2.MirrorUsagePoint{Resource: sep2.Resource{Href: "/mup/1"}, DeviceLFDI: oldLFDI}); err != nil {
		t.Fatalf("seed mirror: %v", err)
	}
	h := newAdminManagementHandler(&Stores{
		EndDeviceManagers: managers,
		EndDevices:        memory.NewEndDeviceStore(),
		MirrorUsagePoints: mups,
	}, nil)
	if h == nil {
		t.Fatal("newAdminManagementHandler returned nil")
	}
	if err := managers.Assign(context.Background(), mgr, oldLFDI); err != nil {
		t.Fatalf("Assign: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/management-pairs/rekey",
		strings.NewReader(`{"role":"managed","from":"`+oldLFDI+`","to":"`+newLFDI+`"}`))
	w := httptest.NewRecorder()
	h.HandleRekeyManagementPair().ServeHTTP(w, req)

	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409; body = %s", w.Code, w.Body.String())
	}
	if got, _ := managers.ManagerOf(context.Background(), oldLFDI); got != mgr {
		t.Errorf("ManagerOf(old) = %q, want %q unmoved", got, mgr)
	}
	if got, _ := managers.ManagerOf(context.Background(), newLFDI); got != "" {
		t.Errorf("ManagerOf(new) = %q, want no pair", got)
	}
}
