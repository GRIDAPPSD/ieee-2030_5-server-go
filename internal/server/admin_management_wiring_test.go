package server

import (
	"context"
	"log"
	"strings"
	"testing"

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

	if h := newAdminManagementHandler(&Stores{}); h != nil {
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

	h := newAdminManagementHandler(&Stores{EndDeviceManagers: fakeManagementStore{}})
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

	h := newAdminManagementHandler(&Stores{EndDeviceManagers: memory.NewEndDeviceManagementStore()})
	if h == nil {
		t.Fatal("newAdminManagementHandler(concrete type) = nil, want a handler")
	}
	if buf.Len() != 0 {
		t.Errorf("log output = %q, want none: the production wiring shape must not log", buf.String())
	}
}
