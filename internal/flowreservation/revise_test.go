package flowreservation_test

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/commitment"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/commitment/sources"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/dercontrol"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/flowreservation"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

// Tests for GRIDAPPSD/ieee-2030_5-server-go#668: revising an answered request
// and cancelling a revised one, against the real queue, ledger and issuer.

func (f *cancelFixture) reviseDeps() flowreservation.ReviseDeps {
	pen := uint32(0x40732001)
	return flowreservation.ReviseDeps{
		FRQ:     f.frq,
		FRP:     f.frp,
		Ledger:  f.ledger,
		Writers: sources.NewWriters(f.issuer, f.frpLifecycles),
		Replace: func(edevID string, frp sep2.FlowReservationResponse) (commitment.Replacement, error) {
			return sources.NewReplacement(f.frp, edevID, frp)
		},
		PEN: &pen,
	}
}

// answered stores a request for [base, base+3600) and answers it as asked.
func (f *cancelFixture) answered(t *testing.T, base int64) sep2.FlowReservationResponse {
	t.Helper()
	storeRequest(t, f.frq, aggID, "R1", windowRequest("REQ-REVISE", base, 3600, 10000))
	grant, err := f.queue.Answer(context.Background(), aggID, "R1", flowreservation.Decision{})
	must(t, err)
	return grant
}

func (f *cancelFixture) issueExec(t *testing.T, grant sep2.FlowReservationResponse, start int64, target int16) dercontrol.Result {
	t.Helper()
	ctx := context.Background()
	req := dercontrol.CreateRequest{
		DERProgramHref:  "/edev/" + managedID + "/fsa/fsa1/derp/derp1",
		Type:            dercontrol.TargetW,
		TargetW:         &sep2.ActivePower{Value: target},
		Start:           &start,
		DurationSeconds: 600,
		ExecutesGrant:   grant.MRID,
	}
	var res dercontrol.Result
	must(t, f.ledger.Within(ctx, []string{aggLFDI}, func(v commitment.View) error {
		var err error
		res, err = f.issuer.IssueInFleet(ctx, req, dercontrol.Fleet{Key: aggLFDI, Reach: 1, Check: viewCheck(v)})
		return err
	}))
	return res
}

func shorten(start int64, dur uint32) flowreservation.Decision {
	return flowreservation.Decision{Interval: &sep2.DateTimeInterval{Start: start, Duration: dur}}
}

func (f *cancelFixture) storedResponse(t *testing.T, id string) sep2.FlowReservationResponse {
	t.Helper()
	frp, err := f.frp.Get(context.Background(), aggID, id)
	must(t, err)
	return frp
}

func TestRevise_SameSecondRevisionIsAcceptedOneSecondLater(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newCancelFixture(t, flowreservation.Config{Deadline: time.Hour})
	base := time.Now().Add(time.Hour).Unix()
	old := f.answered(t, base)

	at := time.Unix(old.CreationTime, 0)
	got, err := flowreservation.Revise(ctx, f.reviseDeps(), aggID, "R1", shorten(base, 1800), "operator revise", flowreservation.Attribution{}, at)
	must(t, err)

	if got.CreationTime != old.CreationTime+1 {
		t.Errorf("creationTime = %d, want old+1 = %d", got.CreationTime, old.CreationTime+1)
	}
	stored := f.storedResponse(t, "R1-r1")
	if stored.CreationTime != old.CreationTime+1 {
		t.Errorf("stored creationTime = %d, want %d", stored.CreationTime, old.CreationTime+1)
	}
	if stored.Href != "/edev/"+aggID+"/frp/R1-r1" || stored.Subject != old.Subject || stored.MRID == old.MRID || stored.MRID == "" {
		t.Errorf("stored revision href %q subject %q mRID %q; old subject %q mRID %q", stored.Href, stored.Subject, stored.MRID, old.Subject, old.MRID)
	}
	if stored.Interval == nil || stored.Interval.Start != base || stored.Interval.Duration != 1800 {
		t.Errorf("stored interval = %+v, want start %d duration 1800", stored.Interval, base)
	}
	want := dercontrol.DeriveStatus(at.Unix(), stored.CreationTime, base, dercontrol.LifecycleRecord{})
	if stored.EventStatus == nil || *stored.EventStatus != want {
		t.Errorf("stored EventStatus = %+v, want derived %+v", stored.EventStatus, want)
	}
}

func TestRevise_LaterClockSetsCreationTimeToNow(t *testing.T) {
	t.Parallel()
	f := newCancelFixture(t, flowreservation.Config{Deadline: time.Hour})
	base := time.Now().Add(time.Hour).Unix()
	old := f.answered(t, base)

	at := time.Unix(old.CreationTime+90, 0)
	got, err := flowreservation.Revise(context.Background(), f.reviseDeps(), aggID, "R1", shorten(base, 1800), "operator revise", flowreservation.Attribution{}, at)
	must(t, err)
	if got.CreationTime != at.Unix() {
		t.Errorf("creationTime = %d, want now = %d", got.CreationTime, at.Unix())
	}
}

