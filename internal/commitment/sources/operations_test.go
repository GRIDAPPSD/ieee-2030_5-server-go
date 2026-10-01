package sources_test

import (
	"context"
	"errors"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/commitment"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/commitment/sources"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/dercontrol"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/flowreservation"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/handlers/sep2time"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

// The S6 operations against the real stores and a real Issuer: what a
// client reading the stores after CancelGrant or Revise would see.

const grantID = "frq-1"

type opsHarness struct {
	*fixture
	ledger *commitment.Ledger
	issuer *dercontrol.Issuer
	base   int64 // start of the grant's window, in the future
}

func newOpsHarness(t *testing.T) *opsHarness {
	t.Helper()
	f := newFixture(t)
	programs := memory.NewScopedStore[sep2.DERProgram]()
	must(t, programs.Create(context.Background(), managedID, "derp1",
		sep2.DERProgram{DERControlListLink: &sep2.ListLink{Href: "/edev/m1/fsa/fsa1/derp/derp1/derc"}}))
	pen := uint32(1)
	issuer, err := dercontrol.NewIssuer(programs, f.controls, f.controlLifecycles, dercontrol.Config{PEN: &pen})
	must(t, err)
	h := &opsHarness{
		fixture: f,
		ledger:  sources.NewLedger(f.devices, f.managers, f.responses, f.responseLifecycles, f.controls, f.controlLifecycles),
		issuer:  issuer,
		base:    sep2time.Now().Unix() + 3600,
	}
	return h
}

func (h *opsHarness) writers() commitment.Writers {
	return sources.NewWriters(h.issuer, h.responseLifecycles)
}

// grantResponse is a charging grant on [base, base+3600): energy +10000 Wh
// (charging positive), power 4000 W.
func (h *opsHarness) grantResponse(id, mrid string, start int64, dur uint32, energy int64) sep2.FlowReservationResponse {
	frp := sep2.FlowReservationResponse{
		EnergyAvailable: &sep2.SignedRealEnergy{Value: energy},
		PowerAvailable:  &sep2.ActivePower{Value: 4000},
	}
	frp.Href = "/edev/" + aggID + "/frp/" + id
	frp.MRID = mrid
	frp.Subject = "FRQ-MRID"
	frp.Interval = &sep2.DateTimeInterval{Start: start, Duration: dur}
	return frp
}

func (h *opsHarness) storeGrant(t *testing.T) sep2.FlowReservationResponse {
	t.Helper()
	frp := h.grantResponse(grantID, "GRANT-1", h.base, 3600, 10000)
	must(t, h.responses.Create(context.Background(), aggID, grantID, frp))
	return frp
}

// issue creates a control on the managed device through the ledger, as the
// admin create route does.
func (h *opsHarness) issue(t *testing.T, grant string, start int64, dur uint32, target int16) dercontrol.Result {
	t.Helper()
	ctx := context.Background()
	req := dercontrol.CreateRequest{
		DERProgramHref:  "/edev/m1/fsa/fsa1/derp/derp1",
		Type:            dercontrol.TargetW,
		TargetW:         &sep2.ActivePower{Value: target},
		Start:           &start,
		DurationSeconds: dur,
		ExecutesGrant:   grant,
	}
	var res dercontrol.Result
	err := h.ledger.Within(ctx, []string{aggLFDI}, func(v commitment.View) error {
		var err error
		res, err = h.issuer.IssueInFleet(ctx, req, dercontrol.Fleet{Key: aggLFDI, Reach: 1, Check: func(ctx context.Context, p dercontrol.Proposal) error {
			return v.CheckControl(ctx, commitment.Proposal{FleetKey: p.FleetKey, Window: p.Window, GrantMRID: p.GrantMRID, TargetW: p.TargetW, Reach: p.Reach, Supersedes: p.Supersedes})
		}})
		return err
	})
	must(t, err)
	return res
}

func (h *opsHarness) lifecycle(t *testing.T, res dercontrol.Result) dercontrol.LifecycleRecord {
	t.Helper()
	lc, err := h.controlLifecycles.Get(context.Background(), res.Scope.Key(), res.ID)
	must(t, err)
	return lc
}

// grantOnWindow asks the queue's own gate for a new grant on [start,
// start+dur) under the aggregator, storing it under id when free.
func (h *opsHarness) grantOnWindow(t *testing.T, id string, start int64, dur uint32) error {
	t.Helper()
	gate := flowreservation.NewLedgerGate(h.ledger, h.resolver())
	frp := h.grantResponse(id, "MRID-"+id, start, dur, 10000)
	return gate.Grant(context.Background(), aggID, frp.Interval, "", func(ctx context.Context) error {
		return h.responses.Create(ctx, aggID, id, frp)
	})
}

func TestCancelGrant_CancelsExecutionsAndFreesTheWindow(t *testing.T) {
	t.Parallel()
	h := newOpsHarness(t)
	h.storeGrant(t)
	a := h.issue(t, "GRANT-1", h.base, 600, -2000)
	b := h.issue(t, "GRANT-1", h.base+600, 600, -1000)

	// A charge grant executes as a negative target (design 5.3 rule 5).
	if v := a.Control.DERControlBase.OpModTargetW; v == nil || v.Value != -2000 {
		t.Fatalf("stored opModTargetW = %v, want -2000", v)
	}
	if err := h.grantOnWindow(t, "frq-2", h.base, 3600); err == nil {
		t.Fatal("a second grant on the live grant's window was accepted")
	}

	now := sep2time.Now().Unix()
	must(t, h.ledger.CancelGrant(context.Background(), h.writers(), "GRANT-1", "client cancel", now))

	for _, res := range []dercontrol.Result{a, b} {
		lc := h.lifecycle(t, res)
		if lc.CancelledAt == nil || lc.CancelReason != "client cancel" || lc.GrantMRID != "GRANT-1" {
			t.Errorf("execution %s lifecycle = %+v, want cancelled with the reason and still linked", res.Control.MRID, lc)
		}
	}
	rlc, err := h.responseLifecycles.Get(context.Background(), aggID, grantID)
	must(t, err)
	if rlc.CancelledAt == nil || *rlc.CancelledAt != now || rlc.CancelReason != "client cancel" {
		t.Errorf("response lifecycle = %+v, want cancelled at %d with the reason", rlc, now)
	}
	served, err := flowreservation.NewDerivedStatusResponseStore(h.responses, h.responseLifecycles).Get(context.Background(), aggID, grantID)
	must(t, err)
	if served.EventStatus == nil || served.EventStatus.CurrentStatus != sep2.EventStatusCancelled {
		t.Errorf("served EventStatus = %+v, want Cancelled", served.EventStatus)
	}

	if err := h.grantOnWindow(t, "frq-2", h.base, 3600); err != nil {
		t.Fatalf("a new grant on the cancelled grant's window = %v, want accepted", err)
	}
	if _, err := h.responses.Get(context.Background(), aggID, "frq-2"); err != nil {
		t.Fatalf("the new grant was not stored: %v", err)
	}
}

func TestCancelPlainControl_FreesItsWindow(t *testing.T) {
	t.Parallel()
	h := newOpsHarness(t)
	plain := h.issue(t, "", h.base, 600, 1500)

	err := h.grantOnWindow(t, "frq-2", h.base+300, 600)
	var conflict *commitment.ConflictError
	if !errors.As(err, &conflict) || conflict.Code != commitment.ConflictFleetWindow || conflict.MRID != plain.Control.MRID {
		t.Fatalf("grant over a live plain control = %v, want fleet_window_committed naming %s", err, plain.Control.MRID)
	}

	_, err = h.issuer.Cancel(context.Background(), plain.Scope, plain.ID, "operator stop")
	must(t, err)
	if err := h.grantOnWindow(t, "frq-2", h.base+300, 600); err != nil {
		t.Fatalf("grant after the plain control was cancelled = %v, want accepted", err)
	}
}

// revise runs a revision of GRANT-1 to frp, stored under the id
// flowreservation.RevisionID gives.
func (h *opsHarness) revise(t *testing.T, frp sep2.FlowReservationResponse, now int64) error {
	t.Helper()
	return h.ledger.Revise(context.Background(), h.writers(), "GRANT-1", "revised", now, func(old commitment.Grant) (commitment.Replacement, error) {
		if old.ID != grantID {
			t.Errorf("old grant ID = %q, want %q", old.ID, grantID)
		}
		return sources.NewReplacement(h.responses, aggID, frp)
	})
}

func TestRevise_ThatFitsMovesEveryExecution(t *testing.T) {
	t.Parallel()
	h := newOpsHarness(t)
	old := h.storeGrant(t)
	a := h.issue(t, "GRANT-1", h.base, 600, -2000)
	b := h.issue(t, "GRANT-1", h.base+600, 600, -1000)

	newID := flowreservation.RevisionID(grantID)
	next := h.grantResponse(newID, "GRANT-2", h.base, 1800, 5000)
	now := sep2time.Now().Unix()
	must(t, h.revise(t, next, now))

	for _, res := range []dercontrol.Result{a, b} {
		lc := h.lifecycle(t, res)
		if lc.GrantMRID != "GRANT-2" || lc.CancelledAt != nil {
			t.Errorf("execution %s lifecycle = %+v, want live and linked to GRANT-2", res.Control.MRID, lc)
		}
	}
	rlc, err := h.responseLifecycles.Get(context.Background(), aggID, grantID)
	must(t, err)
	if rlc.CancelledAt == nil || *rlc.CancelledAt != now {
		t.Errorf("old response lifecycle = %+v, want cancelled at %d", rlc, now)
	}
	stored, err := h.responses.Get(context.Background(), aggID, newID)
	must(t, err)
	if stored.MRID != "GRANT-2" || *stored.Interval != *next.Interval || stored.EnergyAvailable.Value != 5000 {
		t.Errorf("stored revision = %+v, want GRANT-2 as built", stored)
	}
	if _, err := h.responseLifecycles.Get(context.Background(), aggID, newID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("revision lifecycle read = %v, want no record: the new response is live", err)
	}
	oldNow, err := h.responses.Get(context.Background(), aggID, grantID)
	must(t, err)
	if oldNow.MRID != old.MRID || *oldNow.Interval != *old.Interval || oldNow.EnergyAvailable.Value != old.EnergyAvailable.Value ||
		oldNow.PowerAvailable.Value != old.PowerAvailable.Value || oldNow.Subject != old.Subject {
		t.Errorf("old response = %+v, want its stored fields unchanged by the revision", oldNow)
	}

	execs, err := h.controlSource().ExecutionsOf(context.Background(), "GRANT-2")
	must(t, err)
	if len(execs) != 2 {
		t.Errorf("ExecutionsOf(GRANT-2) = %d controls, want 2", len(execs))
	}
}

func TestRevise_ShrunkBelowAnExecutionIsRefused(t *testing.T) {
	t.Parallel()
	h := newOpsHarness(t)
	h.storeGrant(t)
	a := h.issue(t, "GRANT-1", h.base, 600, -2000)
	b := h.issue(t, "GRANT-1", h.base+600, 600, -1000)
	beforeA, beforeB := h.lifecycle(t, a), h.lifecycle(t, b)

	newID := flowreservation.RevisionID(grantID)
	next := h.grantResponse(newID, "GRANT-2", h.base, 1199, 10000)
	err := h.revise(t, next, sep2time.Now().Unix())
	var conflict *commitment.ConflictError
	if !errors.As(err, &conflict) || conflict.Code != commitment.ConflictOutsideInterval || conflict.MRID != b.Control.MRID {
		t.Fatalf("Revise() = %v, want execution_outside_interval naming %s", err, b.Control.MRID)
	}

	if got := h.lifecycle(t, a); got.GrantMRID != beforeA.GrantMRID || got.CancelledAt != nil {
		t.Errorf("execution a lifecycle = %+v, want %+v", got, beforeA)
	}
	if got := h.lifecycle(t, b); got.GrantMRID != beforeB.GrantMRID || got.CancelledAt != nil {
		t.Errorf("execution b lifecycle = %+v, want %+v", got, beforeB)
	}
	if _, err := h.responses.Get(context.Background(), aggID, newID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("revision read = %v, want not stored", err)
	}
	if _, err := h.responseLifecycles.Get(context.Background(), aggID, grantID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("old response lifecycle read = %v, want no record: the old grant is still live", err)
	}
}

func TestRevise_ToZeroDurationCancelsExecutions(t *testing.T) {
	t.Parallel()
	h := newOpsHarness(t)
	h.storeGrant(t)
	a := h.issue(t, "GRANT-1", h.base, 600, -2000)

	newID := flowreservation.RevisionID(grantID)
	must(t, h.revise(t, h.grantResponse(newID, "GRANT-2", h.base, 0, 10000), sep2time.Now().Unix()))

	if lc := h.lifecycle(t, a); lc.CancelledAt == nil || lc.GrantMRID != "GRANT-1" {
		t.Errorf("execution lifecycle = %+v, want cancelled and still linked to GRANT-1", lc)
	}
	if _, err := h.responses.Get(context.Background(), aggID, newID); err != nil {
		t.Errorf("denial not stored: %v", err)
	}
	if err := h.grantOnWindow(t, "frq-3", h.base, 3600); err != nil {
		t.Fatalf("a grant after the denial = %v, want accepted", err)
	}
}

func TestNewReplacement(t *testing.T) {
	t.Parallel()
	h := newOpsHarness(t)
	frp := h.grantResponse("frq-1-r1", "GRANT-2", h.base, 600, 10000)
	rep, err := sources.NewReplacement(h.responses, aggID, frp)
	must(t, err)
	g := rep.Grant
	if g.MRID != "GRANT-2" || g.ID != "frq-1-r1" || g.EndDeviceID != aggID || g.Window == nil ||
		*g.Window != (commitment.Window{Start: h.base, Duration: 600}) || g.Energy.Value != 10000 || g.Power.Value != 4000 || g.CancelledAt != nil {
		t.Fatalf("Replacement.Grant = %+v", g)
	}
	must(t, rep.Create(context.Background()))
	if _, err := h.responses.Get(context.Background(), aggID, "frq-1-r1"); err != nil {
		t.Fatalf("Create stored nothing: %v", err)
	}
	must(t, rep.Delete(context.Background()))
	if _, err := h.responses.Get(context.Background(), aggID, "frq-1-r1"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("after Delete read = %v, want not found", err)
	}

	frp.Href = "/edev/other/frp/frq-1-r1"
	if _, err := sources.NewReplacement(h.responses, aggID, frp); err == nil {
		t.Fatal("NewReplacement accepted an href under another EndDevice")
	}
}

func TestWriters_MarkCancelledRefusesACancelledGrant(t *testing.T) {
	t.Parallel()
	h := newOpsHarness(t)
	h.storeGrant(t)
	g, err := h.grants().Grant(context.Background(), "GRANT-1")
	must(t, err)
	w := h.writers()
	must(t, w.Grants.MarkCancelled(context.Background(), g, "first", 10))
	err = w.Grants.MarkCancelled(context.Background(), g, "second", 20)
	var conflict *commitment.ConflictError
	if !errors.As(err, &conflict) || conflict.Code != commitment.ConflictGrantNotLive {
		t.Fatalf("second MarkCancelled = %v, want grant_not_live", err)
	}
	rlc, err := h.responseLifecycles.Get(context.Background(), aggID, grantID)
	must(t, err)
	if *rlc.CancelledAt != 10 || rlc.CancelReason != "first" {
		t.Fatalf("response lifecycle = %+v, want the first cancellation kept", rlc)
	}
}
