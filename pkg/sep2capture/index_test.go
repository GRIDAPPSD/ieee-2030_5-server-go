package sep2capture

import (
	"testing"
	"time"
)

// TestIndexOutOfOrderArrivalKeepsExtremesAndOrdering is P7 (coverage-lane
// MEDIUM 3): ids can reach Record out of id order, since two connections on
// one client can finish, and so reach Record, in either order. FirstSeen
// and LastSeen must track the earliest Started and latest Ended seen across
// every arrival, not the first and most recent to arrive, and the
// per-client id slice must stay sorted regardless of arrival order.
//
// Mutant (index.go, add): reverting to unconditional
// `c.LastSeen = e.Ended` and a FirstSeen set only when c is first created
// (the pre-fix behavior) makes this RED: FirstSeen becomes id 3's later
// start and LastSeen becomes id 2's earlier end, matching what the
// security-lane probe found (FirstSeen = +3s, LastSeen = +2.001s).
func TestIndexOutOfOrderArrivalKeepsExtremesAndOrdering(t *testing.T) {
	base := time.Now()
	entryFor := func(id uint64) exchangeEntry {
		started := base.Add(time.Duration(id) * time.Second)
		ended := started.Add(time.Millisecond)
		return exchangeEntry{
			Summary: Summary{
				ID: id, ClientKey: "client-1", Started: started, Ended: ended,
				Method: "GET", Path: "/x",
			},
			segment: 0,
		}
	}

	x := newIndex()
	// Arrival order 3, 1, 2: Started increases with id (id 1 earliest,
	// id 3 latest), so arrival order is not time order.
	for _, id := range []uint64{3, 1, 2} {
		x.add(entryFor(id))
	}

	x.mu.Lock()
	c := x.byClient["client-1"]
	x.mu.Unlock()
	if c == nil {
		t.Fatal("byClient[client-1]: got nil")
	}

	wantFirstSeen := base.Add(1 * time.Second)
	wantLastSeen := base.Add(3*time.Second + time.Millisecond)
	if !c.FirstSeen.Equal(wantFirstSeen) {
		t.Errorf("FirstSeen: got %v, want %v (id 1's Started, the earliest)", c.FirstSeen, wantFirstSeen)
	}
	if !c.LastSeen.Equal(wantLastSeen) {
		t.Errorf("LastSeen: got %v, want %v (id 3's Ended, the latest)", c.LastSeen, wantLastSeen)
	}

	if got := c.ids; len(got) != 3 || got[0] != 1 || got[1] != 2 || got[2] != 3 {
		t.Errorf("ids after out-of-order arrival: got %v, want [1 2 3] (sorted despite arrival order 3,1,2)", got)
	}
}

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
