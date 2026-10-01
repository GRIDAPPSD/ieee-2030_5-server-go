package handler_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/commitment"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/handler"
)

// An operator's cancel of a DER control is never refused for a reason about
// locking or fleets (#667).

// failingFleets is a resolver that cannot answer.
type failingFleets struct{}

func (failingFleets) FleetOf(context.Context, string) (string, error) {
	return "", errors.New("resolver unavailable")
}

func (d *dcHarness) controlCancelled(t *testing.T, mrid string) bool {
	t.Helper()
	parent, id, _, err := d.controls.ByMRID(context.Background(), mrid)
	if err != nil {
		t.Fatalf("ByMRID: %v", err)
	}
	lc, err := d.lifecycles.Get(context.Background(), parent, id)
	return err == nil && lc.CancelledAt != nil
}

func (d *dcHarness) cancelPath(mrid string) string {
	return "/api/der/controls/" + mrid + "/cancel"
}

// executionOnGrant creates a control on device 0 that executes the
// aggregator's grant, so its lifecycle record carries the aggregator's fleet.
func (d *dcHarness) executionOnGrant(t *testing.T) handler.DERControlCreated {
	t.Helper()
	d.seedFleet(t)
	gStart := futureStart(600)
	d.seedGrant(t, grantSpec{start: gStart, duration: 3600, energyWh: 100, powerW: ptrI16(5000)})
	w := d.do(t, http.MethodPost, "/api/der/controls", targetWBody("0", -1000, gStart, 60, grantMRID))
	if w.Code != http.StatusCreated {
		t.Fatalf("create execution: %d %s", w.Code, w.Body.String())
	}
	return decodeCreated(t, w)
}

// holdFleet takes the fleet lock until the returned func is called.
func holdFleet(t *testing.T, d *dcHarness, fleet string) (release func()) {
	t.Helper()
	held, rel, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		done <- d.h.Ledger.Within(context.Background(), []string{fleet}, func(commitment.View) error {
			close(held)
			<-rel
			return nil
		})
	}()
	<-held
	return func() {
		close(rel)
		if err := <-done; err != nil {
			t.Errorf("Within: %v", err)
		}
	}
}

// TestDERControlCancel_AfterUnassignStillWaitsOnTheGrantsFleet: once the
// device leaves its aggregator, the control's device fleet differs from the
// fleet CancelGrant locks, so the cancel must lock the fleet stored on the
// control's lifecycle record.
func TestDERControlCancel_AfterUnassignStillWaitsOnTheGrantsFleet(t *testing.T) {
	d := newDCHarness(t, ptrU32(dcPEN))
	created := d.executionOnGrant(t)
	release := holdFleet(t, d, aggLFDI)
	if err := d.managers.Unassign(context.Background(), dcLFDI); err != nil {
		t.Fatalf("Unassign: %v", err)
	}

	cancelled := make(chan *httptest.ResponseRecorder, 1)
	go func() { cancelled <- d.do(t, http.MethodPost, d.cancelPath(created.MRID), "") }()
	select {
	case w := <-cancelled:
		t.Fatalf("cancel returned %d while the aggregator's lock was held after Unassign; it must wait", w.Code)
	case <-time.After(150 * time.Millisecond):
	}
	if d.controlCancelled(t, created.MRID) {
		t.Fatal("the control was cancelled while the grant's fleet lock was held")
	}

	release()
	if w := <-cancelled; w.Code != http.StatusOK {
		t.Fatalf("cancel after release: %d %s", w.Code, w.Body.String())
	}
	if !d.controlCancelled(t, created.MRID) {
		t.Error("the control is not cancelled after the lock was released")
	}
}

func TestDERControlCancel_WaitsForTheDevicesCurrentFleet(t *testing.T) {
	d := newDCHarness(t, ptrU32(dcPEN))
	created := decodeCreated(t, d.do(t, http.MethodPost, "/api/der/controls", maxLimWBody(futureStart(600), 100, 300)))
	fleet, err := d.h.Fleets.FleetOf(context.Background(), dcDevice)
	if err != nil {
		t.Fatalf("FleetOf: %v", err)
	}
	release := holdFleet(t, d, fleet)

	cancelled := make(chan *httptest.ResponseRecorder, 1)
	go func() { cancelled <- d.do(t, http.MethodPost, d.cancelPath(created.MRID), "") }()
	select {
	case w := <-cancelled:
		t.Fatalf("cancel returned %d while the fleet lock was held; it must wait", w.Code)
	case <-time.After(150 * time.Millisecond):
	}
	release()
	if w := <-cancelled; w.Code != http.StatusOK {
		t.Fatalf("cancel after release: %d %s", w.Code, w.Body.String())
	}
}

