package sep2capture

import (
	"strings"
	"testing"
)

// TestIndexMemoryBudgetEvictsEarly is the operator's 2026-09-22 index
// memory cap (security lane M1): CapBytes alone lets a client with many
// small exchanges push the index heap far past what the disk cap would
// suggest. IndexMemBudgetBytes is set far below CapBytes here so eviction
// is driven only by the index budget, never by disk space, proving the
// early-eviction path fires on its own.
//
// Mutant (segment_writer.go, ensureRoomFor): dropping the overIndex check
// leaves the loop bounded solely by overDisk, so IndexMemoryEvictions
// stays 0 and IndexApproxBytes climbs unbounded past indexBudget.
func TestIndexMemoryBudgetEvictsEarly(t *testing.T) {
	const (
		capBytes    = 64 * 1024 * 1024 // generous: disk must never be the trigger
		segBytes    = 4 * 1024
		indexBudget = 8 * 1024 // small enough to force eviction quickly
		recordBytes = 400
	)

	dir := t.TempDir()
	st, err := NewStore(StoreConfig{Dir: dir, CapBytes: capBytes, SegmentBytes: segBytes, IndexMemBudgetBytes: indexBudget})
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { closeStore(t, st) })

	for i := 1; i <= 200; i++ {
		id := uint64(i)
		st.Record(makeExchange(id, id, "client-1", recordBytes/2, recordBytes/2))
	}
	waitQueueDrained(t, st)

	stats := st.Stats()
	if stats.IndexMemoryEvictions == 0 {
		t.Fatal("IndexMemoryEvictions: got 0, want > 0 after pushing well past the index memory budget")
	}

	// One segment's worth of entries is the documented transient
	// allowance (ensureRoomFor's own doc: "the index still over its
	// budget... by up to one record/entry on top of one segment"),
	// since the active segment can never be evicted.
	oneEntry := indexEntryBytes("GET", "/x", "client-1", "client-1-sfdi", "")
	slack := (segBytes/recordBytes + 2) * oneEntry
	if stats.IndexApproxBytes > indexBudget+slack {
		t.Errorf("IndexApproxBytes: got %d, want <= %d (budget %d plus one segment's transient slack)", stats.IndexApproxBytes, indexBudget+slack, indexBudget)
	}

	if got := dirSize(t, dir); got > capBytes {
		t.Errorf("dir size: got %d, want <= %d (disk cap must still hold)", got, capBytes)
	}

	// Oldest-first, same as the disk cap: id 1 must be gone.
	if _, err := st.Exchange(1); err != ErrEvicted {
		t.Errorf("Exchange(1) after index-memory eviction: got err %v, want ErrEvicted", err)
	}
}

// TestIndexTruncatesMethodLikePath is the security-lane M1 fix at
// record_parse.go: an oversized first token on the request line is capped
// the same way Path already is, so it cannot inflate index memory
// unbounded (a 1 MiB Method was the review's own reproduction).
//
// Mutant (record_parse.go, parseRequestLine): removing the Method
// truncation leaves method at its full length, so this stays RED with a
// 1 MiB-plus Method surviving into the Summary.
func TestIndexTruncatesMethodLikePath(t *testing.T) {
	dir := t.TempDir()
	st, err := NewStore(StoreConfig{Dir: dir})
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { closeStore(t, st) })

	hugeMethod := strings.Repeat("A", 1<<20)
	req := []byte(hugeMethod + " /x HTTP/1.1\r\n\r\n")
	resp := []byte("HTTP/1.1 200 OK\r\n\r\n")
	ex := makeExchange(1, 1, "client-1", 0, 0)
	ex.Request = Direction{Bytes: req, TrueLen: int64(len(req))}
	ex.Response = Direction{Bytes: resp, TrueLen: int64(len(resp))}
	st.Record(ex)
	waitQueueDrained(t, st)

	sums := st.Exchanges("client-1", 0, 0)
	if len(sums) != 1 {
		t.Fatalf("Exchanges: got %d, want 1", len(sums))
	}
	if got := len(sums[0].Method); got > maxIndexedMethod {
		t.Errorf("Method length: got %d, want <= %d (maxIndexedMethod)", got, maxIndexedMethod)
	}
	if sums[0].Method != hugeMethod[:maxIndexedMethod] {
		t.Errorf("Method: got the first %d bytes to differ from the huge method's own prefix, want them equal", maxIndexedMethod)
	}
}
