package sep2capture

import (
	"testing"
)

// TestDirectoryStaysUnderCapAndEvictsOldestFirst is Q7 item 3's first
// acceptance: a 2 MB cap, 256 KB segments, ten times that volume pushed
// through, checked at intervals against the directory's real size, and the
// oldest exchanges gone once eviction has run.
//
// Mutant (segment_writer.go, ensureRoomFor): replacing the loop body with
// `return nil` (never evicting) makes this RED: dirSize grows past capBytes
// well before the volume target is reached, since nothing ever deletes a
// segment.
func TestDirectoryStaysUnderCapAndEvictsOldestFirst(t *testing.T) {
	const (
		capBytes    = 2 * 1024 * 1024
		segBytes    = 256 * 1024
		recordBytes = 4 * 1024 // req + resp combined, before the header
		volume      = 10 * capBytes
	)

	dir := t.TempDir()
	st, err := NewStore(StoreConfig{Dir: dir, CapBytes: capBytes, SegmentBytes: segBytes})
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { closeStore(t, st) })

	n := volume / recordBytes
	checkEvery := n / 20
	if checkEvery == 0 {
		checkEvery = 1
	}
	for i := 0; i < n; i++ {
		id := uint64(i + 1)
		st.Record(makeExchange(id, id, "client-1", recordBytes/2, recordBytes/2))
		if i%checkEvery == checkEvery-1 {
			waitQueueDrained(t, st)
			if got := dirSize(t, dir); got > capBytes {
				t.Fatalf("dir size after %d records: got %d bytes, want <= %d (cap)", i+1, got, capBytes)
			}
		}
	}
	waitQueueDrained(t, st)
	if got := dirSize(t, dir); got > capBytes {
		t.Fatalf("final dir size: got %d bytes, want <= %d (cap)", got, capBytes)
	}

	stats := st.Stats()
	if stats.EvictedSegments == 0 {
		t.Fatal("EvictedSegments: got 0, want > 0 after pushing 10x the cap")
	}

	// Oldest-first: the very first exchange must be gone, and the very
	// last one must still be readable.
	if _, err := st.Exchange(1); err != ErrEvicted {
		t.Errorf("Exchange(1) after eviction: got err %v, want ErrEvicted", err)
	}
	if _, err := st.Exchange(uint64(n)); err != nil {
		t.Errorf("Exchange(%d) (most recent): got err %v, want nil", n, err)
	}
}

// TestEvictionNeverDeletesTheActiveSegment is a narrower unit test of
// ensureRoomFor's own boundary: with capBytes smaller than one segment,
// eviction must stop rather than delete the segment still being written
// to (Q4: "the active segment is never deleted").
func TestEvictionNeverDeletesTheActiveSegment(t *testing.T) {
	dir := t.TempDir()
	st, err := NewStore(StoreConfig{Dir: dir, CapBytes: 1024, SegmentBytes: 4096})
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { closeStore(t, st) })

	for i := 1; i <= 5; i++ {
		id := uint64(i)
		st.Record(makeExchange(id, id, "client-1", 200, 200))
	}
	waitQueueDrained(t, st)

	if len(st.liveSegs) == 0 {
		t.Fatal("no live segment after writing records well under a single segment's own size")
	}
	// The most recent exchange must still be there: if eviction had
	// touched the active segment, this would be ErrEvicted instead.
	if _, err := st.Exchange(5); err != nil {
		t.Errorf("Exchange(5) with the active segment still open: got %v, want nil", err)
	}
}
