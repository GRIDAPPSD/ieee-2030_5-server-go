package flowreservation_test

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/commitment"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/dercontrol"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/flowreservation"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

// Tests for GRIDAPPSD/ieee-2030_5-server-go#672: removing a request once its
// responses have ended, and what a crash between two of its deletes leaves.

const grace = 1800 * time.Second

type retentionFixture struct {
	*cancelFixture
	answers *memory.ScopedStore[flowreservation.AnswerRecord]
}

func newRetentionFixture(t *testing.T) *retentionFixture {
	t.Helper()
	return &retentionFixture{
		cancelFixture: newCancelFixture(t, flowreservation.Config{Deadline: hold}),
		answers:       memory.NewScopedStore[flowreservation.AnswerRecord](),
	}
}

func (f *retentionFixture) retention() *flowreservation.Retention {
	return &flowreservation.Retention{
		FRQ:        f.frq,
		FRP:        f.frp,
		Lifecycles: f.frpLifecycles,
		Answers:    f.answers,
		Ledger:     f.ledger,
		Fleets:     commitment.Resolver{Devices: f.devices, Managers: f.managers},
		Grace:      grace,
	}
}

// member is one response of a seeded chain.
type member struct {
	start       int64
	dur         uint32
	created     int64
	cancelledAt *int64
}

// seedChain stores a request under frqID and one response per member, oldest
// first, each with an answer record, and a lifecycle record for each
// cancelled member. It returns the chain's ids.
func (f *retentionFixture) seedChain(t *testing.T, frqID string, created int64, members ...member) []string {
	t.Helper()
	ctx := context.Background()
	f.pendingRequest(t, frqID, created, windowRequest("REQ-"+frqID, members[0].start, members[0].dur, 1000))
	ids := make([]string, len(members))
	id := frqID
	for i, m := range members {
		if i > 0 {
			id = flowreservation.RevisionID(id)
		}
		ids[i] = id
		frp := f.responseFor(m.start, m.dur, fmt.Sprintf("RESP-%s-%d", frqID, i))
		frp.Href = "/edev/" + aggID + "/frp/" + id
		frp.CreationTime = m.created
		must(t, f.frp.Create(ctx, aggID, id, frp))
		must(t, f.answers.Create(ctx, aggID, id, flowreservation.AnswerRecord{
			Action: flowreservation.ActionAnswer, By: flowreservation.Attribution{Kind: flowreservation.KindOperator, At: m.created},
		}))
		if m.cancelledAt != nil {
			must(t, f.frpLifecycles.Create(ctx, aggID, id, dercontrol.LifecycleRecord{CancelledAt: m.cancelledAt, CancelReason: "test"}))
		}
	}
	return ids
}

// held reports which of the request, each response, its lifecycle record and
// its answer record are stored.
func (f *retentionFixture) held(t *testing.T, frqID string, ids []string) map[string]bool {
	t.Helper()
	ctx := context.Background()
	present := func(err error) bool {
		if errors.Is(err, store.ErrNotFound) {
			return false
		}
		must(t, err)
		return true
	}
	out := map[string]bool{}
	_, err := f.frq.Get(ctx, aggID, frqID)
	out["frq "+frqID] = present(err)
	for _, id := range ids {
		_, err := f.frp.Get(ctx, aggID, id)
		out["frp "+id] = present(err)
		_, err = f.frpLifecycles.Get(ctx, aggID, id)
		out["lc "+id] = present(err)
		_, err = f.answers.Get(ctx, aggID, id)
		out["ans "+id] = present(err)
	}
	return out
}

func noneHeld(t *testing.T, got map[string]bool) {
	t.Helper()
	for k, v := range got {
		if v {
			t.Errorf("%s still stored after the chain ended, want removed", k)
		}
	}
}

func ptr(v int64) *int64 { return &v }

// An ended chain goes with every record keyed like it, and only at its end
// plus the grace.
func TestRetention_EndedChainIsRemovedWithItsRecords(t *testing.T) {
	t.Parallel()
	f := newRetentionFixture(t)
	now := time.Now()
	end := now.Unix() - int64(grace/time.Second) - 1
	ids := f.seedChain(t, "frq-1", end-7200,
		member{start: end - 3600, dur: 3600, created: end - 7200, cancelledAt: ptr(end - 3700)},
		member{start: end - 1800, dur: 1800, created: end - 3700},
	)

	removed, err := f.retention().Sweep(context.Background(), now)
	must(t, err)
	if removed != 1 {
		t.Errorf("removed = %d, want 1", removed)
	}
	noneHeld(t, f.held(t, "frq-1", ids))
}