func TestRevise_ChainKeepsCreationTimeStrictlyIncreasing(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newCancelFixture(t, flowreservation.Config{Deadline: time.Hour})
	base := time.Now().Add(time.Hour).Unix()
	old := f.answered(t, base)

	at := time.Unix(old.CreationTime, 0)
	for _, dur := range []uint32{3000, 2400, 1800} {
		_, err := flowreservation.Revise(ctx, f.reviseDeps(), aggID, "R1", shorten(base, dur), "operator revise", flowreservation.Attribution{}, at)
		must(t, err)
	}
	chain, err := flowreservation.ChainOf(ctx, f.frp, aggID, "R1")
	must(t, err)
	if len(chain) != 4 {
		t.Fatalf("chain length = %d, want 4", len(chain))
	}
	for i := 1; i < len(chain); i++ {
		if chain[i].CreationTime != chain[i-1].CreationTime+1 {
			t.Errorf("chain[%d].CreationTime = %d, want %d", i, chain[i].CreationTime, chain[i-1].CreationTime+1)
		}
		if chain[i].Subject != chain[0].Subject {
			t.Errorf("chain[%d].Subject = %q, want %q", i, chain[i].Subject, chain[0].Subject)
		}
	}
	wantHrefs := []string{"R1", "R1-r1", "R1-r2", "R1-r3"}
	for i, id := range wantHrefs {
		if chain[i].Href != "/edev/"+aggID+"/frp/"+id {
			t.Errorf("chain[%d].Href = %q, want id %q", i, chain[i].Href, id)
		}
	}
}

func TestRevise_IssuedResponseIsNeverEditedApartFromItsStatus(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newCancelFixture(t, flowreservation.Config{Deadline: time.Hour})
	base := time.Now().Add(time.Hour).Unix()
	f.answered(t, base)
	before, err := json.Marshal(f.storedResponse(t, "R1"))
	must(t, err)

	now := time.Now()
	_, err = flowreservation.Revise(ctx, f.reviseDeps(), aggID, "R1", shorten(base, 1800), "operator revise", flowreservation.Attribution{}, now)
	must(t, err)

	after, err := json.Marshal(f.storedResponse(t, "R1"))
	must(t, err)
	if string(before) != string(after) {
		t.Errorf("old response changed:\nbefore %s\nafter  %s", before, after)
	}
	lc, err := f.frpLifecycles.Get(ctx, aggID, "R1")
	must(t, err)
	if lc.CancelledAt == nil || *lc.CancelledAt != now.Unix() || lc.CancelReason != "operator revise" {
		t.Errorf("old lifecycle = %+v, want cancelled at %d with the reason", lc, now.Unix())
	}
	derived := flowreservation.NewDerivedStatusResponseStore(f.frp, f.frpLifecycles)
	served, err := derived.Get(ctx, aggID, "R1")
	must(t, err)
	if served.EventStatus == nil || served.EventStatus.CurrentStatus != sep2.EventStatusCancelled || served.EventStatus.DateTime != now.Unix() {
		t.Errorf("served old EventStatus = %+v, want Cancelled at %d", served.EventStatus, now.Unix())
	}
}

func TestRevise_ShortenedRevisionFreesTheOldWindow(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newCancelFixture(t, flowreservation.Config{Deadline: time.Hour})
	base := time.Now().Add(time.Hour).Unix()
	f.answered(t, base)
	_, err := flowreservation.Revise(ctx, f.reviseDeps(), aggID, "R1", shorten(base, 1800), "operator revise", flowreservation.Attribution{}, time.Now())
	must(t, err)

	gate := flowreservation.NewLedgerGate(f.ledger, commitment.Resolver{Devices: f.devices, Managers: f.managers})
	free := f.responseFor(base+1800, 1800, "MRID-FREE")
	if err := gate.Grant(ctx, aggID, free.Interval, "", func(ctx context.Context) error {
		return f.frp.Create(ctx, aggID, "frq-free", free)
	}); err != nil {
		t.Errorf("the freed second half was refused: %v", err)
	}
	held := f.responseFor(base, 1800, "MRID-HELD")
	var conflict *commitment.ConflictError
	err = gate.Grant(ctx, aggID, held.Interval, "", func(ctx context.Context) error {
		return f.frp.Create(ctx, aggID, "frq-held", held)
	})
	if !errors.As(err, &conflict) {
		t.Errorf("the revision's own window was not held: err = %v", err)
	}
}

