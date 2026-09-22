package sep2capture

import (
	"context"
	"errors"
	"testing"
	"time"
)

// TestStoreCloseIsIdempotent is Q7 item 3's close requirement: safe to
// call twice. Mutant (store.go, Close): removing closeOnce.Do's guard (so
// both close(s.writeCh) calls run directly) makes this RED with a panic
// ("close of closed channel") instead of two nil returns.
func TestStoreCloseIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	st, err := NewStore(StoreConfig{Dir: dir})
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	st.Record(makeExchange(1, 1, "client-1", 100, 100))

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := st.Close(ctx); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := st.Close(ctx); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}

// TestStoreCloseBoundedByContextCountsAbandoned proves Close is bounded by
// its context even when the writer is stalled, and that what it could not
// flush is counted rather than silently lost.
//
// Mutant (store.go, Close): replacing the `case <-ctx.Done():` branch with
// nothing (so Close always waits for writerDone) makes this RED: Close
// blocks past the test's own timeout instead of returning within ctx's
// budget.
func TestStoreCloseBoundedByContextCountsAbandoned(t *testing.T) {
	dir := t.TempDir()
	st, err := NewStore(StoreConfig{Dir: dir})
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	release := make(chan struct{})
	entered := make(chan struct{}, 1)
	st.testBeforeWrite = func() {
		select {
		case entered <- struct{}{}:
		default:
		}
		<-release
	}
	// Release and fully close (bounded) rather than just unblocking and
	// leaving the writer goroutine to finish on its own time: t.TempDir()
	// removes its directory once every earlier-registered cleanup has
	// run, and an in-flight write racing that removal is exactly the
	// noisy-but-harmless log line this ordering avoids.
	t.Cleanup(func() {
		close(release)
		closeStore(t, st)
	})

	st.Record(makeExchange(1, 1, "client-1", 100, 100))
	<-entered
	st.Record(makeExchange(2, 2, "client-1", 100, 100))
	st.Record(makeExchange(3, 3, "client-1", 100, 100))

	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	start := time.Now()
	err = st.Close(ctx)
	elapsed := time.Since(start)

	if elapsed > time.Second {
		t.Fatalf("Close took %v with the writer stalled, want it bounded near ctx's 150ms budget", elapsed)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Close error: got %v, want context.DeadlineExceeded", err)
	}
	if got := st.Stats().Abandoned; got == 0 {
		t.Error("Abandoned: got 0, want > 0 (records 2 and 3 were still queued behind the stalled write)")
	}
}
