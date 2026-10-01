package flowreservation_test

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/flowreservation"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

// storeOne stores request frqID under edevID with one response, which is
// built by mk and stored under the request's own id.
func (f *retentionFixture) storeOne(t *testing.T, edevID, frqID string, created int64, frp sep2.FlowReservationResponse) {
	t.Helper()
	ctx := context.Background()
	frq := sep2.FlowReservationRequest{MRID: "REQ-" + frqID, CreationTime: created}
	frq.Href = "/edev/" + edevID + "/frq/" + frqID
	must(t, f.frq.Create(ctx, edevID, frqID, frq))
	frp.Href = "/edev/" + edevID + "/frp/" + frqID
	if frp.MRID == "" {
		frp.MRID = "RESP-" + frqID
	}
	must(t, f.frp.Create(ctx, edevID, frqID, frp))
	must(t, f.answers.Create(ctx, edevID, frqID, flowreservation.AnswerRecord{Action: flowreservation.ActionAnswer}))
}

func (f *retentionFixture) requestStored(t *testing.T, edevID, frqID string) bool {
	t.Helper()
	_, err := f.frq.Get(context.Background(), edevID, frqID)
	if errors.Is(err, store.ErrNotFound) {
		return false
	}
	must(t, err)
	return true
}

// A response with no interval ends at its creation, and a denial created
// after the window it names ended (a late answer) ends at its creation too:
// neither is removed before its creation plus the grace.
func TestRetention_ResponseEndsNoEarlierThanItsCreation(t *testing.T) {
	t.Parallel()
	now := time.Now().Unix()
	g := int64(grace / time.Second)
	cases := []struct {
		name     string
		interval *sep2.DateTimeInterval
	}{
		{"no interval", nil},
		{"late denial of a window that ended long ago", &sep2.DateTimeInterval{Start: now - 10*g, Duration: 0}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			f := newRetentionFixture(t)
			frp := sep2.FlowReservationResponse{}
			frp.CreationTime = now - 100
			frp.Interval = c.interval
			f.storeOne(t, aggID, "R1", now-200, frp)

			removed, err := f.retention().Sweep(context.Background(), time.Unix(now, 0))
			must(t, err)
			if removed != 0 || !f.requestStored(t, aggID, "R1") {
				t.Fatalf("removed = %d, request stored %v; want kept until creation plus grace", removed, f.requestStored(t, aggID, "R1"))
			}
			removed, err = f.retention().Sweep(context.Background(), time.Unix(now-100+g, 0))
			must(t, err)
			if removed != 1 {
				t.Errorf("at creation plus grace: removed = %d, want 1", removed)
			}
		})
	}
}

// revisingFleets resolves fleets like the real resolver, and the first time
// it is asked it stores an active revision first: a revision that lands
// between the sweep's unlocked read and its fleet lock.
type revisingFleets struct {
	flowreservation.FleetResolver
	once   sync.Once
	revise func()
}

func (r *revisingFleets) FleetOf(ctx context.Context, edevID string) (string, error) {
	r.once.Do(r.revise)
	return r.FleetResolver.FleetOf(ctx, edevID)
}

func TestRetention_RevisionLandingBeforeTheLockKeepsTheChain(t *testing.T) {
	t.Parallel()
	f := newRetentionFixture(t)
	now := time.Now().Unix()
	end := now - int64(grace/time.Second) - 1
	f.seedChain(t, "R1", end-600, member{start: end - 600, dur: 600, created: end - 600})
	r := f.retention()
	r.Fleets = &revisingFleets{FleetResolver: r.Fleets, revise: func() {
		frp := f.responseFor(now-10, 3600, "RESP-R1-r1")
		frp.Href = "/edev/" + aggID + "/frp/R1-r1"
		frp.CreationTime = now - 10
		must(t, f.frp.Create(context.Background(), aggID, "R1-r1", frp))
	}}

	removed, err := r.Sweep(context.Background(), time.Unix(now, 0))
	must(t, err)
	if removed != 0 {
		t.Fatalf("removed = %d, want 0: the chain gained an active revision before the lock", removed)
	}
	got := f.held(t, "R1", []string{"R1", "R1-r1"})
	for _, k := range []string{"frq R1", "frp R1", "frp R1-r1"} {
		if !got[k] {
			t.Errorf("%s was removed", k)
		}
	}
}