func TestRevise_ToDenialIsADenialAndATerminalOne(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newCancelFixture(t, flowreservation.Config{Deadline: time.Hour})
	base := time.Now().Add(time.Hour).Unix()
	f.answered(t, base)

	got, err := flowreservation.Revise(ctx, f.reviseDeps(), aggID, "R1", flowreservation.Decision{Kind: flowreservation.Deny}, "operator revise", flowreservation.Attribution{}, time.Now())
	must(t, err)
	if got.Interval == nil || got.Interval.Duration != 0 || got.Interval.Start != base {
		t.Errorf("denial interval = %+v, want duration 0 at %d", got.Interval, base)
	}
	if got.EnergyAvailable == nil || got.EnergyAvailable.Value != 0 || got.PowerAvailable == nil || got.PowerAvailable.Value != 0 {
		t.Errorf("denial available energy %+v power %+v, want zero", got.EnergyAvailable, got.PowerAvailable)
	}

	_, err = flowreservation.Revise(ctx, f.reviseDeps(), aggID, "R1", shorten(base, 600), "again", flowreservation.Attribution{}, time.Now())
	var conflict *commitment.ConflictError
	if !errors.As(err, &conflict) || conflict.Code != commitment.ConflictGrantNotLive {
		t.Errorf("revising a denial: err = %v, want grant_not_live", err)
	}
	if _, err := f.frp.Get(ctx, aggID, "R1-r2"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("a refused revision stored R1-r2: err = %v", err)
	}
}

func TestRevise_CancelledResponseIsRefused(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newCancelFixture(t, flowreservation.Config{Deadline: time.Hour})
	base := time.Now().Add(time.Hour).Unix()
	old := f.answered(t, base)
	// Cancel the grant alone, leaving its request live, so the refusal
	// under test is the ledger's and not the cancelled request's.
	must(t, f.ledger.CancelGrant(ctx, sources.NewWriters(f.issuer, f.frpLifecycles), old.MRID, "operator cancel", time.Now().Unix()))

	_, err := flowreservation.Revise(ctx, f.reviseDeps(), aggID, "R1", shorten(base, 600), "late", flowreservation.Attribution{}, time.Now())
	var conflict *commitment.ConflictError
	if !errors.As(err, &conflict) || conflict.Code != commitment.ConflictGrantNotLive {
		t.Fatalf("err = %v, want grant_not_live", err)
	}
	if _, err := f.frp.Get(ctx, aggID, "R1-r1"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("a refused revision stored R1-r1: err = %v", err)
	}
}

func TestRevise_CancelledRequestIsRefused(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newCancelFixture(t, flowreservation.Config{Deadline: time.Hour})
	base := time.Now().Add(time.Hour).Unix()
	f.answered(t, base)
	must(t, f.canceller.Cancel(ctx, aggID, "R1", cancelledStatus(time.Now().Unix())))

	_, err := flowreservation.Revise(ctx, f.reviseDeps(), aggID, "R1", shorten(base, 600), "late", flowreservation.Attribution{}, time.Now())
	if !errors.Is(err, flowreservation.ErrRequestCancelled) {
		t.Fatalf("err = %v, want ErrRequestCancelled", err)
	}
	if _, err := f.frp.Get(ctx, aggID, "R1-r1"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("a refused revision stored R1-r1: err = %v", err)
	}
}

func TestRevise_UnansweredRequestHasNothingToRevise(t *testing.T) {
	t.Parallel()
	f := newCancelFixture(t, flowreservation.Config{Deadline: time.Hour})
	storeRequest(t, f.frq, aggID, "R1", windowRequest("REQ-PENDING", time.Now().Add(time.Hour).Unix(), 600, 10000))
	_, err := flowreservation.Revise(context.Background(), f.reviseDeps(), aggID, "R1", flowreservation.Decision{}, "x", flowreservation.Attribution{}, time.Now())
	if !errors.Is(err, flowreservation.ErrNothingToRevise) {
		t.Errorf("err = %v, want ErrNothingToRevise", err)
	}
}

func TestRevise_ControlsMoveToTheRevisionWhenTheyStillFit(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newCancelFixture(t, flowreservation.Config{Deadline: time.Hour})
	base := time.Now().Add(time.Hour).Unix()
	old := f.answered(t, base)
	exec := f.issueExec(t, old, base, -2000)

	got, err := flowreservation.Revise(ctx, f.reviseDeps(), aggID, "R1", shorten(base, 1800), "operator revise", flowreservation.Attribution{}, time.Now())
	must(t, err)

	lc, err := f.controlLifecycles.Get(ctx, exec.Scope.Key(), exec.ID)
	must(t, err)
	if lc.GrantMRID != got.MRID || lc.CancelledAt != nil {
		t.Errorf("execution lifecycle = %+v, want live and linked to the revision %s", lc, got.MRID)
	}
}