// #672 criterion 2: a request whose response is still active is retained and
// a client can still cancel it.
func TestRetention_ActiveGrantIsRetainedAndStillCancellable(t *testing.T) {
	t.Parallel()
	f := newRetentionFixture(t)
	now := time.Now()
	// Created more than a grace ago, so only the window's end can keep it.
	created := now.Unix() - 2*int64(grace/time.Second)
	ids := f.seedChain(t, "R1", created, member{start: now.Unix() - 300, dur: 3600, created: created})

	removed, err := f.retention().Sweep(context.Background(), now)
	must(t, err)
	if removed != 0 {
		t.Fatalf("removed = %d, want 0 while the grant is active", removed)
	}
	for k, v := range f.held(t, "R1", ids) {
		if want := k != "lc R1"; v != want {
			t.Errorf("%s stored = %v, want %v", k, v, want)
		}
	}

	at := now.Unix()
	must(t, f.canceller.Cancel(context.Background(), aggID, "R1", cancelledStatus(at)))
	lc, ok := f.responseLifecycle(t, "R1")
	if !ok || lc.CancelledAt == nil {
		t.Fatalf("after the cancel the grant carries no cancel mark: %+v (stored %v)", lc, ok)
	}
	if got := f.storedRequest(t, "R1").RequestStatus.RequestStatus; got != sep2.RequestStatusCancelled {
		t.Errorf("request status = %d, want Cancelled", got)
	}
}

// #672 criterion 3: a cancelled response, revised away long ago, does not
// remove its request while the later response is active.
func TestRetention_RevisedAwayResponseDoesNotRemoveItsRequestWhileTheTipIsActive(t *testing.T) {
	t.Parallel()
	f := newRetentionFixture(t)
	now := time.Now().Unix()
	long := now - 10*int64(grace/time.Second)
	ids := f.seedChain(t, "R1", long-60,
		member{start: long - 3600, dur: 600, created: long - 60, cancelledAt: ptr(long - 30)},
		member{start: now - 60, dur: 3600, created: long - 30},
	)

	removed, err := f.retention().Sweep(context.Background(), time.Unix(now, 0))
	must(t, err)
	if removed != 0 {
		t.Fatalf("removed = %d, want 0 while the revision is active", removed)
	}
	got := f.held(t, "R1", ids)
	for _, k := range []string{"frq R1", "frp " + ids[0], "lc " + ids[0], "ans " + ids[0], "frp " + ids[1], "ans " + ids[1]} {
		if !got[k] {
			t.Errorf("%s was removed while the chain's tip is active", k)
		}
	}
}

// A revision that shortened the window does not take the revised-away
// response with it before that response's own original end plus the grace.
func TestRetention_RevisedAwayResponseIsKeptUntilItsOwnOriginalEnd(t *testing.T) {
	t.Parallel()
	f := newRetentionFixture(t)
	now := time.Now().Unix()
	g := int64(grace / time.Second)
	tipEnd := now - g - 600
	ids := f.seedChain(t, "R1", tipEnd-7200,
		member{start: tipEnd - 3600, dur: 7200, created: tipEnd - 7200, cancelledAt: ptr(tipEnd - 3500)},
		member{start: tipEnd - 3500, dur: 3500, created: tipEnd - 3500},
	)

	removed, err := f.retention().Sweep(context.Background(), time.Unix(now, 0))
	must(t, err)
	if removed != 0 {
		t.Fatalf("removed = %d, want 0: the first response's original end plus grace is %d, after now %d", removed, tipEnd+3600+g, now)
	}
	if got := f.held(t, "R1", ids); !got["frq R1"] || !got["frp R1"] || !got["lc R1"] {
		t.Errorf("held = %v, want the request and the cancelled first response with its mark", got)
	}
}

