package sep2capture

import (
	"fmt"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/stalltest"
)

// TestStalledWriterNeverBlocksRecordAndCountsDrops is Q7 item 3's third
// acceptance. testBeforeWrite holds the writer goroutine on a channel the
// test controls, standing in for a stalled disk, while Record keeps being
// called: every call must return while the writer is still held, and once
// the byte-bounded queue fills, DroppedQueueFull must be counted rather
// than the call blocking. The writer is released only at cleanup, so a
// Record that waits on it can never return inside stalltest.CallBound. There
// is no wall-clock budget per call, since CPU load alone can exceed one.
//
// Mutants (store.go, Record):
//   - replacing the byte-cap admission loop's drop with a wait for room
//     (`time.Sleep(time.Millisecond); continue` in place of
//     `s.droppedQueueFull.Add(1); return`) blocks Record behind the
//     stalled writer, and this goes RED on stalltest.CallBound;
//   - replacing that loop with an unconditional `s.inFlightBytes.Add(size)`
//     goes RED on DroppedQueueFull staying 0.
func TestStalledWriterNeverBlocksRecordAndCountsDrops(t *testing.T) {
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
		ex := makeExchange(id, id, "client-1", bigDirection, bigDirection)
		stalltest.Returns(t, fmt.Sprintf("Record(%d)", id), func() { st.Record(ex) })
		if st.Stats().DroppedQueueFull > 0 {
			sawDrop = true
			break
		}
	}
	if !sawDrop {
		t.Fatal("DroppedQueueFull stayed 0 after enough large records to exceed writeQueueByteCap while stalled")
	}
}
