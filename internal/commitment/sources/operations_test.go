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
	hooks  *lifecycleHooks
	marks  *markHooks
	base   int64 // start of the grant's window, in the future
}

// lifecycleHooks wraps the control lifecycle store the Issuer writes
// through, so a test can fail one write or hide one record from it; the
// ledger's own reads go to the unwrapped store.
type lifecycleHooks struct {
	*memory.ScopedStore[dercontrol.LifecycleRecord]
	failUpdate func(id string, rec dercontrol.LifecycleRecord) error
	hideGet    string
}

func (l *lifecycleHooks) Update(ctx context.Context, parentID, id string, rec dercontrol.LifecycleRecord) error {
	if l.failUpdate != nil {
		if err := l.failUpdate(id, rec); err != nil {
			return err
		}
	}
	return l.ScopedStore.Update(ctx, parentID, id, rec)
}

func (l *lifecycleHooks) Get(ctx context.Context, parentID, id string) (dercontrol.LifecycleRecord, error) {
	if id == l.hideGet {
		return dercontrol.LifecycleRecord{}, store.ErrNotFound
	}
	return l.ScopedStore.Get(ctx, parentID, id)
}

// markHooks wraps the response lifecycle store MarkCancelled writes
// through, to fail the cancel mark of a response.
type markHooks struct {
	*memory.ScopedStore[dercontrol.LifecycleRecord]
	failCreate error
}

func (m *markHooks) Create(ctx context.Context, parentID, id string, rec dercontrol.LifecycleRecord) error {
	if m.failCreate != nil {
		return m.failCreate
	}
	return m.ScopedStore.Create(ctx, parentID, id, rec)
}

// deleteFails is a response store whose Delete fails, as a store does when
// a revision's undo cannot remove it.
type deleteFails struct {
	*memory.ScopedStore[sep2.FlowReservationResponse]
}

var errDelete = errors.New("test: delete failed")

func (deleteFails) Delete(context.Context, string, string) error { return errDelete }

func newOpsHarness(t *testing.T) *opsHarness {
	t.Helper()
	f := newFixture(t)
	programs := memory.NewScopedStore[sep2.DERProgram]()
	must(t, programs.Create(context.Background(), managedID, "derp1",
		sep2.DERProgram{DERControlListLink: &sep2.ListLink{Href: "/edev/m1/fsa/fsa1/derp/derp1/derc"}}))
	pen := uint32(1)
	hooks := &lifecycleHooks{ScopedStore: f.controlLifecycles}
	issuer, err := dercontrol.NewIssuer(programs, f.controls, hooks, dercontrol.Config{PEN: &pen})
	must(t, err)
	h := &opsHarness{
		fixture: f,
		ledger:  sources.NewLedger(f.devices, f.managers, f.responses, f.responseLifecycles, f.controls, f.controlLifecycles),
		issuer:  issuer,
		hooks:   hooks,
		marks:   &markHooks{ScopedStore: f.responseLifecycles},
		base:    sep2time.Now().Unix() + 3600,
	}
	return h
}

