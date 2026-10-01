package flowreservation_test

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/commitment"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/commitment/sources"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/dercontrol"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/flowreservation"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
)

// Tests for GRIDAPPSD/ieee-2030_5-server-go#762: the startup pass that
// restores pending requests and finishes interrupted cancels and revisions.
// Each crash state is built directly in the stores, the way the write order
// of the design leaves it, and Recover is asserted by what the stores hold.

const hold = 300 * time.Second

func (f *cancelFixture) recoverDeps() flowreservation.RecoverDeps {
	return flowreservation.RecoverDeps{
		FRQ:        f.frq,
		FRP:        f.frp,
		Lifecycles: f.frpLifecycles,
		Queue:      f.queue,
		Ledger:     f.ledger,
		Writers:    sources.NewWriters(f.issuer, f.frpLifecycles),
	}
}

// pendingRequest stores a request created at created, with the href the POST
// handler gives it.
func (f *cancelFixture) pendingRequest(t *testing.T, id string, created int64, req sep2.FlowReservationRequest) sep2.FlowReservationRequest {
	t.Helper()
	req.CreationTime = created
	req.Href = "/edev/" + aggID + "/frq/" + id
	storeRequest(t, f.frq, aggID, id, req)
	return req
}

func (f *cancelFixture) cancelRequest(t *testing.T, id string, at int64) {
	t.Helper()
	frq := f.storedRequest(t, id)
	frq.RequestStatus = cancelledStatus(at)
	must(t, f.frq.Update(context.Background(), aggID, id, frq))
}

func (f *cancelFixture) responseLifecycle(t *testing.T, id string) (dercontrol.LifecycleRecord, bool) {
	t.Helper()
	lc, err := f.frpLifecycles.Get(context.Background(), aggID, id)
	if errors.Is(err, store.ErrNotFound) {
		return dercontrol.LifecycleRecord{}, false
	}
	must(t, err)
	return lc, true
}

func (f *cancelFixture) executionLifecycle(t *testing.T, res dercontrol.Result) dercontrol.LifecycleRecord {
	t.Helper()
	lc, err := f.controlLifecycles.Get(context.Background(), res.Scope.Key(), res.ID)
	must(t, err)
	return lc
}

// liveResponses returns the ids in R1's chain that carry no cancel mark.
func (f *cancelFixture) liveResponses(t *testing.T, frqID string) []string {
	t.Helper()
	chain, err := flowreservation.ChainOf(context.Background(), f.frp, aggID, frqID)
	must(t, err)
	var live []string
	for _, r := range chain {
		id, ok := flowreservation.ResponseID(aggID, r.Href)
		if !ok {
			t.Fatalf("response href %q has no id", r.Href)
		}
		if _, cancelled := f.responseLifecycle(t, id); !cancelled {
			live = append(live, id)
		}
	}
	return live
}

func TestQueueDeadlineAt(t *testing.T) {
	t.Parallel()
	f := newCancelFixture(t, flowreservation.Config{Deadline: hold})
	cases := []struct {
		name string
		frq  sep2.FlowReservationRequest
		want int64
	}{
		{"creation plus the bound", sep2.FlowReservationRequest{CreationTime: 1000, IntervalRequested: &sep2.DateTimeInterval{Start: 9000, Duration: 60}}, 1300},
		{"requested start sooner than the bound", sep2.FlowReservationRequest{CreationTime: 1000, IntervalRequested: &sep2.DateTimeInterval{Start: 1100, Duration: 60}}, 1100},
		{"no interval requested", sep2.FlowReservationRequest{CreationTime: 1000}, 1300},
	}
	for _, c := range cases {
		if got := f.queue.DeadlineAt(c.frq); got != c.want {
			t.Errorf("%s: DeadlineAt = %d, want %d", c.name, got, c.want)
		}
	}
}

