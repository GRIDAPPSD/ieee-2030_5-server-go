package sep2capture

import (
	"strings"
	"testing"
)

// TestIndexMemoryBudgetEvictsEarly is the operator's 2026-09-22 index
// memory cap: CapBytes alone lets a client with many small exchanges push
// the index heap far past what the disk cap would suggest. IndexMemBudgetBytes is set far below CapBytes here so eviction
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
	if stats.EvictedSegments != 0 {
		t.Errorf("EvictedSegments: got %d, want 0 (every eviction here is index-budget driven, the generous disk cap is never the cause)", stats.EvictedSegments)
	}

	// This scenario's slack is roughly one live segment's worth of
	// entries: segBytes is small enough that ordinary disk-size rollover
	// reaches the budget before the early-roll path in ensureRoomFor ever
	// has to trigger, unlike TestIndexBudgetHoldsWithoutSegmentRoll below.
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

// TestIndexTruncatesMethodLikePath is record_parse.go's fix: an oversized
// first token on the request line is capped the same way Path already is,
// so it cannot inflate index memory unbounded (a 1 MiB Method was the
// original reproduction).
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

// TestParseRequestLineTruncatesMethodAtBoundary pins the boundary a 1
// MiB-scale Method cannot: maxIndexedMethod itself must pass through
// whole, and one byte past it must be cut back to maxIndexedMethod.
//
// Mutant (record_parse.go, parseRequestLine): changing
// `len(m) > maxIndexedMethod` to `len(m) > maxIndexedMethod+1` makes the
// "one byte over" case RED, since a 33-byte Method then survives whole
// (only a 34-byte or longer Method would still be truncated).
func TestParseRequestLineTruncatesMethodAtBoundary(t *testing.T) {
	cases := []struct {
		name      string
		methodLen int
		wantLen   int
	}{
		{"exactly at the cap: kept whole", maxIndexedMethod, maxIndexedMethod},
		{"one byte over the cap: truncated", maxIndexedMethod + 1, maxIndexedMethod},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			method := strings.Repeat("A", tc.methodLen)
			line := []byte(method + " /x HTTP/1.1\r\n")

			gotMethod, _ := parseRequestLine(line)

			if len(gotMethod) != tc.wantLen {
				t.Errorf("Method length: got %d, want %d", len(gotMethod), tc.wantLen)
			}
			if gotMethod != method[:tc.wantLen] {
				t.Errorf("Method: got %q, want the input method's first %d bytes", gotMethod, tc.wantLen)
			}
		})
	}
}

// TestIndexBudgetHoldsWithoutSegmentRoll: SegmentBytes is set large enough
// that these small exchanges never fill it on disk within the run, so
// ensureRoomFor's own eviction is the only thing that can ever bound the
// index: nothing else rolls the active segment. Before the fix,
// ensureRoomFor breaks out as soon as the oldest live segment is the active
// one, so the index budget is never enforced while only one segment exists
// and IndexApproxBytes grows without any eviction at all.
//
// The check-before-add design gives a tight guarantee once the active
// segment can be rolled early for index reasons: ensureRoomFor only
// returns once approxBytes+entryEstimate <= indexMemBudget, so
// IndexApproxBytes should never exceed the budget by more than a couple of
// entries, nowhere near a whole segment's worth (tens of thousands of
// entries at these sizes).
//
// Mutant: reverting ensureRoomFor to break unconditionally when the oldest
// live segment is the active one (the pre-fix behavior) makes this RED:
// IndexApproxBytes grows linearly with the number of records, with zero
// evictions of any kind. A real-heap check was tried here too and removed:
// at n=2000 the estimate-bound entries measure far under any ceiling loose
// enough to survive -race, so it never failed even with evictSegment
// itself disabled (TestIndexMemoryAtFullCap bands the estimate against
// real heap at a scale where that distinction shows).
func TestIndexBudgetHoldsWithoutSegmentRoll(t *testing.T) {
	const (
		capBytes    = 8 * 1024 * 1024 // generous: disk must never be the trigger
		segBytes    = 4 * 1024 * 1024 // large enough that 2000 small records never roll it
		indexBudget = 64 * 1024       // small next to segBytes: the index alone must force eviction
		reqLen      = 25
		respLen     = 25
		n           = 2000
	)

	dir := t.TempDir()

	st, err := NewStore(StoreConfig{Dir: dir, CapBytes: capBytes, SegmentBytes: segBytes, IndexMemBudgetBytes: indexBudget})
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { closeStore(t, st) })

	for i := 1; i <= n; i++ {
		id := uint64(i)
		st.Record(makeExchange(id, id, "client-1", reqLen, respLen))
	}
	waitQueueDrained(t, st)

	stats := st.Stats()
	oneEntry := indexEntryBytes("GET", "/x", "client-1", "client-1-sfdi", "")

	if stats.IndexMemoryEvictions == 0 {
		t.Fatal("IndexMemoryEvictions: got 0, want > 0 (the index budget must force eviction with only one segment live)")
	}

	estimateCeiling := indexBudget + 4*oneEntry
	if stats.IndexApproxBytes > estimateCeiling {
		t.Errorf("IndexApproxBytes: got %d, want <= %d (budget %d plus a few entries, nowhere near one segment's %d-entry capacity)",
			stats.IndexApproxBytes, estimateCeiling, indexBudget, segBytes/(recordHeaderLen+reqLen+respLen))
	}

	if got := dirSize(t, dir); got > capBytes {
		t.Errorf("dir size: got %d, want <= %d (disk cap must still hold)", got, capBytes)
	}
}