// A cancelled grant stays served until its original end plus the grace,
// cancelled, and goes at that instant.
func TestRetention_CancelledGrantIsKeptUntilItsOriginalEndPlusGrace(t *testing.T) {
	t.Parallel()
	f := newRetentionFixture(t)
	start := time.Now().Unix() - 100000
	end := start + 3600
	ids := f.seedChain(t, "R1", start-600, member{start: start, dur: 3600, created: start - 600, cancelledAt: ptr(start - 300)})
	r := f.retention()
	served := flowreservation.NewDerivedStatusResponseStore(f.frp, f.frpLifecycles)

	before := time.Unix(end+int64(grace/time.Second)-1, 0)
	removed, err := r.Sweep(context.Background(), before)
	must(t, err)
	if removed != 0 {
		t.Fatalf("one second before end plus grace: removed = %d, want 0", removed)
	}
	frp, err := served.Get(context.Background(), aggID, "R1")
	must(t, err)
	if frp.EventStatus == nil || frp.EventStatus.CurrentStatus != sep2.EventStatusCancelled {
		t.Errorf("served EventStatus = %+v, want Cancelled until it is removed", frp.EventStatus)
	}

	removed, err = r.Sweep(context.Background(), before.Add(time.Second))
	must(t, err)
	if removed != 1 {
		t.Errorf("at end plus grace: removed = %d, want 1", removed)
	}
	noneHeld(t, f.held(t, "R1", ids))
}

// An unanswered request is never removed, however old.
func TestRetention_RequestWithNoResponseIsNeverRemoved(t *testing.T) {
	t.Parallel()
	f := newRetentionFixture(t)
	f.pendingRequest(t, "R1", 1000, windowRequest("REQ-OLD", 2000, 60, 1))

	removed, err := f.retention().Sweep(context.Background(), time.Now())
	must(t, err)
	if removed != 0 {
		t.Errorf("removed = %d, want 0", removed)
	}
	if got := f.storedRequest(t, "R1").MRID; got != "REQ-OLD" {
		t.Errorf("stored request mRID = %q, want REQ-OLD", got)
	}
}

// crashAfter fails every Delete, on any of the stores it wraps, once n have
// run: the state a process killed after n deletes leaves. order names each
// Delete that ran, including one that found nothing.
type crashAfter struct {
	mu    sync.Mutex
	n     int
	order []string
}

var errCrash = errors.New("injected crash")

func (c *crashAfter) del(name string, f func() error) error {
	c.mu.Lock()
	if c.n <= 0 {
		c.mu.Unlock()
		return errCrash
	}
	c.n--
	c.order = append(c.order, name)
	c.mu.Unlock()
	return f()
}

type crashFRQ struct {
	*memory.ScopedStore[sep2.FlowReservationRequest]
	c *crashAfter
}

func (s crashFRQ) Delete(ctx context.Context, p, id string) error {
	return s.c.del("frq "+id, func() error { return s.ScopedStore.Delete(ctx, p, id) })
}

type crashFRP struct {
	*memory.ScopedStore[sep2.FlowReservationResponse]
	c *crashAfter
}

func (s crashFRP) Delete(ctx context.Context, p, id string) error {
	return s.c.del("frp "+id, func() error { return s.ScopedStore.Delete(ctx, p, id) })
}

type crashRecords struct {
	inner flowreservation.RecordDeleter
	name  string
	c     *crashAfter
}

func (s crashRecords) Delete(ctx context.Context, p, id string) error {
	return s.c.del(s.name+" "+id, func() error { return s.inner.Delete(ctx, p, id) })
}

func (f *retentionFixture) crashingRetention(c *crashAfter) *flowreservation.Retention {
	r := f.retention()
	r.FRQ = crashFRQ{f.frq, c}
	r.FRP = crashFRP{f.frp, c}
	r.Lifecycles = crashRecords{f.frpLifecycles, "lc", c}
	r.Answers = crashRecords{f.answers, "ans", c}
	return r
}