func TestRecover_PendingRequestIsDecidedWhenItsHoldEnds(t *testing.T) {
	t.Parallel()
	now := time.Now()
	future := now.Add(time.Hour).Unix()
	cases := []struct {
		name      string
		created   int64
		start     int64
		wantDelay time.Duration
	}{
		{"200 s into a 300 s hold", now.Unix() - 200, future, 100 * time.Second},
		{"deadline passed while down", now.Unix() - 400, future, 0},
		{"requested start nearer than the deadline", now.Unix() - 200, now.Unix() + 30, 30 * time.Second},
		{"start passed while down", now.Unix() - 100, now.Unix() - 10, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			f := newCancelFixture(t, flowreservation.Config{Deadline: hold})
			timers := f.queue.RecordTimers()
			f.pendingRequest(t, "R1", c.created, windowRequest("REQ-PENDING", c.start, 3600, 10000))

			counts, err := flowreservation.Recover(context.Background(), f.recoverDeps(), now)
			must(t, err)

			if counts.Scanned != 1 || counts.Rearmed != 1 || counts.Failed != 0 {
				t.Fatalf("counts = %+v, want one request scanned and rearmed", counts)
			}
			live := timers.Live()
			if len(live) != 1 || live[0].Delay != c.wantDelay {
				t.Fatalf("armed timers = %+v, want one with delay %s", live, c.wantDelay)
			}
			if n := len(f.responses(t, aggID)); n != 0 {
				t.Fatalf("responses before the hold ends = %d, want 0", n)
			}

			live[0].Fire()
			frp := f.storedResponse(t, "R1")
			if frp.Subject != "REQ-PENDING" || frp.Interval == nil || frp.Interval.Start != c.start || frp.Interval.Duration != 3600 {
				t.Errorf("response subject %q interval %+v, want a grant of the requested window [%d, +3600) for REQ-PENDING", frp.Subject, frp.Interval, c.start)
			}
			if frp.EnergyAvailable == nil || frp.EnergyAvailable.Value != 10000 {
				t.Errorf("EnergyAvailable = %+v, want the requested 10000", frp.EnergyAvailable)
			}
		})
	}
}

func TestRecover_StartPassedWhileDownIsDeniedWhenTheWindowIsTaken(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	now := time.Now()
	f := newCancelFixture(t, flowreservation.Config{Deadline: hold})
	timers := f.queue.RecordTimers()
	start := now.Add(-10 * time.Second).Unix()
	storeRequest(t, f.frq, aggID, "R0", windowRequest("REQ-HOLDER", start, 3600, 10000))
	_, err := f.queue.Answer(ctx, aggID, "R0", flowreservation.Decision{})
	must(t, err)
	f.pendingRequest(t, "R1", now.Unix()-100, windowRequest("REQ-LATE", start, 3600, 10000))

	_, err = flowreservation.Recover(ctx, f.recoverDeps(), now)
	must(t, err)
	live := timers.Live()
	if len(live) != 1 || live[0].Delay != 0 {
		t.Fatalf("armed timers = %+v, want one decided at once", live)
	}
	live[0].Fire()

	frp := f.storedResponse(t, "R1")
	if frp.Subject != "REQ-LATE" || frp.Interval == nil || frp.Interval.Duration != 0 {
		t.Errorf("response subject %q interval %+v, want a zero-duration denial of REQ-LATE", frp.Subject, frp.Interval)
	}
}

func TestRecover_CancelledRequestWithNoResponseIsDenied(t *testing.T) {
	t.Parallel()
	f := newCancelFixture(t, flowreservation.Config{Deadline: hold})
	timers := f.queue.RecordTimers()
	start := time.Now().Add(time.Hour).Unix()
	f.pendingRequest(t, "R1", time.Now().Unix()-50, windowRequest("REQ-CANCELLED", start, 600, 10000))
	at := time.Now().Unix() - 20
	f.cancelRequest(t, "R1", at)

	counts, err := flowreservation.Recover(context.Background(), f.recoverDeps(), time.Now())
	must(t, err)

	if counts.DeniedCancelled != 1 || counts.Rearmed != 0 || counts.Failed != 0 {
		t.Fatalf("counts = %+v, want one cancelled request denied and none rearmed", counts)
	}
	if n := len(timers.Live()); n != 0 {
		t.Errorf("timers armed = %d, want 0 for a cancelled request", n)
	}
	resps := f.responses(t, aggID)
	if len(resps) != 1 {
		t.Fatalf("responses = %d, want exactly 1", len(resps))
	}
	frp := resps[0]
	if frp.Subject != "REQ-CANCELLED" || frp.Interval == nil || frp.Interval.Duration != 0 || frp.Interval.Start != start {
		t.Errorf("response subject %q interval %+v, want a zero-duration denial at the requested start %d", frp.Subject, frp.Interval, start)
	}
	if frq := f.storedRequest(t, "R1"); frq.RequestStatus != cancelledStatus(at) {
		t.Errorf("request status = %+v, want it left as cancelled at %d", frq.RequestStatus, at)
	}
}