type hrefRecorder struct {
	mu    sync.Mutex
	hrefs []string
}

func (n *hrefRecorder) Notify(_ context.Context, href string, _ uint8) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.hrefs = append(n.hrefs, href)
}

// Removing a request tells the device's response list subscribers once.
func TestRetention_RemovalNotifiesTheResponseList(t *testing.T) {
	t.Parallel()
	f := newRetentionFixture(t)
	now := time.Now().Unix()
	end := now - int64(grace/time.Second) - 1
	f.seedChain(t, "R1", end-600, member{start: end - 600, dur: 600, created: end - 600})
	f.seedChain(t, "R2", end-600, member{start: end - 600, dur: 600, created: end - 600})
	n := &hrefRecorder{}
	r := f.retention()
	r.Notifier = n

	removed, err := r.Sweep(context.Background(), time.Unix(now, 0))
	must(t, err)
	if removed != 2 {
		t.Fatalf("removed = %d, want 2", removed)
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	if len(n.hrefs) != 1 || n.hrefs[0] != flowreservation.ListHref(aggID) {
		t.Errorf("notified %v, want once for %s", n.hrefs, flowreservation.ListHref(aggID))
	}
}

// countingHandler counts warn-level records.
type countingHandler struct{ warns *atomic.Int32 }

func (h countingHandler) Enabled(context.Context, slog.Level) bool { return true }
func (h countingHandler) Handle(_ context.Context, r slog.Record) error {
	if r.Level == slog.LevelWarn {
		h.warns.Add(1)
	}
	return nil
}
func (h countingHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h countingHandler) WithGroup(string) slog.Handler      { return h }

// An ended chain whose device no longer exists, or has no LFDI, is removed
// without a fleet lock, as the orphan pass does, and logs no warning.
func TestRetention_UnresolvableFleetIsRemovedWithoutALock(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name   string
		device *sep2.EndDevice
	}{
		{"device gone", nil},
		{"device without an LFDI", &sep2.EndDevice{}},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			f := newRetentionFixture(t)
			if c.device != nil {
				must(t, f.devices.Create(context.Background(), "x1", *c.device))
			}
			now := time.Now().Unix()
			end := now - int64(grace/time.Second) - 1
			frp := f.responseFor(end-600, 600, "")
			frp.CreationTime = end - 600
			f.storeOne(t, "x1", "R1", end-600, frp)
			var warns atomic.Int32
			r := f.retention()
			r.Log = slog.New(countingHandler{&warns})

			removed, err := r.Sweep(context.Background(), time.Unix(now, 0))
			must(t, err)
			if removed != 1 || f.requestStored(t, "x1", "R1") {
				t.Errorf("removed = %d, request stored %v; want removed", removed, f.requestStored(t, "x1", "R1"))
			}
			if _, err := f.frp.Get(context.Background(), "x1", "R1"); !errors.Is(err, store.ErrNotFound) {
				t.Errorf("response after the sweep: %v, want not found", err)
			}
			if w := warns.Load(); w != 0 {
				t.Errorf("%d warnings, want none", w)
			}
		})
	}
}

// listFailingFRQ2 fails List for one parent.
type listFailingFRQ2 struct {
	*memory.ScopedStore[sep2.FlowReservationRequest]
	bad string
}

func (s listFailingFRQ2) List(ctx context.Context, p string, o store.ListOptions) (store.ListResult[sep2.FlowReservationRequest], error) {
	if p == s.bad {
		return store.ListResult[sep2.FlowReservationRequest]{}, errors.New("list failed")
	}
	return s.ScopedStore.List(ctx, p, o)
}