// A crash after any delete leaves a state that startup recovery does not
// answer again, and that the next sweep removes. The one exception is named:
// once the first response is gone nothing lists its records, so a crash
// before they go leaves them unread.
func TestRetention_CrashBetweenTwoDeletesIsFinishedByTheNextSweep(t *testing.T) {
	t.Parallel()
	seed := func(t *testing.T, f *retentionFixture, now int64) []string {
		end := now - int64(grace/time.Second) - 1
		return f.seedChain(t, "R1", end-7200,
			member{start: end - 3600, dur: 3600, created: end - 7200, cancelledAt: ptr(end - 3700)},
			member{start: end - 1800, dur: 1800, created: end - 3700},
		)
	}
	full := &crashAfter{n: 1 << 30}
	{
		f := newRetentionFixture(t)
		now := time.Now()
		seed(t, f, now.Unix())
		_, err := f.crashingRetention(full).Sweep(context.Background(), now)
		must(t, err)
	}
	wantOrder := []string{"lc R1-r2", "ans R1-r2", "frp R1-r1", "lc R1-r1", "ans R1-r1", "frq R1", "frp R1", "lc R1", "ans R1"}
	if fmt.Sprint(full.order) != fmt.Sprint(wantOrder) {
		t.Errorf("delete order = %v, want %v", full.order, wantOrder)
	}

	for n := 0; n < len(wantOrder); n++ {
		t.Run(fmt.Sprintf("crash after %d deletes", n), func(t *testing.T) {
			t.Parallel()
			f := newRetentionFixture(t)
			timers := f.queue.RecordTimers()
			now := time.Now()
			ids := seed(t, f, now.Unix())

			if _, err := f.crashingRetention(&crashAfter{n: n}).Sweep(context.Background(), now); err != nil {
				t.Fatalf("Sweep: %v (a per-request failure is logged, not returned)", err)
			}

			counts, err := flowreservation.Recover(context.Background(), f.recoverDeps(), now)
			must(t, err)
			if counts.Rearmed != 0 || counts.DeniedCancelled != 0 || len(timers.Live()) != 0 {
				t.Fatalf("Recover after the crash: %+v, %d live timers; want nothing re-answered", counts, len(timers.Live()))
			}
			if _, err := f.frp.Get(context.Background(), aggID, "R1"); err == nil {
				if _, err := f.frq.Get(context.Background(), aggID, "R1"); err == nil {
					// The first response and the request are both still
					// there: nothing new may have been built under it.
					got, err := flowreservation.ChainOf(context.Background(), f.frp, aggID, "R1")
					must(t, err)
					if len(got) == 0 || got[0].MRID != "RESP-R1-0" {
						t.Fatalf("first response after Recover = %+v, want the seeded one", got)
					}
				}
			}

			_, err = f.retention().Sweep(context.Background(), now)
			must(t, err)
			got := f.held(t, "R1", append(ids, "R1-r2"))
			if n == 7 || n == 8 {
				// After "frp R1": the first response's records cannot be
				// listed once it and its request are gone.
				for k, v := range got {
					if want := (k == "lc R1" && n == 7) || k == "ans R1"; v != want {
						t.Errorf("%s stored = %v, want %v", k, v, want)
					}
				}
				return
			}
			noneHeld(t, got)
		})
	}
}

// The orphan sweep removes a response whose request is gone, with its
// records, and never a live response or its cancel mark.
func TestRetention_OrphanSweepNeverRemovesALiveResponsesCancelMark(t *testing.T) {
	t.Parallel()
	f := newRetentionFixture(t)
	ctx := context.Background()
	now := time.Now().Unix()
	live := f.seedChain(t, "R1", now-60, member{start: now + 3600, dur: 600, created: now - 60, cancelledAt: ptr(now - 10)})
	orphan := f.seedChain(t, "R9", now-60, member{start: now + 3600, dur: 600, created: now - 60, cancelledAt: ptr(now - 10)})
	must(t, f.frq.Delete(ctx, aggID, "R9"))

	removed, err := f.retention().Sweep(ctx, time.Unix(now, 0))
	must(t, err)
	if removed != 0 {
		t.Errorf("requests removed = %d, want 0", removed)
	}
	noneHeld(t, f.held(t, "R9", orphan))
	got := f.held(t, "R1", live)
	for k, v := range got {
		if !v {
			t.Errorf("%s of a live request was removed", k)
		}
	}
	lc, ok := f.responseLifecycle(t, "R1")
	if !ok || lc.CancelledAt == nil || *lc.CancelledAt != now-10 {
		t.Errorf("live response's cancel mark = %+v (stored %v), want CancelledAt %d", lc.CancelledAt, ok, now-10)
	}
}

// getFailingFRP fails Get for one id, so that request's chain cannot be read.
type getFailingFRP struct {
	*memory.ScopedStore[sep2.FlowReservationResponse]
	bad string
}