func TestRecover_CancelledRequestWithLiveGrantCancelsGrantAndExecutions(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newCancelFixture(t, flowreservation.Config{Deadline: time.Hour})
	base := time.Now().Add(time.Hour).Unix()
	grant := f.answered(t, base)
	frq := f.storedRequest(t, "R1")
	frq.Href = "/edev/" + aggID + "/frq/R1"
	must(t, f.frq.Update(ctx, aggID, "R1", frq))
	e1 := f.issueExec(t, grant, base, -2000)
	e2 := f.issueExec(t, grant, base+600, -3000)
	f.cancelRequest(t, "R1", time.Now().Unix())

	counts, err := flowreservation.Recover(ctx, f.recoverDeps(), time.Now())
	must(t, err)

	if counts.GrantsCancelled != 1 || counts.Failed != 0 {
		t.Fatalf("counts = %+v, want one grant cancelled", counts)
	}
	lc, marked := f.responseLifecycle(t, "R1")
	if !marked || lc.CancelledAt == nil || lc.CancelReason != "client cancel" {
		t.Errorf("response lifecycle = %+v (present %v), want cancelled with reason client cancel", lc, marked)
	}
	for _, res := range []dercontrol.Result{e1, e2} {
		elc := f.executionLifecycle(t, res)
		if elc.CancelledAt == nil || elc.GrantMRID != grant.MRID {
			t.Errorf("execution %s lifecycle = %+v, want cancelled and still linked to %s", res.Control.MRID, elc, grant.MRID)
		}
	}
	if got := f.liveResponses(t, "R1"); len(got) != 0 {
		t.Errorf("live responses after the pass = %v, want none for a cancelled request", got)
	}

	again, err := flowreservation.Recover(ctx, f.recoverDeps(), time.Now())
	must(t, err)
	if again.GrantsCancelled != 0 || again.Untouched != 1 {
		t.Errorf("second pass counts = %+v, want the request untouched", again)
	}
}

// revisionPrefixWithExecutions stores the state a Revise leaves when it
// stops after creating the new response and before cancelling the old one:
// the new response sits at R1-r1 with the old subject and a later
// creationTime, and two live executions of the old grant still name it. dur
// is the new grant's duration; 0 makes the tip a denial.
func (f *cancelFixture) revisionPrefixWithExecutions(t *testing.T, dur uint32) (old, tip sep2.FlowReservationResponse, execs []dercontrol.Result) {
	t.Helper()
	ctx := context.Background()
	base := time.Now().Add(time.Hour).Unix()
	old = f.answered(t, base)
	frq := f.storedRequest(t, "R1")
	frq.Href = "/edev/" + aggID + "/frq/R1"
	must(t, f.frq.Update(ctx, aggID, "R1", frq))
	execs = []dercontrol.Result{f.issueExec(t, old, base, -2000), f.issueExec(t, old, base+600, -3000)}

	tip = old
	tip.MRID = "MRID-TIP"
	tip.Href = "/edev/" + aggID + "/frp/R1-r1"
	tip.CreationTime = old.CreationTime + 1
	tip.Interval = &sep2.DateTimeInterval{Start: base, Duration: dur}
	if dur == 0 {
		tip.EnergyAvailable = &sep2.SignedRealEnergy{}
		tip.PowerAvailable = &sep2.ActivePower{}
	}
	must(t, f.frp.Create(ctx, aggID, "R1-r1", tip))
	return old, tip, execs
}

func TestRecover_TwoLiveResponsesRollForwardWhenTheTipFits(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newCancelFixture(t, flowreservation.Config{Deadline: time.Hour})
	old, tip, execs := f.revisionPrefixWithExecutions(t, 1800)

	counts, err := flowreservation.Recover(ctx, f.recoverDeps(), time.Now())
	must(t, err)

	if counts.RevisionsRolledForward != 1 || counts.RevisionsRolledBack != 0 || counts.Failed != 0 {
		t.Fatalf("counts = %+v, want one revision rolled forward", counts)
	}
	if got, want := f.liveResponses(t, "R1"), []string{"R1-r1"}; !slices.Equal(got, want) {
		t.Errorf("live responses = %v, want only the tip %v", got, want)
	}
	lc, _ := f.responseLifecycle(t, "R1")
	if lc.CancelledAt == nil {
		t.Errorf("old response lifecycle = %+v, want a cancel mark", lc)
	}
	for _, res := range execs {
		elc := f.executionLifecycle(t, res)
		if elc.GrantMRID != tip.MRID || elc.CancelledAt != nil {
			t.Errorf("execution %s lifecycle = %+v, want live and linked to the tip %s, not the old %s", res.Control.MRID, elc, tip.MRID, old.MRID)
		}
	}
}