func TestRevise_RefusedNamingTheControlThatNoLongerFits(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newCancelFixture(t, flowreservation.Config{Deadline: time.Hour})
	base := time.Now().Add(time.Hour).Unix()
	old := f.answered(t, base)
	f.issueExec(t, old, base, -2000)
	late := f.issueExec(t, old, base+1800, -2000)

	_, err := flowreservation.Revise(ctx, f.reviseDeps(), aggID, "R1", shorten(base, 1200), "operator revise", flowreservation.Attribution{}, time.Now())
	var conflict *commitment.ConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("err = %v, want a ConflictError naming the control", err)
	}
	if conflict.MRID != late.Control.MRID {
		t.Errorf("refusal names %q, want the control past the shortened window %q", conflict.MRID, late.Control.MRID)
	}
	if _, err := f.frp.Get(ctx, aggID, "R1-r1"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("a refused revision stored R1-r1: err = %v", err)
	}
	if lc, err := f.frpLifecycles.Get(ctx, aggID, "R1"); err == nil && lc.CancelledAt != nil {
		t.Errorf("a refused revision cancelled the old grant: %+v", lc)
	}
	lc, err := f.controlLifecycles.Get(ctx, late.Scope.Key(), late.ID)
	must(t, err)
	if lc.GrantMRID != old.MRID || lc.CancelledAt != nil {
		t.Errorf("a refused revision touched the control: %+v", lc)
	}
}

func TestCancel_FollowsTheRevisionAndCancelsItsControls(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newCancelFixture(t, flowreservation.Config{Deadline: time.Hour})
	base := time.Now().Add(time.Hour).Unix()
	old := f.answered(t, base)
	exec := f.issueExec(t, old, base, -2000)
	rev, err := flowreservation.Revise(ctx, f.reviseDeps(), aggID, "R1", shorten(base, 1800), "operator revise", flowreservation.Attribution{}, time.Now())
	must(t, err)
	oldBefore, err := f.frpLifecycles.Get(ctx, aggID, "R1")
	must(t, err)

	at := time.Now().Unix()
	must(t, f.canceller.Cancel(ctx, aggID, "R1", cancelledStatus(at)))

	rlc, err := f.frpLifecycles.Get(ctx, aggID, "R1-r1")
	must(t, err)
	if rlc.CancelledAt == nil || rlc.CancelReason != "client cancel" {
		t.Errorf("revision lifecycle = %+v, want cancelled with reason client cancel", rlc)
	}
	lc, err := f.controlLifecycles.Get(ctx, exec.Scope.Key(), exec.ID)
	must(t, err)
	if lc.CancelledAt == nil || lc.GrantMRID != rev.MRID {
		t.Errorf("execution lifecycle = %+v, want cancelled and linked to %s", lc, rev.MRID)
	}
	if oldAfter, err := f.frpLifecycles.Get(ctx, aggID, "R1"); err != nil || *oldAfter.CancelledAt != *oldBefore.CancelledAt || oldAfter.CancelReason != oldBefore.CancelReason {
		t.Errorf("the cancel rewrote the old response's mark: %+v then %+v (err %v)", oldBefore, oldAfter, err)
	}

	must(t, f.canceller.Cancel(ctx, aggID, "R1", cancelledStatus(at+5)))
}

func TestCancel_AfterARevisionToDenialChangesNothing(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newCancelFixture(t, flowreservation.Config{Deadline: time.Hour})
	base := time.Now().Add(time.Hour).Unix()
	f.answered(t, base)
	_, err := flowreservation.Revise(ctx, f.reviseDeps(), aggID, "R1", flowreservation.Decision{Kind: flowreservation.Deny}, "operator revise", flowreservation.Attribution{}, time.Now())
	must(t, err)

	must(t, f.canceller.Cancel(ctx, aggID, "R1", cancelledStatus(time.Now().Unix())))
	if lc, err := f.frpLifecycles.Get(ctx, aggID, "R1-r1"); err == nil {
		t.Errorf("the denial tip gained a lifecycle record: %+v", lc)
	}
}

func TestChainOf_EmptyBeforeAnyResponse(t *testing.T) {
	t.Parallel()
	f := newCancelFixture(t, flowreservation.Config{Deadline: time.Hour})
	chain, err := flowreservation.ChainOf(context.Background(), f.frp, aggID, "R1")
	must(t, err)
	if len(chain) != 0 {
		t.Errorf("chain = %+v, want empty", chain)
	}
}

// frqHook and frpHook decorate a store's Get so a test can park a caller
// between two steps or fail a read.
type frqHook struct {
	flowreservation.FRQReader
	after func(id string, err error)
}

func (h frqHook) Get(ctx context.Context, parentID, id string) (sep2.FlowReservationRequest, error) {
	frq, err := h.FRQReader.Get(ctx, parentID, id)
	if h.after != nil {
		h.after(id, err)
	}
	return frq, err
}

type frpHook struct {
	flowreservation.FRPStore
	get func(ctx context.Context, parentID, id string) (sep2.FlowReservationResponse, error)
}

func (h frpHook) Get(ctx context.Context, parentID, id string) (sep2.FlowReservationResponse, error) {
	return h.get(ctx, parentID, id)
}

func waitFor(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(10 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
	}
}