func (s getFailingFRP) Get(ctx context.Context, p, id string) (sep2.FlowReservationResponse, error) {
	if id == s.bad {
		return sep2.FlowReservationResponse{}, errors.New("disk read failed")
	}
	return s.ScopedStore.Get(ctx, p, id)
}

// A request whose chain cannot be read is skipped whole, and the sweep goes
// on to the next one.
func TestRetention_UnreadableChainIsSkippedAndNothingOfItDeleted(t *testing.T) {
	t.Parallel()
	f := newRetentionFixture(t)
	now := time.Now()
	end := now.Unix() - int64(grace/time.Second) - 1
	bad := f.seedChain(t, "R1", end-600,
		member{start: end - 600, dur: 300, created: end - 600, cancelledAt: ptr(end - 400)},
		member{start: end - 300, dur: 300, created: end - 400},
	)
	good := f.seedChain(t, "R2", end-600, member{start: end - 600, dur: 600, created: end - 600})
	r := f.retention()
	r.FRP = getFailingFRP{f.frp, "R1-r1"}

	removed, err := r.Sweep(context.Background(), now)
	must(t, err)
	if removed != 1 {
		t.Errorf("removed = %d, want 1 (R2 only)", removed)
	}
	for k, v := range f.held(t, "R1", bad) {
		if want := k != "lc R1-r1"; v != want {
			t.Errorf("%s stored = %v, want %v: nothing of an unreadable chain is deleted", k, v, want)
		}
	}
	noneHeld(t, f.held(t, "R2", good))
}

// lockProbeHandler is a slog handler that records whether the fleet lock was
// held when a record was logged.
type lockProbeHandler struct {
	probe *lockProbe
}

func (h lockProbeHandler) Enabled(context.Context, slog.Level) bool { return true }
func (h lockProbeHandler) Handle(ctx context.Context, _ slog.Record) error {
	h.probe.Notify(ctx, "", 0)
	return nil
}
func (h lockProbeHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h lockProbeHandler) WithGroup(string) slog.Handler      { return h }

// The sweep notifies and logs only once the fleet lock is released.
func TestRetention_HoldsNoLockWhileItNotifiesOrLogs(t *testing.T) {
	t.Parallel()
	f := newRetentionFixture(t)
	now := time.Now()
	end := now.Unix() - int64(grace/time.Second) - 1
	f.seedChain(t, "R1", end-600, member{start: end - 600, dur: 600, created: end - 600})
	f.seedChain(t, "R2", end-600, member{start: end - 600, dur: 600, created: end - 600})
	notes := &lockProbe{ledger: f.ledger}
	logs := &lockProbe{ledger: f.ledger}
	r := f.retention()
	r.Notifier = notes
	r.Log = slog.New(lockProbeHandler{logs})
	r.FRP = getFailingFRP{f.frp, "R2"}

	removed, err := r.Sweep(context.Background(), now)
	must(t, err)
	if removed != 1 {
		t.Fatalf("removed = %d, want 1", removed)
	}
	for name, p := range map[string]*lockProbe{"notifications": notes, "log records": logs} {
		p.mu.Lock()
		if p.calls == 0 {
			t.Errorf("no %s, want at least one", name)
		}
		if p.held != 0 {
			t.Errorf("%d of %d %s ran under the fleet lock", p.held, p.calls, name)
		}
		p.mu.Unlock()
	}
}

func TestRetention_RefusesIncompleteDeps(t *testing.T) {
	t.Parallel()
	f := newRetentionFixture(t)
	r := f.retention()
	r.Ledger = nil
	if _, err := r.Sweep(context.Background(), time.Now()); !errors.Is(err, flowreservation.ErrIncompleteRetention) {
		t.Fatalf("Sweep error = %v, want ErrIncompleteRetention", err)
	}
}

// Start's stop waits for the ticker goroutine and may be called twice.
func TestRetention_StartSweepsOnTheTickAndStops(t *testing.T) {
	t.Parallel()
	f := newRetentionFixture(t)
	now := time.Now()
	end := now.Unix() - int64(grace/time.Second) - 1
	ids := f.seedChain(t, "R1", end-600, member{start: end - 600, dur: 600, created: end - 600})

	stop := f.retention().Start(context.Background(), 10*time.Millisecond, time.Now)
	deadline := time.Now().Add(2 * time.Second)
	for f.held(t, "R1", ids)["frq R1"] && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	stop()
	stop()
	noneHeld(t, f.held(t, "R1", ids))
}