func (h *opsHarness) writers() commitment.Writers {
	return sources.NewWriters(h.issuer, h.marks)
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
	frp.CreationTime = 100
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
// flowreservation.RevisionID gives, created after the old response.
func (h *opsHarness) revise(t *testing.T, frp sep2.FlowReservationResponse, now int64) error {
	t.Helper()
	frp.CreationTime = 200
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
		*g.Window != (commitment.Window{Start: h.base, Duration: 600}) || g.Energy.Value != 10000 || g.Power.Value != 4000 || g.CancelledAt != nil ||
		g.Subject != "FRQ-MRID" || g.CreationTime != 100 {
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

const errRelink = "test: relink write failed"

// revisionStore is what sources.NewReplacement writes the revision through.
type revisionStore interface {
	Create(ctx context.Context, parentID, id string, frp sep2.FlowReservationResponse) error
	Delete(ctx context.Context, parentID, id string) error
}

// reviseVia revises GRANT-1 to mrid, at the id RevisionID gives and over a
// window that fits both executions.
func (h *opsHarness) reviseVia(t *testing.T, responses revisionStore, mrid string) error {
	t.Helper()
	next := h.grantResponse(flowreservation.RevisionID(grantID), mrid, h.base, 1800, 5000)
	next.CreationTime = 200
	return h.ledger.Revise(context.Background(), h.writers(), "GRANT-1", "revised", 300, func(commitment.Grant) (commitment.Replacement, error) {
		return sources.NewReplacement(responses, aggID, next)
	})
}

func (h *opsHarness) wantRolledBack(t *testing.T, a, b dercontrol.Result) {
	t.Helper()
	for _, res := range []dercontrol.Result{a, b} {
		if lc := h.lifecycle(t, res); lc.GrantMRID != "GRANT-1" || lc.CancelledAt != nil {
			t.Errorf("execution %s lifecycle = %+v, want live and back on GRANT-1", res.Control.MRID, lc)
		}
	}
	if _, err := h.responseLifecycles.Get(context.Background(), aggID, grantID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("old response lifecycle read = %v, want no record: GRANT-1 is still live", err)
	}
}

func TestRevise_FailedRelinkUndoneAgainstRealStores(t *testing.T) {
	t.Parallel()
	h := newOpsHarness(t)
	h.storeGrant(t)
	a := h.issue(t, "GRANT-1", h.base, 600, -2000)
	b := h.issue(t, "GRANT-1", h.base+600, 600, -1000)
	h.hooks.failUpdate = func(id string, rec dercontrol.LifecycleRecord) error {
		if id == b.ID && rec.GrantMRID == "GRANT-2" {
			return errors.New(errRelink)
		}
		return nil
	}

	err := h.reviseVia(t, h.responses, "GRANT-2")
	if err == nil || errors.Is(err, commitment.ErrUndo) {
		t.Fatalf("Revise() = %v, want the relink failure with a clean rollback and no ErrUndo", err)
	}
	h.wantRolledBack(t, a, b)
	if _, err := h.responses.Get(context.Background(), aggID, flowreservation.RevisionID(grantID)); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("revision read = %v, want it deleted by the undo", err)
	}

	h.hooks.failUpdate = nil
	if err := h.reviseVia(t, h.responses, "GRANT-3"); err != nil {
		t.Fatalf("retry after a clean rollback = %v, want accepted", err)
	}
}

func TestRevise_RetryAfterErrUndoNamesTheStoredRevision(t *testing.T) {
	t.Parallel()
	h := newOpsHarness(t)
	h.storeGrant(t)
	a := h.issue(t, "GRANT-1", h.base, 600, -2000)
	b := h.issue(t, "GRANT-1", h.base+600, 600, -1000)
	h.hooks.failUpdate = func(id string, rec dercontrol.LifecycleRecord) error {
		if id == b.ID && rec.GrantMRID == "GRANT-2" {
			return errors.New(errRelink)
		}
		return nil
	}
	revID := flowreservation.RevisionID(grantID)

	err := h.reviseVia(t, deleteFails{h.responses}, "GRANT-2")
	if !errors.Is(err, commitment.ErrUndo) || !errors.Is(err, errDelete) {
		t.Fatalf("Revise() = %v, want ErrUndo wrapping the failed delete", err)
	}
	stored, gerr := h.responses.Get(context.Background(), aggID, revID)
	must(t, gerr)
	if stored.MRID != "GRANT-2" {
		t.Fatalf("stored revision = %+v, want GRANT-2 left behind", stored)
	}
	h.wantRolledBack(t, a, b)

	// The stored revision is a live grant on the window the retry wants, so
	// the retry is refused naming it.
	h.hooks.failUpdate = nil
	err = h.reviseVia(t, h.responses, "GRANT-3")
	var conflict *commitment.ConflictError
	if !errors.As(err, &conflict) || conflict.Code != commitment.ConflictFleetWindow || conflict.MRID != "GRANT-2" {
		t.Fatalf("retry = %v, want fleet_window_committed naming the stored GRANT-2", err)
	}
	h.wantRolledBack(t, a, b)
}

// A denial whose undo failed is not a live grant, so only the id collision
// stops the retry, and it names the stored revision.
func TestRevise_RetryAfterDeniedErrUndoNamesTheStoredRevision(t *testing.T) {
	t.Parallel()
	h := newOpsHarness(t)
	h.storeGrant(t)
	a := h.issue(t, "GRANT-1", h.base, 600, -2000)
	revID := flowreservation.RevisionID(grantID)
	deny := func(responses revisionStore, mrid string) error {
		next := h.grantResponse(revID, mrid, h.base, 0, 5000)
		next.CreationTime = 200
		return h.ledger.Revise(context.Background(), h.writers(), "GRANT-1", "withdrawn", 300, func(commitment.Grant) (commitment.Replacement, error) {
			return sources.NewReplacement(responses, aggID, next)
		})
	}
	h.marks.failCreate = errors.New("test: mark failed")

	err := deny(deleteFails{h.responses}, "GRANT-2")
	if !errors.Is(err, commitment.ErrUndo) {
		t.Fatalf("Revise() = %v, want ErrUndo", err)
	}
	if stored, gerr := h.responses.Get(context.Background(), aggID, revID); gerr != nil || stored.MRID != "GRANT-2" {
		t.Fatalf("stored denial = %+v, %v, want GRANT-2 left behind", stored, gerr)
	}

	h.marks.failCreate = nil
	err = deny(h.responses, "GRANT-3")
	var left *sources.RevisionStoredError
	if !errors.As(err, &left) || left.EndDeviceID != aggID || left.ID != revID || !errors.Is(err, store.ErrAlreadyExists) {
		t.Fatalf("retry = %v, want a RevisionStoredError naming %s/%s", err, aggID, revID)
	}
	if again, gerr := h.responses.Get(context.Background(), aggID, revID); gerr != nil || again.MRID != "GRANT-2" {
		t.Errorf("stored denial after the refused retry = %+v, %v, want GRANT-2 untouched", again, gerr)
	}
	if rlc, gerr := h.responseLifecycles.Get(context.Background(), aggID, grantID); !errors.Is(gerr, store.ErrNotFound) {
		t.Errorf("old response lifecycle = %+v, %v, want no record: GRANT-1 is still live", rlc, gerr)
	}
	if lc := h.lifecycle(t, a); lc.CancelledAt == nil {
		t.Errorf("execution lifecycle = %+v, want cancelled: the denial path cancels first and a retry finishes it", lc)
	}
}

func TestRevise_RefusedRelinkIsNotErrUndo(t *testing.T) {
	t.Parallel()
	h := newOpsHarness(t)
	h.storeGrant(t)
	a := h.issue(t, "GRANT-1", h.base, 600, -2000)
	b := h.issue(t, "GRANT-1", h.base+600, 600, -1000)
	h.hooks.hideGet = b.ID

	err := h.reviseVia(t, h.responses, "GRANT-2")
	var refusal *dercontrol.RefusalError
	if !errors.As(err, &refusal) || refusal.Code != dercontrol.RefusalControlNotFound || errors.Is(err, commitment.ErrUndo) {
		t.Fatalf("Revise() = %v, want a control_not_found refusal and no ErrUndo", err)
	}
	h.hooks.hideGet = ""
	h.wantRolledBack(t, a, b)
	if _, err := h.responses.Get(context.Background(), aggID, flowreservation.RevisionID(grantID)); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("revision read = %v, want it deleted by the undo", err)
	}
}