// A failed List still returns an error, but only after the failures already
// collected are logged and the orphan pass has run.
func TestRetention_FailedListStillLogsAndRunsTheOrphanPass(t *testing.T) {
	t.Parallel()
	f := newRetentionFixture(t)
	ctx := context.Background()
	now := time.Now().Unix()
	end := now - int64(grace/time.Second) - 1
	f.seedChain(t, "R1", end-600,
		member{start: end - 600, dur: 300, created: end - 600},
		member{start: end - 300, dur: 300, created: end - 300},
	)
	orphan := f.seedChain(t, "R9", end-600, member{start: end - 600, dur: 600, created: end - 600})
	must(t, f.frq.Delete(ctx, aggID, "R9"))
	must(t, f.devices.Create(ctx, "z1", sep2.EndDevice{LFDI: standaloneLFDI}))
	other := sep2.FlowReservationRequest{MRID: "Z"}
	other.Href = "/edev/z1/frq/RZ"
	must(t, f.frq.Create(ctx, "z1", "RZ", other))

	var warns atomic.Int32
	r := f.retention()
	r.FRQ = listFailingFRQ2{f.frq, "z1"}
	r.FRP = getFailingFRP{f.frp, "R1-r1"}
	r.Log = slog.New(countingHandler{&warns})

	if _, err := r.Sweep(ctx, time.Unix(now, 0)); err == nil {
		t.Fatal("Sweep = nil, want the List failure")
	}
	if w := warns.Load(); w < 1 {
		t.Errorf("%d warnings, want the unreadable chain logged", w)
	}
	noneHeld(t, f.held(t, "R9", orphan))
}

// blockingFRQ holds Parents until release is closed, so a sweep can be caught
// in flight.
type blockingFRQ struct {
	flowreservation.RetentionFRQ
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (b *blockingFRQ) Parents(ctx context.Context) ([]string, error) {
	b.once.Do(func() { close(b.entered) })
	<-b.release
	return b.RetentionFRQ.Parents(ctx)
}

// stop returns only after a sweep already running has returned.
func TestRetention_StopWaitsForASweepInProgress(t *testing.T) {
	t.Parallel()
	f := newRetentionFixture(t)
	b := &blockingFRQ{RetentionFRQ: f.frq, entered: make(chan struct{}), release: make(chan struct{})}
	r := f.retention()
	r.FRQ = b

	stop := r.Start(context.Background(), time.Millisecond, time.Now)
	select {
	case <-b.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("no sweep started")
	}
	var stopped atomic.Bool
	done := make(chan struct{})
	go func() {
		stop()
		stopped.Store(true)
		close(done)
	}()
	time.Sleep(100 * time.Millisecond)
	if stopped.Load() {
		t.Fatal("stop returned while a sweep was still running")
	}
	close(b.release)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("stop did not return after the sweep ended")
	}
}

// countingFRQ counts the sweeps that reach the request store.
type countingFRQ struct {
	flowreservation.RetentionFRQ
	n atomic.Int32
}

func (c *countingFRQ) Parents(ctx context.Context) ([]string, error) {
	c.n.Add(1)
	return c.RetentionFRQ.Parents(ctx)
}

// Start stops sweeping when its parent context ends, without stop.
func TestRetention_StartStopsWithItsParentContext(t *testing.T) {
	t.Parallel()
	f := newRetentionFixture(t)
	c := &countingFRQ{RetentionFRQ: f.frq}
	r := f.retention()
	r.FRQ = c
	ctx, cancel := context.WithCancel(context.Background())
	stop := r.Start(ctx, time.Millisecond, time.Now)
	defer stop()
	deadline := time.Now().Add(5 * time.Second)
	for c.n.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	cancel()
	time.Sleep(50 * time.Millisecond)
	before := c.n.Load()
	time.Sleep(100 * time.Millisecond)
	if after := c.n.Load(); after != before {
		t.Errorf("sweeps went on after the parent context ended: %d then %d", before, after)
	}
}

// An interval of zero or less takes RetentionInterval instead of panicking.
func TestRetention_StartToleratesANonPositiveInterval(t *testing.T) {
	t.Parallel()
	f := newRetentionFixture(t)
	for _, d := range []time.Duration{0, -time.Second} {
		stop := f.retention().Start(context.Background(), d, time.Now)
		stop()
	}
}

// A zero or negative grace is refused rather than removing chains the
// instant they end.
func TestRetention_RefusesANonPositiveGrace(t *testing.T) {
	t.Parallel()
	f := newRetentionFixture(t)
	now := time.Now().Unix()
	f.seedChain(t, "R1", now-600, member{start: now - 600, dur: 300, created: now - 600})
	for _, g := range []time.Duration{0, -time.Second} {
		r := f.retention()
		r.Grace = g
		if _, err := r.Sweep(context.Background(), time.Unix(now, 0)); !errors.Is(err, flowreservation.ErrIncompleteRetention) {
			t.Errorf("grace %v: Sweep error = %v, want ErrIncompleteRetention", g, err)
		}
	}
	if !f.requestStored(t, aggID, "R1") {
		t.Error("a sweep with no grace removed a chain")
	}
}
