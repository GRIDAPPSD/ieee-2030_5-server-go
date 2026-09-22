package sep2capture

import (
	"context"
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
	ch := st.Subscribe(ctx)

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
	ch := st.Subscribe(ctx)
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
	ch := st.Subscribe(ctx)
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
