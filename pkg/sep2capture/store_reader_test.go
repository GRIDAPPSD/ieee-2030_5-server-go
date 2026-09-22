package sep2capture

import (
	"context"
	"runtime"
	"sync/atomic"
	"testing"
	"time"
)

// summaryIDs collects the ids from a slice of Summary, for assertion
// messages that read better than a struct dump.
func summaryIDs(sums []Summary) []uint64 {
	out := make([]uint64, len(sums))
	for i, s := range sums {
		out[i] = s.ID
	}
	return out
}

// TestExchangesAfterIDAndLimitSurviveOutOfOrderArrival is the Store-level
// half of TestIndexOutOfOrderArrivalKeepsExtremesAndOrdering: the same
// out-of-order arrival sequence (3, 1, 2), this time through Record and
// Exchanges, covering afterID's boundary and limit.
func TestExchangesAfterIDAndLimitSurviveOutOfOrderArrival(t *testing.T) {
	dir := t.TempDir()
	st, err := NewStore(StoreConfig{Dir: dir})
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { closeStore(t, st) })

	for _, id := range []uint64{3, 1, 2} {
		st.Record(makeExchange(id, id, "client-1", 100, 100))
	}
	waitQueueDrained(t, st)

	all := st.Exchanges("client-1", 0, 0)
	if len(all) != 3 {
		t.Fatalf("Exchanges(afterID=0, limit=0): got %d, want 3", len(all))
	}
	for i, want := range []uint64{1, 2, 3} {
		if all[i].ID != want {
			t.Errorf("Exchanges[%d].ID: got %d, want %d (ascending despite arrival order 3,1,2)", i, all[i].ID, want)
		}
	}

	after1 := st.Exchanges("client-1", 1, 0)
	if got := summaryIDs(after1); len(got) != 2 || got[0] != 2 || got[1] != 3 {
		t.Errorf("Exchanges(afterID=1): got ids %v, want [2 3]", got)
	}

	limited := st.Exchanges("client-1", 0, 2)
	if got := summaryIDs(limited); len(got) != 2 || got[0] != 1 || got[1] != 2 {
		t.Errorf("Exchanges(limit=2): got ids %v, want [1 2]", got)
	}
}

// TestSubscribeReceivesRecordedSummary: dropping segment_writer.go's
// publish call.
func TestSubscribeReceivesRecordedSummary(t *testing.T) {
	dir := t.TempDir()
	st, err := NewStore(StoreConfig{Dir: dir})
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { closeStore(t, st) })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch := st.Subscribe(ctx, nil)

	st.Record(makeExchange(1, 1, "client-1", 100, 100))

	select {
	case sum := <-ch:
		if sum.ID != 1 {
			t.Errorf("Subscribe summary ID: got %d, want 1", sum.ID)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Subscribe: no summary received within 2s of Record")
	}
}

// TestSubscribeChannelClosesOnContextCancel: the channel never closed on
// ctx cancel.
func TestSubscribeChannelClosesOnContextCancel(t *testing.T) {
	dir := t.TempDir()
	st, err := NewStore(StoreConfig{Dir: dir})
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { closeStore(t, st) })

	ctx, cancel := context.WithCancel(context.Background())
	ch := st.Subscribe(ctx, nil)
	cancel()

	select {
	case _, ok := <-ch:
		if ok {
			t.Fatal("Subscribe channel: got a value, want it closed with no value after ctx cancel")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Subscribe channel: still open 2s after ctx cancel")
	}
}

// TestSubscribeCtxDoneDoesNotForceExpire is item 5's acceptance (PR 620
// review, silent-failure MEDIUM 3): ending a subscription through its own
// ctx.Done() (an ordinary disconnect) must never call forceExpire, only
// the slow-reader drop and Store.Close do (closeSubscription's doc,
// store_reader.go). Proven directly against Subscribe's own contract
// rather than by racing a real connection's close against the callback
// over HTTP: whether forceExpire's own SetWriteDeadline call actually
// observes an already-closed fd there depends on how far net/http's own
// connection teardown has gotten by that moment, which is not
// deterministic either way. called is read only after the channel closes:
// close(sub.ch) in closeSubscription always runs after the forceExpire
// call in the same once.Do body, so seeing the channel closed is proof
// that call already happened, or already provably did not.
//
// Mutant (store_reader.go, Subscribe): changing the ctx.Done() goroutine's
// `s.closeSubscription(sub, false)` to `s.closeSubscription(sub, true)`
// makes this RED: called reads true.
func TestSubscribeCtxDoneDoesNotForceExpire(t *testing.T) {
	dir := t.TempDir()
	st, err := NewStore(StoreConfig{Dir: dir})
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { closeStore(t, st) })

	var called atomic.Bool
	ctx, cancel := context.WithCancel(context.Background())
	ch := st.Subscribe(ctx, func() { called.Store(true) })
	cancel()

	select {
	case _, ok := <-ch:
		if ok {
			t.Fatal("Subscribe channel: got a value, want it closed with no value after ctx cancel")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Subscribe channel: still open 2s after ctx cancel")
	}

	if called.Load() {
		t.Fatal("forceExpire: got called, want not called (ctx.Done() ending a subscription must never force the write deadline)")
	}
}