// TestNewStoreDefaultsIndexMemBudget pins defaultIndexMemBudgetBytes
// (store.go): a zero IndexMemBudgetBytes in StoreConfig must fall back to
// exactly the operator's 2026-09-22 256 MB default, not any other value.
//
// Mutant (store.go): changing defaultIndexMemBudgetBytes survives every
// other test in this file, since none of them build a Store without
// setting IndexMemBudgetBytes explicitly.
func TestNewStoreDefaultsIndexMemBudget(t *testing.T) {
	dir := t.TempDir()
	st, err := NewStore(StoreConfig{Dir: dir})
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { closeStore(t, st) })

	// The literal, not the defaultIndexMemBudgetBytes identifier: comparing
	// against the constant it is itself assigned from would pass no matter
	// what value that constant held (the coverage re-review's exact miss).
	const wantDefault = 256 * 1024 * 1024
	if st.indexMemBudget != wantDefault {
		t.Errorf("indexMemBudget: got %d, want %d (the operator's 2026-09-22 256 MB default)", st.indexMemBudget, wantDefault)
	}
}

// TestIndexMemoryBudgetSmallerThanOneEntryTerminates: a budget below what
// one entry's own indexEntryBytes estimate costs must still make progress,
// never spin ensureRoomFor forever trying to admit an entry it can never
// fit under budget. ensureRoomFor's own two guards (rolledForIndex,
// s.active.size==0) turn out redundant with each other on this call path,
// since a fresh roll always leaves size==0 before ensureRoomFor's caller
// ever writes to it; only dropping both hangs it. waitQueueDrained's own
// 5s deadline is the RED signal: with both gone, ensureRoomFor never
// returns and inFlightBytes never reaches 0.
//
// Mutant (segment_writer.go, ensureRoomFor): dropping both
// `rolledForIndex ||` and `s.active.size == 0` from the break condition
// hangs this test until waitQueueDrained's deadline fires.
func TestIndexMemoryBudgetSmallerThanOneEntryTerminates(t *testing.T) {
	dir := t.TempDir()
	st, err := NewStore(StoreConfig{Dir: dir, IndexMemBudgetBytes: 1})
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { closeStore(t, st) })

	for i := 1; i <= 20; i++ {
		id := uint64(i)
		st.Record(makeExchange(id, id, "client-1", 25, 25))
	}
	waitQueueDrained(t, st)

	if got := st.Stats().IndexMemoryEvictions; got == 0 {
		t.Error("IndexMemoryEvictions: got 0, want > 0 (a budget under one entry must still evict and make progress, not just wedge)")
	}
}
