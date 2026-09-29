package server

import (
	"context"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/handler"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

// #715 fleet-read wiring tests. Same shape as
// admin_management_wiring_test.go: an unset EndDeviceManagers is a silent,
// deliberate choice; a wrong concrete type is a logged wiring bug; the
// production shape mounts silently. fakeManagementStore is defined in
// admin_management_wiring_test.go (same package).

func TestNewAdminFleetHandler_NilStoreIsSilent(t *testing.T) {
	var buf strings.Builder
	prevOut := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(prevOut) })

	if h := newAdminFleetHandler(&Stores{}); h != nil {
		t.Fatalf("newAdminFleetHandler(nil EndDeviceManagers) = %v, want nil", h)
	}
	if buf.Len() != 0 {
		t.Errorf("log output = %q, want none: an unset store is a deliberate wiring choice, not a bug", buf.String())
	}
}

func TestNewAdminFleetHandler_WrongConcreteTypeLogsOnce(t *testing.T) {
	var buf strings.Builder
	prevOut := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(prevOut) })

	h := newAdminFleetHandler(&Stores{EndDeviceManagers: fakeManagementStore{}})
	if h != nil {
		t.Fatalf("newAdminFleetHandler(wrong concrete type) = %v, want nil", h)
	}
	got := buf.String()
	if !strings.Contains(got, "EndDeviceManagers") || !strings.Contains(got, "fakeManagementStore") {
		t.Errorf("log output = %q, want it to name Stores.EndDeviceManagers and the wrong concrete type", got)
	}
}

func TestNewAdminFleetHandler_ConcreteTypeIsSilent(t *testing.T) {
	var buf strings.Builder
	prevOut := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(prevOut) })

	h := newAdminFleetHandler(&Stores{EndDeviceManagers: memory.NewEndDeviceManagementStore()})
	if h == nil {
		t.Fatal("newAdminFleetHandler(concrete type) = nil, want a handler")
	}
	if buf.Len() != 0 {
		t.Errorf("log output = %q, want none: the production wiring shape must not log", buf.String())
	}
}

// TestNewAdminFleetHandler_PartiallyWiredStoresDoNotPanic is the regression
// test for the typed-nil trap store.IsAbsent exists to avoid (see
// admin_fleet_wiring.go's doc comment): a *memory.ScopedStore/*memory.Store
// nil pointer assigned straight into an interface-typed field is a non-nil
// interface, so a plain "== nil" guard inside the handler would never fire
// and the first read would panic instead of reporting the section absent.
func TestNewAdminFleetHandler_PartiallyWiredStoresDoNotPanic(t *testing.T) {
	managers := memory.NewEndDeviceManagementStore()
	const aggLFDI = "AAAA000000000000000000000000000000000001"
	const devLFDI = "D001000000000000000000000000000000000001"
	if err := managers.Assign(context.Background(), aggLFDI, devLFDI); err != nil {
		t.Fatalf("Assign: %v", err)
	}

	// Every DER/mirror field on Stores left at its zero value (nil
	// pointer): a deployment that wires management pairs but not DER or
	// mirror storage.
	h := newAdminFleetHandler(&Stores{EndDeviceManagers: managers})
	if h == nil {
		t.Fatal("newAdminFleetHandler = nil, want a handler")
	}

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/derms/fleets", nil)
	handler.HandleListFleets(h)(w, req) // must not panic
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", w.Code, w.Body.String())
	}
}