// TestSubscribeCountsSlowSubscribers: SlowSubscribers never counted. The
// channel is left undrained past subscriberBufferSize so publish's
// non-blocking send must fall back to its default branch.
func TestSubscribeCountsSlowSubscribers(t *testing.T) {
	dir := t.TempDir()
	st, err := NewStore(StoreConfig{Dir: dir})
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { closeStore(t, st) })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch := st.Subscribe(ctx, nil)
	_ = ch // never drained: that is what forces publish's default branch

	for i := 1; i <= subscriberBufferSize+5; i++ {
		id := uint64(i)
		st.Record(makeExchange(id, id, "client-1", 100, 100))
	}
	waitQueueDrained(t, st)

	if got := st.Stats().SlowSubscribers; got == 0 {
		t.Error("SlowSubscribers: got 0, want > 0 after overflowing an undrained subscriber's buffer")
	}
}

// TestSummariesAfterOrdersByPublishSequenceNotID is coverage-lane MEDIUM 1
// (round 2): replay order was never asserted, so sorting by ID instead of
// Seq left every existing test green. Publish order here (ids 5,4,3,2,1)
// is the reverse of id order, so the two orderings disagree: a client
// resuming mid-replay records the highest Seq it saw, so any lower Seq it
// never received would fall permanently below its own resume point.
//
// Mutant (store_reader.go, summariesAfter): changing the sort key from
// `out[i].Seq < out[j].Seq` to `out[i].ID < out[j].ID` makes this RED: got
// [1 2 3 4 5] (ascending by ID), want [5 4 3 2 1] (ascending by Seq, the
// order they actually published in).
func TestSummariesAfterOrdersByPublishSequenceNotID(t *testing.T) {
	st := newTestStore(t)
	for _, id := range []uint64{5, 4, 3, 2, 1} {
		st.Record(makeExchange(id, id, "client-1", 32, 32))
	}
	waitQueueDrained(t, st)

	got := summaryIDs(st.summariesAfter(0))
	want := []uint64{5, 4, 3, 2, 1}
	if !idsEqual(got, want) {
		t.Fatalf("summariesAfter(0) ids in Seq order: got %v, want %v (publish order, not ascending by ID)", got, want)
	}
}

// TestSummariesAfterCostIsProportionalToWhatItReplaysNotToTheIndex is
// silent-failure-lane MEDIUM 2 (round 2): a reconnect missing only a
// handful of events used to allocate and sort a slice sized for the WHOLE
// index regardless of afterSeq, holding idx.mu throughout: the writer
// goroutine needs the same lock to index a new record (segment_writer.go,
// writeOne -> index.add), so a burst of reconnects near a large index
// could stall capture. Measured before this fix, 200000 entries, a resume
// missing 5: about 13.7ms held and about 40MB allocated for a 5-entry
// result (the review's own probe). This proves the allocation no longer
// scales with the index: a bare *Store with a hand-built index is enough,
// since summariesAfter only ever touches s.idx.
//
// Mutant (store_reader.go, summariesAfter): reverting to
// `make([]Summary, 0, len(s.idx.byID))` makes this RED: allocated bytes
// jump from a 5-entry result to a 200000-entry one, well past budget.
func TestSummariesAfterCostIsProportionalToWhatItReplaysNotToTheIndex(t *testing.T) {
	const total = 200000
	const missed = 5

	st := &Store{idx: newIndex()}
	now := time.Now()
	for i := uint64(1); i <= total; i++ {
		st.idx.add(exchangeEntry{
			Summary: Summary{ID: i, Seq: i, ClientKey: "client-1", Started: now, Ended: now, Method: "GET", Path: "/x"},
			segment: 0,
		})
	}

	afterSeq := uint64(total - missed)

	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	start := time.Now()
	out := st.summariesAfter(afterSeq)
	elapsed := time.Since(start)
	runtime.ReadMemStats(&after)

	if len(out) != missed {
		t.Fatalf("summariesAfter(%d) out of %d: got %d entries, want %d", afterSeq, total, len(out), missed)
	}

	allocated := after.TotalAlloc - before.TotalAlloc
	// Far above a 5-entry result (a few hundred bytes even under -race
	// instrumentation) and far below a 200000-entry allocation (roughly
	// 200 bytes/entry * 200000 =~ 40MB, the pre-fix shape), so this bound
	// distinguishes the two without being sensitive to GC or race-mode
	// overhead on this host.
	const budget = 4 * 1024 * 1024
	if allocated > budget {
		t.Errorf("bytes allocated by summariesAfter for a %d-entry result out of a %d-entry index: got %d, want < %d (proportional to what was replayed, not to the index)", missed, total, allocated, budget)
	}
	t.Logf("summariesAfter(missed %d of %d): %v elapsed, %d bytes allocated", missed, total, elapsed, allocated)
}