// TestCancel_RacingARevisionStillCancelsTheRevision parks Revise after it
// has read the request and Cancel after its chain walk found only R1, then
// lets Revise finish before Cancel continues. Cancel's cancel of R1 reads
// "not live"; it must walk again and cancel R1-r1 and its control.
func TestCancel_RacingARevisionStillCancelsTheRevision(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newCancelFixture(t, flowreservation.Config{Deadline: time.Hour})
	base := time.Now().Add(time.Hour).Unix()
	old := f.answered(t, base)
	exec := f.issueExec(t, old, base, -2000)

	reqRead, releaseRevise := make(chan struct{}), make(chan struct{})
	walked, releaseCancel := make(chan struct{}), make(chan struct{})
	var parkRevise, parkCancel sync.Once

	deps := f.reviseDeps()
	deps.FRQ = frqHook{FRQReader: f.frq, after: func(string, error) {
		parkRevise.Do(func() { close(reqRead); <-releaseRevise })
	}}
	canceller := flowreservation.NewCanceller(f.frq, frpHook{FRPStore: f.frp, get: func(ctx context.Context, p, id string) (sep2.FlowReservationResponse, error) {
		frp, err := f.frp.Get(ctx, p, id)
		if id == "R1-r1" && errors.Is(err, store.ErrNotFound) {
			parkCancel.Do(func() { close(walked); <-releaseCancel })
		}
		return frp, err
	}}, f.queue, f.ledger, sources.NewWriters(f.issuer, f.frpLifecycles))

	reviseDone, cancelDone := make(chan error, 1), make(chan error, 1)
	go func() {
		_, err := flowreservation.Revise(ctx, deps, aggID, "R1", shorten(base, 1800), "operator revise", flowreservation.Attribution{}, time.Now())
		reviseDone <- err
	}()
	waitFor(t, reqRead, "Revise to read the request")
	go func() { cancelDone <- canceller.Cancel(ctx, aggID, "R1", cancelledStatus(time.Now().Unix())) }()
	waitFor(t, walked, "Cancel to walk the chain")

	close(releaseRevise)
	if err := <-reviseDone; err != nil {
		t.Fatalf("Revise: %v", err)
	}
	close(releaseCancel)
	if err := <-cancelDone; err != nil {
		t.Fatalf("Cancel: %v", err)
	}

	rlc, err := f.frpLifecycles.Get(ctx, aggID, "R1-r1")
	if err != nil || rlc.CancelledAt == nil || rlc.CancelReason != "client cancel" {
		t.Errorf("revision lifecycle = %+v (err %v), want cancelled with reason client cancel", rlc, err)
	}
	lc, err := f.controlLifecycles.Get(ctx, exec.Scope.Key(), exec.ID)
	must(t, err)
	if lc.CancelledAt == nil {
		t.Errorf("execution lifecycle = %+v, want cancelled", lc)
	}
}

// A refused cancel mark plus a refused rollback delete leaves two live
// responses in one chain.
func TestCancel_CancelsEveryLiveResponseAfterAFailedUndo(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newCancelFixture(t, flowreservation.Config{Deadline: time.Hour})
	base := time.Now().Add(time.Hour).Unix()
	old := f.answered(t, base)
	exec := f.issueExec(t, old, base, -2000)

	real := sources.NewWriters(f.issuer, f.frpLifecycles)
	deps := f.reviseDeps()
	deps.Writers = commitment.Writers{Executions: real.Executions, Grants: &failingGrantWriter{inner: real.Grants}}
	deps.Replace = func(edevID string, frp sep2.FlowReservationResponse) (commitment.Replacement, error) {
		rep, err := sources.NewReplacement(f.frp, edevID, frp)
		rep.Delete = func(context.Context) error { return errors.New("boom: delete refused") }
		return rep, err
	}
	_, err := flowreservation.Revise(ctx, deps, aggID, "R1", shorten(base, 1800), "operator revise", flowreservation.Attribution{}, time.Now())
	if !errors.Is(err, commitment.ErrUndo) {
		t.Fatalf("Revise err = %v, want ErrUndo", err)
	}
	for _, id := range []string{"R1", "R1-r1"} {
		if lc, err := f.frpLifecycles.Get(ctx, aggID, id); err == nil && lc.CancelledAt != nil {
			t.Fatalf("setup: %s already cancelled: %+v", id, lc)
		}
	}

	must(t, f.canceller.Cancel(ctx, aggID, "R1", cancelledStatus(time.Now().Unix())))

	for _, id := range []string{"R1", "R1-r1"} {
		lc, err := f.frpLifecycles.Get(ctx, aggID, id)
		if err != nil || lc.CancelledAt == nil || lc.CancelReason != "client cancel" {
			t.Errorf("%s lifecycle = %+v (err %v), want cancelled with reason client cancel", id, lc, err)
		}
	}
	elc, err := f.controlLifecycles.Get(ctx, exec.Scope.Key(), exec.ID)
	must(t, err)
	if elc.CancelledAt == nil {
		t.Errorf("execution lifecycle = %+v, want cancelled", elc)
	}
}