// TestDERControlCancel_WithoutAFleetLockStillCancels: each way the lock
// cannot be taken cancels anyway and says why at WARN.
func TestDERControlCancel_WithoutAFleetLockStillCancels(t *testing.T) {
	for _, tc := range []struct {
		name   string
		reason string
		setup  func(t *testing.T, d *dcHarness)
	}{
		{"device has no LFDI", "device_without_lfdi", func(t *testing.T, d *dcHarness) {
			if err := d.devices.Update(context.Background(), dcDevice, sep2.EndDevice{SFDI: "1"}); err != nil {
				t.Fatal(err)
			}
		}},
		{"device deleted", "device_gone", func(t *testing.T, d *dcHarness) {
			if err := d.devices.Delete(context.Background(), dcDevice); err != nil {
				t.Fatal(err)
			}
		}},
		{"resolver errors", "fleet_resolve_failed", func(_ *testing.T, d *dcHarness) { d.h.Fleets = failingFleets{} }},
		{"no resolver", "no_resolver", func(_ *testing.T, d *dcHarness) { d.h.Fleets = nil }},
		{"no ledger", "no_ledger", func(_ *testing.T, d *dcHarness) { d.h.Ledger = nil }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := newDCHarness(t, ptrU32(dcPEN))
			created := decodeCreated(t, d.do(t, http.MethodPost, "/api/der/controls", maxLimWBody(futureStart(600), 100, 300)))
			tc.setup(t, d)

			w := d.do(t, http.MethodPost, d.cancelPath(created.MRID), "")
			if w.Code != http.StatusOK {
				t.Fatalf("cancel: %d %s, want 200", w.Code, w.Body.String())
			}
			if !d.controlCancelled(t, created.MRID) {
				t.Error("the control is not cancelled")
			}
			logs := d.logs.String()
			if !strings.Contains(logs, "level=WARN") || !strings.Contains(logs, "der_control_cancel_unlocked") || !strings.Contains(logs, "reason="+tc.reason) {
				t.Errorf("log = %q, want a WARN der_control_cancel_unlocked with reason=%s", logs, tc.reason)
			}
		})
	}
}

// TestDERControlCancel_ClientGoneWhileWaitingIs503: a cancel whose context
// ends while it waits for the lock answers 503 client_gone at WARN, as a
// create does, and cancels nothing.
func TestDERControlCancel_ClientGoneWhileWaitingIs503(t *testing.T) {
	d := newDCHarness(t, ptrU32(dcPEN))
	created := decodeCreated(t, d.do(t, http.MethodPost, "/api/der/controls", maxLimWBody(futureStart(600), 100, 300)))
	fleet, err := d.h.Fleets.FleetOf(context.Background(), dcDevice)
	if err != nil {
		t.Fatalf("FleetOf: %v", err)
	}
	release := holdFleet(t, d, fleet)

	ctx, stop := context.WithCancel(context.Background())
	req := httptest.NewRequest(http.MethodPost, d.cancelPath(created.MRID), nil).WithContext(ctx)
	req.RemoteAddr = "192.0.2.7:4000"
	w := httptest.NewRecorder()
	done := make(chan struct{})
	go func() { d.mux.ServeHTTP(w, req); close(done) }()
	time.Sleep(100 * time.Millisecond)
	stop()
	// Within takes the lock before it reads the context, so the cancel
	// notices the client left once the holder lets go.
	release()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("the cancel did not return after its context ended")
	}

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d %s, want 503", w.Code, w.Body.String())
	}
	logs := d.logs.String()
	if !strings.Contains(logs, "level=WARN") || !strings.Contains(logs, "client_gone") || strings.Contains(logs, "level=ERROR") {
		t.Errorf("log = %q, want WARN client_gone and no ERROR", logs)
	}
	if d.controlCancelled(t, created.MRID) {
		t.Error("a cancel whose client went away cancelled the control")
	}
}