// TestWriterKeepsIndexingWhileSummariesAfterRunsRepeatedly is the other
// half of the same fix: repeated near-max summariesAfter calls (the
// "reconnects repeated" shape the review measured) must not starve the
// writer goroutine of idx.mu for long. Interleaved rather than truly
// concurrent, to stay deterministic: each round records one new exchange,
// drains it, then calls summariesAfter once.
//
// Asserted on cumulative bytes allocated across all rounds, not wall-clock
// (PR 620 review, coverage MEDIUM 2, round 3): a 2s wall budget failed 1 of
// 3 -race iterations on the review's own host at 2.013s, and the fix's own
// documented mutant only moved the measured window by about 0.09s against
// roughly 0.5s of run-to-run noise, so it did not reliably fail even when
// reverted. The fixed cost is proportional to what each call replays (5
// entries * rounds), never to the index, so allocation separates the two
// cases by more than two orders of magnitude regardless of host load or
// -race instrumentation overhead.
//
// Mutant (store_reader.go, summariesAfter): reverting to
// `make([]Summary, 0, len(s.idx.byID))` makes this RED: allocated bytes
// jump from a 5-entries-per-round result to a 200000-entry one every
// round, well past budget.
func TestWriterKeepsIndexingWhileSummariesAfterRunsRepeatedly(t *testing.T) {
	const total = 200000
	const rounds = 20
	// Far above what 20 rounds of a 5-entry result cost (a few thousand
	// bytes) and far below what 20 rounds of a 200000-entry allocation
	// would cost (roughly 20 * 40MB =~ 800MB, the pre-fix shape).
	const allocBudget = 8 * 1024 * 1024

	st := newTestStore(t)
	now := time.Now()
	for i := uint64(1); i <= total; i++ {
		st.idx.add(exchangeEntry{
			Summary: Summary{ID: i, Seq: i, ClientKey: "client-1", Started: now, Ended: now, Method: "GET", Path: "/x"},
			segment: 0,
		})
	}
	// The writer goroutine's own Seq counter must agree with the index
	// just built directly, or the real Record calls below would collide
	// with ids already indexed.
	st.nextPublishSeq = total
	st.maxPublishSeq.Store(total)

	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	start := time.Now()
	for i := uint64(1); i <= rounds; i++ {
		id := total + i
		st.Record(makeExchange(id, id, "client-1", 32, 32))
		waitQueueDrained(t, st)
		if got := len(st.summariesAfter(total + i - 5)); got != 5 {
			t.Fatalf("round %d: summariesAfter near-max entries: got %d, want 5", i, got)
		}
	}
	elapsed := time.Since(start)
	runtime.ReadMemStats(&after)

	allocated := after.TotalAlloc - before.TotalAlloc
	if allocated > allocBudget {
		t.Fatalf("%d rounds of Record+summariesAfter against a %d-entry index allocated %d bytes, want < %d (the writer must not be starved of idx.mu by repeated reconnects re-scanning the whole index)", rounds, total, allocated, allocBudget)
	}
	if got := st.Stats().IndexEntries; got != total+rounds {
		t.Fatalf("IndexEntries after %d rounds: got %d, want %d (every new record indexed, none dropped)", rounds, got, total+rounds)
	}
	t.Logf("%d rounds against a %d-entry index: %v elapsed, %d bytes allocated", rounds, total, elapsed, allocated)
}
