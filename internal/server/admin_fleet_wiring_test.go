package server

import (
	"context"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
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

// fullyWiredFleetStores returns a *Stores with every field
// newAdminFleetHandler reads set to a fresh, empty concrete store, so a
// per-field test can null out exactly one and see both sides of that field's
// store.IsAbsent guard (admin_fleet_wiring.go lines 39, 42, 45, 51 named in
// the #715 fix round 1 review, plus EndDevices and MirrorUsagePoints).
func fullyWiredFleetStores() *Stores {
	return &Stores{
		EndDeviceManagers:   memory.NewEndDeviceManagementStore(),
		EndDevices:          memory.NewEndDeviceStore(),
		DERs:                memory.NewScopedStore[sep2.DER](),
		DERStatuses:         memory.NewScopedStore[sep2.DERStatus](),
		DERAvailabilities:   memory.NewScopedStore[sep2.DERAvailability](),
		MirrorUsagePoints:   memory.NewStore[sep2.MirrorUsagePoint](),
		MirrorMeterReadings: memory.NewScopedStore[sep2.MirrorMeterReading](),
	}
}

// TestNewAdminFleetHandler_EachOptionalStoreGuardedIndependently exercises
// every store.IsAbsent guard in newAdminFleetHandler on both sides: present,
// wired into the matching handler field, and absent, leaving that field nil
// (never a typed-nil interface). A mutation that drops, inverts, or
// misassigns one guard fails on that field alone.
func TestNewAdminFleetHandler_EachOptionalStoreGuardedIndependently(t *testing.T) {
	cases := []struct {
		name    string
		clear   func(*Stores)
		present func(*handler.AdminFleetHandler) bool // true when the field this guard sets is non-nil
	}{
		{"EndDevices", func(s *Stores) { s.EndDevices = nil }, func(h *handler.AdminFleetHandler) bool { return h.EndDevices != nil }},
		{"DERs", func(s *Stores) { s.DERs = nil }, func(h *handler.AdminFleetHandler) bool { return h.DERs != nil }},
		{"DERStatuses", func(s *Stores) { s.DERStatuses = nil }, func(h *handler.AdminFleetHandler) bool { return h.DERStatuses != nil }},
		{"DERAvailabilities", func(s *Stores) { s.DERAvailabilities = nil }, func(h *handler.AdminFleetHandler) bool { return h.DERAvailabilities != nil }},
		{"MirrorUsagePoints", func(s *Stores) { s.MirrorUsagePoints = nil }, func(h *handler.AdminFleetHandler) bool { return h.MirrorUsagePoints != nil }},
		{"MirrorMeterReadings", func(s *Stores) { s.MirrorMeterReadings = nil }, func(h *handler.AdminFleetHandler) bool { return h.MirrorMeterReadings != nil }},
	}

	for _, tc := range cases {
		t.Run(tc.name+"/present", func(t *testing.T) {
			h := newAdminFleetHandler(fullyWiredFleetStores())
			if h == nil {
				t.Fatal("newAdminFleetHandler = nil, want a handler")
			}
			if !tc.present(h) {
				t.Errorf("%s field is nil, want it wired when the store is present", tc.name)
			}
		})
		t.Run(tc.name+"/absent", func(t *testing.T) {
			stores := fullyWiredFleetStores()
			tc.clear(stores)
			h := newAdminFleetHandler(stores)
			if h == nil {
				t.Fatal("newAdminFleetHandler = nil, want a handler (only EndDeviceManagers gates mounting)")
			}
			if tc.present(h) {
				t.Errorf("%s field is non-nil, want nil when the store is absent", tc.name)
			}
		})
	}
}