func TestChainOf_ReadErrorIsReturnedNotTheEndOfTheChain(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newCancelFixture(t, flowreservation.Config{Deadline: time.Hour})
	base := time.Now().Add(time.Hour).Unix()
	f.answered(t, base)
	_, err := flowreservation.Revise(ctx, f.reviseDeps(), aggID, "R1", shorten(base, 1800), "operator revise", flowreservation.Attribution{}, time.Now())
	must(t, err)

	boom := errors.New("boom: read refused")
	flaky := frpHook{FRPStore: f.frp, get: func(ctx context.Context, p, id string) (sep2.FlowReservationResponse, error) {
		if id == "R1-r1" {
			return sep2.FlowReservationResponse{}, boom
		}
		return f.frp.Get(ctx, p, id)
	}}
	chain, err := flowreservation.ChainOf(ctx, flaky, aggID, "R1")
	if !errors.Is(err, boom) || chain != nil {
		t.Fatalf("ChainOf = (%d members, %v), want nil and the read error", len(chain), err)
	}

	canceller := flowreservation.NewCanceller(f.frq, flaky, f.queue, f.ledger, sources.NewWriters(f.issuer, f.frpLifecycles))
	if err := canceller.Cancel(ctx, aggID, "R1", cancelledStatus(time.Now().Unix())); !errors.Is(err, boom) {
		t.Errorf("Cancel err = %v, want the read error", err)
	}
	if lc, err := f.frpLifecycles.Get(ctx, aggID, "R1-r1"); err == nil && lc.CancelledAt != nil {
		t.Errorf("a failed read still cancelled the revision: %+v", lc)
	}
}

func TestRevise_NilDependenciesFailClosed(t *testing.T) {
	t.Parallel()
	f := newCancelFixture(t, flowreservation.Config{Deadline: time.Hour})
	base := time.Now().Add(time.Hour).Unix()
	f.answered(t, base)
	revise := func(deps flowreservation.ReviseDeps) error {
		_, err := flowreservation.Revise(context.Background(), deps, aggID, "R1", shorten(base, 1800), "x", flowreservation.Attribution{}, time.Now())
		return err
	}

	noLedger := f.reviseDeps()
	noLedger.Ledger = nil
	if err := revise(noLedger); !errors.Is(err, commitment.ErrNoLedger) {
		t.Errorf("nil Ledger: err = %v, want ErrNoLedger", err)
	}
	noReplace := f.reviseDeps()
	noReplace.Replace = nil
	if err := revise(noReplace); !errors.Is(err, flowreservation.ErrIncompleteDeps) {
		t.Errorf("nil Replace: err = %v, want ErrIncompleteDeps", err)
	}
	noFRQ := f.reviseDeps()
	noFRQ.FRQ = nil
	if err := revise(noFRQ); !errors.Is(err, flowreservation.ErrIncompleteDeps) {
		t.Errorf("nil FRQ: err = %v, want ErrIncompleteDeps", err)
	}
	noFRP := f.reviseDeps()
	noFRP.FRP = nil
	if err := revise(noFRP); !errors.Is(err, flowreservation.ErrIncompleteDeps) {
		t.Errorf("nil FRP: err = %v, want ErrIncompleteDeps", err)
	}
	if _, err := f.frp.Get(context.Background(), aggID, "R1-r1"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("a refused revise stored R1-r1: err = %v", err)
	}
}

func TestRevise_StoredEventStatusFollowsTheDerivedRuleForAStartedWindow(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newCancelFixture(t, flowreservation.Config{Deadline: time.Hour})
	start := time.Now().Add(-10 * time.Minute).Unix()
	old := f.answered(t, start)

	// A later clock: the window and the revision have both started.
	later := time.Unix(old.CreationTime+90, 0)
	got, err := flowreservation.Revise(ctx, f.reviseDeps(), aggID, "R1", shorten(start, 1800), "operator revise", flowreservation.Attribution{}, later)
	must(t, err)
	wantLater := dercontrol.DeriveStatus(later.Unix(), got.CreationTime, start, dercontrol.LifecycleRecord{})
	if wantLater.CurrentStatus != sep2.EventStatusActive {
		t.Fatalf("setup: derived status %+v, want Active", wantLater)
	}
	if st := f.storedResponse(t, "R1-r1").EventStatus; st == nil || *st != wantLater {
		t.Errorf("stored EventStatus = %+v, want %+v", st, wantLater)
	}

	// Same second as the response it replaces: creationTime is one second
	// ahead of the clock, so the derived rule reads Scheduled, not Active.
	same := time.Unix(got.CreationTime, 0)
	got2, err := flowreservation.Revise(ctx, f.reviseDeps(), aggID, "R1", shorten(start, 900), "again", flowreservation.Attribution{}, same)
	must(t, err)
	wantSame := dercontrol.DeriveStatus(same.Unix(), got2.CreationTime, start, dercontrol.LifecycleRecord{})
	if wantSame.CurrentStatus != sep2.EventStatusScheduled {
		t.Fatalf("setup: derived status %+v, want Scheduled", wantSame)
	}
	if st := f.storedResponse(t, "R1-r2").EventStatus; st == nil || *st != wantSame {
		t.Errorf("stored EventStatus = %+v, want %+v", st, wantSame)
	}
}

