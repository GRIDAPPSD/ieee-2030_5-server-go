package sep2capture

import (
	"testing"
	"time"
)

// TestStalledWriterNeverBlocksRecordAndCountsDrops is Q7 item 3's third
// acceptance. testBeforeWrite holds the writer goroutine on a channel the
// test controls, standing in for a stalled disk, while Record keeps being
// called: every call must return well inside recordBudget, and once the
// byte-bounded queue fills, DroppedQueueFull must be counted rather than
// the call blocking.
//
// Mutant (store.go, Record): replacing the byte-cap admission loop
// (`for { cur := s.inFlightBytes.Load(); if cur+size > writeQueueByteCap
// {...}; if CAS ... }`) with an unconditional `s.inFlightBytes.Add(size)`
// makes this RED: nothing ever refuses admission at this test's volume, so
// every record reaches the (still non-blocking) channel send and
// DroppedQueueFull stays 0.
func TestStalledWriterNeverBlocksRecordAndCountsDrops(t *testing.T) {
	const recordBudget = 200 * time.Millisecond

	dir := t.TempDir()
	st, err := NewStore(StoreConfig{Dir: dir, CapBytes: 8 * 1024 * 1024, SegmentBytes: 1024 * 1024})
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	release := make(chan struct{})
	entered := make(chan struct{}, 1)
	st.testBeforeWrite = func() {
		select {
		case entered <- struct{}{}:
		default:
			// Only the first write's arrival matters to the test; a
			// later one (once release fires) must not block trying to
			// signal a channel nobody drains anymore.
		}
		<-release
	}
	t.Cleanup(func() {
		close(release)
		closeStore(t, st)
	})

	// One record to get the writer goroutine stuck inside testBeforeWrite.
	st.Record(makeExchange(1, 1, "client-1", 1000, 1000))
	<-entered

	// Every direction is capped at 4 MiB, so one record is at most 8 MiB;
	// writeQueueByteCap is 64 MiB, so a handful of large records is
	// enough to exhaust it while the writer is stalled.
	const bigDirection = 3 * 1024 * 1024
	sawDrop := false
	for i := 2; i <= 40; i++ {
		id := uint64(i)
		start := time.Now()
		st.Record(makeExchange(id, id, "client-1", bigDirection, bigDirection))
		if elapsed := time.Since(start); elapsed > recordBudget {
			t.Fatalf("Record(%d) took %v with the writer stalled, want < %v", id, elapsed, recordBudget)
		}
		if st.Stats().DroppedQueueFull > 0 {
			sawDrop = true
			break
		}
	}
	if !sawDrop {
		t.Fatal("DroppedQueueFull stayed 0 after enough large records to exceed writeQueueByteCap while stalled")
	}
}