func TestRecover_TwoLiveResponsesRollBackWhenTheTipNoLongerFits(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newCancelFixture(t, flowreservation.Config{Deadline: time.Hour})
	// The tip lasts 600 s, so the second execution (600 s to 1200 s) does not fit.
	old, _, execs := f.revisionPrefixWithExecutions(t, 600)

	counts, err := flowreservation.Recover(ctx, f.recoverDeps(), time.Now())
	must(t, err)

	if counts.RevisionsRolledBack != 1 || counts.RevisionsRolledForward != 0 || counts.Failed != 0 {
		t.Fatalf("counts = %+v, want one revision rolled back", counts)
	}
	if got, want := f.liveResponses(t, "R1"), []string{"R1"}; !slices.Equal(got, want) {
		t.Errorf("live responses = %v, want only the old response %v", got, want)
	}
	if _, err := f.frp.Get(ctx, aggID, "R1-r1"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("tip lookup error = %v, want it deleted", err)
	}
	if _, marked := f.responseLifecycle(t, "R1"); marked {
		t.Error("old response gained a cancel mark on a roll back")
	}
	for _, res := range execs {
		elc := f.executionLifecycle(t, res)
		if elc.GrantMRID != old.MRID || elc.CancelledAt != nil {
			t.Errorf("execution %s lifecycle = %+v, want live and linked to the old %s", res.Control.MRID, elc, old.MRID)
		}
	}
}

func TestRecover_CancelledRequestWithTwoLiveResponsesEndsWithNoneLive(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newCancelFixture(t, flowreservation.Config{Deadline: time.Hour})
	_, _, execs := f.revisionPrefixWithExecutions(t, 1800)
	f.cancelRequest(t, "R1", time.Now().Unix())

	counts, err := flowreservation.Recover(ctx, f.recoverDeps(), time.Now())
	must(t, err)

	if counts.RevisionsRolledForward != 1 || counts.GrantsCancelled != 1 || counts.Failed != 0 {
		t.Fatalf("counts = %+v, want the revision finished and then the tip cancelled", counts)
	}
	if got := f.liveResponses(t, "R1"); len(got) != 0 {
		t.Errorf("live responses = %v, want none for a cancelled request", got)
	}
	for _, res := range execs {
		if elc := f.executionLifecycle(t, res); elc.CancelledAt == nil {
			t.Errorf("execution %s lifecycle = %+v, want cancelled", res.Control.MRID, elc)
		}
	}
}

func TestRecover_RevisionToDenialStoppedBeforeTheOldGrantWasMarked(t *testing.T) {
	t.Parallel()
	f := newCancelFixture(t, flowreservation.Config{Deadline: time.Hour})
	_, _, execs := f.revisionPrefixWithExecutions(t, 0)

	counts, err := flowreservation.Recover(context.Background(), f.recoverDeps(), time.Now())
	must(t, err)

	if counts.RevisionsRolledForward != 1 || counts.Failed != 0 {
		t.Fatalf("counts = %+v, want the revision to a denial rolled forward", counts)
	}
	if got, want := f.liveResponses(t, "R1"), []string{"R1-r1"}; !slices.Equal(got, want) {
		t.Errorf("live responses = %v, want only the denial %v", got, want)
	}
	for _, res := range execs {
		if elc := f.executionLifecycle(t, res); elc.CancelledAt == nil {
			t.Errorf("execution %s lifecycle = %+v, want cancelled", res.Control.MRID, elc)
		}
	}
}