func TestRevise_ToDenialCancelsTheOldGrant(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newCancelFixture(t, flowreservation.Config{Deadline: time.Hour})
	base := time.Now().Add(time.Hour).Unix()
	f.answered(t, base)
	now := time.Now()
	_, err := flowreservation.Revise(ctx, f.reviseDeps(), aggID, "R1", flowreservation.Decision{Kind: flowreservation.Deny}, "operator deny", flowreservation.Attribution{}, now)
	must(t, err)

	lc, err := f.frpLifecycles.Get(ctx, aggID, "R1")
	if err != nil || lc.CancelledAt == nil || *lc.CancelledAt != now.Unix() || lc.CancelReason != "operator deny" {
		t.Errorf("old lifecycle = %+v (err %v), want cancelled at %d with the reason", lc, err, now.Unix())
	}
}

func TestRevise_RefusesATipWithNoStoreID(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newCancelFixture(t, flowreservation.Config{Deadline: time.Hour})
	storeRequest(t, f.frq, aggID, "R1", windowRequest("REQ-BADHREF", time.Now().Add(time.Hour).Unix(), 600, 10000))
	must(t, f.frp.Create(ctx, aggID, "R1", sep2.FlowReservationResponse{}))

	_, err := flowreservation.Revise(ctx, f.reviseDeps(), aggID, "R1", flowreservation.Decision{}, "x", flowreservation.Attribution{}, time.Now())
	if err == nil || !strings.HasPrefix(err.Error(), "flowreservation: response href") {
		t.Errorf("err = %v, want the no-store-id refusal", err)
	}
}

func TestRevise_RequestReadErrorIsReturned(t *testing.T) {
	t.Parallel()
	f := newCancelFixture(t, flowreservation.Config{Deadline: time.Hour})
	f.answered(t, time.Now().Add(time.Hour).Unix())
	boom := errors.New("boom: request read refused")
	deps := f.reviseDeps()
	deps.FRQ = failingFRQ{boom}

	_, err := flowreservation.Revise(context.Background(), deps, aggID, "R1", flowreservation.Decision{}, "x", flowreservation.Attribution{}, time.Now())
	if !errors.Is(err, boom) {
		t.Errorf("err = %v, want the read error", err)
	}
	if _, err := f.frp.Get(context.Background(), aggID, "R1-r1"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("a refused revise stored R1-r1: err = %v", err)
	}
}

type failingFRQ struct{ err error }

func (f failingFRQ) Get(context.Context, string, string) (sep2.FlowReservationRequest, error) {
	return sep2.FlowReservationRequest{}, f.err
}

func TestCancel_EmptyChainAfterAnAnsweredRequestIsNotFound(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newCancelFixture(t, flowreservation.Config{Deadline: time.Hour})
	f.answered(t, time.Now().Add(time.Hour).Unix())
	emptyFRP := memory.NewScopedStore[sep2.FlowReservationResponse]()
	canceller := flowreservation.NewCanceller(f.frq, emptyFRP, f.queue, f.ledger, sources.NewWriters(f.issuer, f.frpLifecycles))

	err := canceller.Cancel(ctx, aggID, "R1", cancelledStatus(time.Now().Unix()))
	if !errors.Is(err, store.ErrNotFound) {
		t.Errorf("err = %v, want store.ErrNotFound", err)
	}
}

// relinkBackRefuser lets an execution move to the revision and refuses to
// move it back to the old grant, as a store that fails during rollback does.
type relinkBackRefuser struct {
	commitment.ExecutionWriter
	oldMRID string
}

func (w relinkBackRefuser) RelinkExecution(ctx context.Context, c commitment.Control, grantMRID string) error {
	if grantMRID == w.oldMRID {
		return errors.New("boom: relink back refused")
	}
	return w.ExecutionWriter.RelinkExecution(ctx, c, grantMRID)
}

