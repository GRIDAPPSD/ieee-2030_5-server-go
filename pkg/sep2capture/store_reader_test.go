package sep2capture

import (
	"context"
	"testing"
	"time"
)

// TestSubscribeReceivesRecordedSummary is coverage-lane M2's first
// surviving mutant: dropping segment_writer.go's publish call.
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

// TestSubscribeChannelClosesOnContextCancel is coverage-lane M2's second
// surviving mutant: the channel never closed on ctx cancel.
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

// TestSubscribeCountsSlowSubscribers is coverage-lane M2's third surviving
// mutant: SlowSubscribers never counted. The channel is left undrained
// past subscriberBufferSize so publish's non-blocking send must fall back
// to its default branch.
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