func TestRecover_CancelledRequestAnsweredWithADenialIsLeftAlone(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newCancelFixture(t, flowreservation.Config{Deadline: time.Hour})
	f.pendingRequest(t, "R1", time.Now().Unix()-50, windowRequest("REQ-DENIED", time.Now().Add(time.Hour).Unix(), 600, 10000))
	_, err := f.queue.Answer(ctx, aggID, "R1", flowreservation.Decision{Kind: flowreservation.Deny})
	must(t, err)
	f.cancelRequest(t, "R1", time.Now().Unix())

	counts, err := flowreservation.Recover(ctx, f.recoverDeps(), time.Now())
	must(t, err)

	if counts.Untouched != 1 || counts.GrantsCancelled != 0 || counts.DeniedCancelled != 0 {
		t.Fatalf("counts = %+v, want the request untouched", counts)
	}
	if _, marked := f.responseLifecycle(t, "R1"); marked {
		t.Error("a denial gained a cancel mark; it commits nothing to cancel")
	}
	if got, want := f.liveResponses(t, "R1"), []string{"R1"}; !slices.Equal(got, want) {
		t.Errorf("live responses = %v, want the one denial %v", got, want)
	}
}

func TestRecover_AnsweredLiveRequestIsLeftAlone(t *testing.T) {
	t.Parallel()
	f := newCancelFixture(t, flowreservation.Config{Deadline: time.Hour})
	timers := f.queue.RecordTimers()
	base := time.Now().Add(time.Hour).Unix()
	before := f.answered(t, base)
	frq := f.storedRequest(t, "R1")
	frq.Href = "/edev/" + aggID + "/frq/R1"
	must(t, f.frq.Update(context.Background(), aggID, "R1", frq))

	counts, err := flowreservation.Recover(context.Background(), f.recoverDeps(), time.Now())
	must(t, err)

	if counts.Untouched != 1 || counts.Rearmed != 0 || counts.GrantsCancelled != 0 {
		t.Fatalf("counts = %+v, want the request untouched", counts)
	}
	if n := len(timers.Live()); n != 0 {
		t.Errorf("timers armed = %d, want 0 for an answered request", n)
	}
	if got := f.storedResponse(t, "R1"); got.MRID != before.MRID || got.Interval.Duration != 3600 {
		t.Errorf("response changed: %+v, was %+v", got, before)
	}
	if _, marked := f.responseLifecycle(t, "R1"); marked {
		t.Error("an answered live grant gained a cancel mark")
	}
}

// listFailingFRQ fails the listing named by its field, as a store whose file
// cannot be read does.
type listFailingFRQ struct {
	flowreservation.RecoverFRQ
	failParents, failList bool
}

var errRecoverStore = errors.New("fake: store unreadable")

func (s listFailingFRQ) Parents(ctx context.Context) ([]string, error) {
	if s.failParents {
		return nil, errRecoverStore
	}
	return s.RecoverFRQ.Parents(ctx)
}

func (s listFailingFRQ) List(ctx context.Context, parent string, opts store.ListOptions) (store.ListResult[sep2.FlowReservationRequest], error) {
	if s.failList {
		return store.ListResult[sep2.FlowReservationRequest]{}, errRecoverStore
	}
	return s.RecoverFRQ.List(ctx, parent, opts)
}

func TestRecover_StoreReadErrorFailsThePass(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name string
		frq  func(flowreservation.RecoverFRQ) flowreservation.RecoverFRQ
	}{
		{"listing owners", func(in flowreservation.RecoverFRQ) flowreservation.RecoverFRQ {
			return listFailingFRQ{RecoverFRQ: in, failParents: true}
		}},
		{"listing requests", func(in flowreservation.RecoverFRQ) flowreservation.RecoverFRQ {
			return listFailingFRQ{RecoverFRQ: in, failList: true}
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			f := newCancelFixture(t, flowreservation.Config{Deadline: hold})
			timers := f.queue.RecordTimers()
			f.pendingRequest(t, "R1", time.Now().Unix()-10, windowRequest("REQ", time.Now().Add(time.Hour).Unix(), 600, 10000))
			deps := f.recoverDeps()
			deps.FRQ = c.frq(deps.FRQ)

			_, err := flowreservation.Recover(context.Background(), deps, time.Now())
			if !errors.Is(err, errRecoverStore) {
				t.Fatalf("Recover error = %v, want it to wrap the store error", err)
			}
			if n := len(timers.Live()); n != 0 {
				t.Errorf("timers armed = %d after a failed pass, want 0", n)
			}
		})
	}
}