// An execution that may still name the revision must keep the revision
// stored, or no walk of the chain reaches the execution again.
func TestRevise_FailedRelinkBackKeepsTheRevisionSoCancelReachesTheControl(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newCancelFixture(t, flowreservation.Config{Deadline: time.Hour})
	base := time.Now().Add(time.Hour).Unix()
	old := f.answered(t, base)
	exec := f.issueExec(t, old, base, -2000)

	real := sources.NewWriters(f.issuer, f.frpLifecycles)
	deps := f.reviseDeps()
	deps.Writers = commitment.Writers{
		Executions: relinkBackRefuser{ExecutionWriter: real.Executions, oldMRID: old.MRID},
		Grants:     &failingGrantWriter{inner: real.Grants},
	}
	_, err := flowreservation.Revise(ctx, deps, aggID, "R1", shorten(base, 1800), "operator revise", flowreservation.Attribution{}, time.Now())
	if !errors.Is(err, commitment.ErrUndo) {
		t.Fatalf("Revise err = %v, want ErrUndo", err)
	}
	rev := f.storedResponse(t, "R1-r1")
	lc, err := f.controlLifecycles.Get(ctx, exec.Scope.Key(), exec.ID)
	must(t, err)
	if lc.GrantMRID != rev.MRID {
		t.Fatalf("setup: execution linked to %q, want the revision %q", lc.GrantMRID, rev.MRID)
	}

	must(t, f.canceller.Cancel(ctx, aggID, "R1", cancelledStatus(time.Now().Unix())))

	lc, err = f.controlLifecycles.Get(ctx, exec.Scope.Key(), exec.ID)
	must(t, err)
	if lc.CancelledAt == nil {
		t.Errorf("execution lifecycle = %+v, want cancelled", lc)
	}
}

// growingChain serves a chain one member longer on every walk: the first
// member is the real R1 and the rest are zero-duration denials with no
// ledger record. It counts walks by reads of the first id.
type growingChain struct {
	flowreservation.FRPStore
	walks int
}

func (g *growingChain) Get(ctx context.Context, p, id string) (sep2.FlowReservationResponse, error) {
	if id == "R1" {
		g.walks++
	}
	ids := []string{"R1"}
	for len(ids) < g.walks {
		ids = append(ids, flowreservation.RevisionID(ids[len(ids)-1]))
	}
	if !slices.Contains(ids, id) {
		return sep2.FlowReservationResponse{}, store.ErrNotFound
	}
	if id == "R1" {
		return g.FRPStore.Get(ctx, p, id)
	}
	return ghostResponse("GHOST-"+id, "/edev/"+p+"/frp/"+id, 1, 0), nil
}

func TestCancel_ChainThatKeepsGrowingIsAnErrorAfterFourPasses(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newCancelFixture(t, flowreservation.Config{Deadline: time.Hour})
	f.answered(t, time.Now().Add(time.Hour).Unix())
	g := &growingChain{FRPStore: f.frp}
	canceller := flowreservation.NewCanceller(f.frq, g, f.queue, f.ledger, sources.NewWriters(f.issuer, f.frpLifecycles))

	err := canceller.Cancel(ctx, aggID, "R1", cancelledStatus(time.Now().Unix()))
	if !errors.Is(err, flowreservation.ErrChainMoving) {
		t.Fatalf("err = %v, want ErrChainMoving", err)
	}
	// Two walks per pass, four passes.
	if g.walks != 8 {
		t.Errorf("walks = %d, want 8", g.walks)
	}
	// The request's own response was still cancelled on the way.
	if lc, err := f.frpLifecycles.Get(ctx, aggID, "R1"); err != nil || lc.CancelledAt == nil {
		t.Errorf("R1 lifecycle = %+v (err %v), want cancelled", lc, err)
	}
}

// A member listed by a walk but gone by the time it is cancelled (a revision
// rolled back cleanly in between) must not turn a finished cancel into an
// error.
func TestCancel_MemberGoneAfterTheWalkIsNotAnError(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newCancelFixture(t, flowreservation.Config{Deadline: time.Hour})
	base := time.Now().Add(time.Hour).Unix()
	old := f.answered(t, base)
	exec := f.issueExec(t, old, base, -2000)

	var served bool
	canceller := flowreservation.NewCanceller(f.frq, frpHook{FRPStore: f.frp, get: func(ctx context.Context, p, id string) (sep2.FlowReservationResponse, error) {
		if id == "R1-r1" && !served {
			served = true
			return ghostResponse("GONE", "/edev/"+p+"/frp/R1-r1", base, 600), nil
		}
		return f.frp.Get(ctx, p, id)
	}}, f.queue, f.ledger, sources.NewWriters(f.issuer, f.frpLifecycles))

	must(t, canceller.Cancel(ctx, aggID, "R1", cancelledStatus(time.Now().Unix())))
	lc, err := f.controlLifecycles.Get(ctx, exec.Scope.Key(), exec.ID)
	must(t, err)
	if lc.CancelledAt == nil {
		t.Errorf("execution lifecycle = %+v, want cancelled", lc)
	}
}

// ghostResponse is a response a walk lists that the ledger has no record of.
func ghostResponse(mrid, href string, start int64, dur uint32) sep2.FlowReservationResponse {
	var frp sep2.FlowReservationResponse
	frp.MRID, frp.Href = mrid, href
	frp.Interval = &sep2.DateTimeInterval{Start: start, Duration: dur}
	return frp
}
