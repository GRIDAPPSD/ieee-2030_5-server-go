package sep2capture

import (
	"testing"
	"time"
)

// TestEvictSegmentIsLinearNotQuadratic is P3 (security lane M2):
// removeSorted's per-id slice shift, one call per evicted id under idx.mu,
// made evicting many ids from one busy client's slice quadratic (10k of
// 100k ids took 338ms). This puts all of one segment's ids on a single
// client and times the eviction against a budget well under that.
//
// Mutant (index.go, evictSegment): replacing the removeIDs batching with a
// removeSorted call per id (the pre-fix shape) makes this RED past the
// budget at this size.
func TestEvictSegmentIsLinearNotQuadratic(t *testing.T) {
	const (
		totalIDs   = 100000
		evictedIDs = 10000
		budget     = 100 * time.Millisecond
	)

	x := newIndex()
	now := time.Now()
	// All ids on one client; the first evictedIDs are on segment 0 (the
	// one evicted below), the rest on segment 1 (left alone): the exact
	// shape security lane M2 measured as quadratic.
	for i := 0; i < totalIDs; i++ {
		id := uint64(i + 1)
		seg := int64(1)
		if i < evictedIDs {
			seg = 0
		}
		x.add(exchangeEntry{
			Summary: Summary{ID: id, ClientKey: "busy-client", Started: now, Ended: now},
			segment: seg,
		})
	}

	start := time.Now()
	x.evictSegment(0)
	elapsed := time.Since(start)
	if elapsed > budget {
		t.Fatalf("evictSegment(10k of 100k ids on one client) took %v, want < %v (was 338ms before the linear fix)", elapsed, budget)
	}

	x.mu.Lock()
	got := len(x.byClient["busy-client"].ids)
	x.mu.Unlock()
	if want := totalIDs - evictedIDs; got != want {
		t.Errorf("remaining ids for busy-client: got %d, want %d", got, want)
	}
}