// failingLifecycles fails every read.
type failingLifecycles struct{}

func (failingLifecycles) Get(context.Context, string, string) (dercontrol.LifecycleRecord, error) {
	return dercontrol.LifecycleRecord{}, errRecoverStore
}

func TestRecover_ChainReadErrorFailsThePass(t *testing.T) {
	t.Parallel()
	f := newCancelFixture(t, flowreservation.Config{Deadline: time.Hour})
	f.answered(t, time.Now().Add(time.Hour).Unix())
	frq := f.storedRequest(t, "R1")
	frq.Href = "/edev/" + aggID + "/frq/R1"
	must(t, f.frq.Update(context.Background(), aggID, "R1", frq))
	deps := f.recoverDeps()
	deps.Lifecycles = failingLifecycles{}

	_, err := flowreservation.Recover(context.Background(), deps, time.Now())
	if !errors.Is(err, errRecoverStore) {
		t.Fatalf("Recover error = %v, want it to wrap the lifecycle read error", err)
	}
}

func TestRecover_OneRequestsFailureIsSkippedAndCounted(t *testing.T) {
	t.Parallel()
	f := newCancelFixture(t, flowreservation.Config{Deadline: hold})
	timers := f.queue.RecordTimers()
	start := time.Now().Add(time.Hour).Unix()
	// A request whose href names no store id cannot be recovered.
	bad := windowRequest("REQ-BAD", start, 600, 10000)
	bad.Href = "/not/a/request"
	storeRequest(t, f.frq, aggID, "R0", bad)
	f.pendingRequest(t, "R1", time.Now().Unix()-10, windowRequest("REQ-GOOD", start, 600, 10000))

	counts, err := flowreservation.Recover(context.Background(), f.recoverDeps(), time.Now())
	must(t, err)

	if counts.Scanned != 2 || counts.Failed != 1 || counts.Rearmed != 1 {
		t.Fatalf("counts = %+v, want two scanned, one failed, one rearmed", counts)
	}
	if n := len(timers.Live()); n != 1 {
		t.Errorf("timers armed = %d, want the good request's one", n)
	}
}

func TestRecover_RefusesIncompleteDeps(t *testing.T) {
	t.Parallel()
	f := newCancelFixture(t, flowreservation.Config{Deadline: hold})
	deps := f.recoverDeps()
	deps.Queue = nil
	if _, err := flowreservation.Recover(context.Background(), deps, time.Now()); !errors.Is(err, flowreservation.ErrIncompleteRecoverDeps) {
		t.Fatalf("Recover error = %v, want ErrIncompleteRecoverDeps", err)
	}
}

// lockProbe is a Notifier that reports whether the fleet lock was held when
// it was called: it tries to take the lock from another goroutine, which
// only succeeds once the caller has released it.
type lockProbe struct {
	ledger *commitment.Ledger
	mu     sync.Mutex
	calls  int
	held   int
}

func (p *lockProbe) Notify(ctx context.Context, _ string, _ uint8) {
	done := make(chan struct{})
	go func() {
		_ = p.ledger.Within(context.WithoutCancel(ctx), []string{aggLFDI}, func(commitment.View) error { return nil })
		close(done)
	}()
	held := false
	select {
	case <-done:
	case <-time.After(300 * time.Millisecond):
		held = true
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls++
	if held {
		p.held++
	}
}

func TestRecover_NotifiesOnlyAfterTheFleetLockIsReleased(t *testing.T) {
	t.Parallel()
	f := newCancelFixture(t, flowreservation.Config{Deadline: time.Hour})
	f.revisionPrefixWithExecutions(t, 1800)
	f.cancelRequest(t, "R1", time.Now().Unix())
	probe := &lockProbe{ledger: f.ledger}
	deps := f.recoverDeps()
	deps.Writers = flowreservation.NotifyingWriters(deps.Writers, probe)
	deps.Notifier = probe

	_, err := flowreservation.Recover(context.Background(), deps, time.Now())
	must(t, err)

	probe.mu.Lock()
	defer probe.mu.Unlock()
	if probe.calls != 1 {
		t.Errorf("notifications = %d, want one for the device (the revision and the cancel both changed its list)", probe.calls)
	}
	if probe.held != 0 {
		t.Errorf("%d notification(s) ran while the fleet lock was held", probe.held)
	}
}
