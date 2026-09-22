package sep2capture

import (
	"testing"
	"time"
)

// TestIndexOutOfOrderArrivalKeepsExtremesAndOrdering: ids can reach Record
// out of id order, since two connections on one client can finish, and so
// reach Record, in either order. FirstSeen and LastSeen must track the
// earliest Started and latest Ended seen across every arrival, not the
// first and most recent to arrive, and the per-client id slice must stay
// sorted regardless of arrival order.
//
// Mutant (index.go, add): reverting to unconditional
// `c.LastSeen = e.Ended` and a FirstSeen set only when c is first created
// (the pre-fix behavior) makes this RED: FirstSeen becomes id 3's later
// start and LastSeen becomes id 2's earlier end (FirstSeen = +3s,
// LastSeen = +2.001s).
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

// TestIndexLastSeenAdvancesInOrder covers add's ordinary path: a client
// already in byClient, arriving in Started order. The out-of-order test
// above only exercises the branch that creates a new clientEntry (id 3
// first) plus one later update; it never proves the `if e.Ended.After` on
// index.go:130 has to fire on its own, so disabling that line (the mutant
// below) still leaves every existing test green.
//
// Mutant (index.go, add): changing `if e.Ended.After(c.LastSeen)` to
// `if false` makes this RED: LastSeen freezes at id 1's Ended instead of
// advancing to id 3's.
func TestIndexLastSeenAdvancesInOrder(t *testing.T) {
	base := time.Now()
	x := newIndex()
	for id := uint64(1); id <= 3; id++ {
		started := base.Add(time.Duration(id) * time.Second)
		ended := started.Add(time.Millisecond)
		x.add(exchangeEntry{
			Summary: Summary{ID: id, ClientKey: "client-1", Started: started, Ended: ended, Method: "GET", Path: "/x"},
			segment: 0,
		})
	}

	x.mu.Lock()
	c := x.byClient["client-1"]
	x.mu.Unlock()
	if c == nil {
		t.Fatal("byClient[client-1]: got nil")
	}

	wantLastSeen := base.Add(3*time.Second + time.Millisecond)
	if !c.LastSeen.Equal(wantLastSeen) {
		t.Errorf("LastSeen after three in-order arrivals: got %v, want %v (id 3's Ended)", c.LastSeen, wantLastSeen)
	}
}

// TestIndexAddRefusesDuplicateID: a second add for an id already in byID
// must not overwrite it. Overwriting byID left the old entry's memBytes
// counted in approxBytes forever (nothing ever subtracts it, since
// evictSegment looks id up by the id, and byID now points at the new
// entry) while adding the new entry's memBytes on top, so the estimate
// drifted upward with no way back down; segIDs also grew inconsistent,
// since the id was recorded against both segments' lists.
//
// Mutant (index.go, add): removing the exists check and letting
// `x.byID[e.ID] = &e` overwrite (the pre-fix behavior) makes this RED:
// byID[1] becomes the second entry's client and approxBytes is the sum of
// both entries' memBytes instead of just the first's.
func TestIndexAddRefusesDuplicateID(t *testing.T) {
	base := time.Now()
	x := newIndex()

	first := exchangeEntry{
		Summary:  Summary{ID: 1, ClientKey: "client-1", Started: base, Ended: base, Method: "GET", Path: "/x"},
		segment:  0,
		memBytes: 300,
	}
	x.add(first)

	dup := exchangeEntry{
		Summary:  Summary{ID: 1, ClientKey: "client-2", Started: base.Add(time.Second), Ended: base.Add(time.Second), Method: "POST", Path: "/y"},
		segment:  1,
		memBytes: 500,
	}
	x.add(dup)

	x.mu.Lock()
	e := x.byID[1]
	approxBytes := x.approxBytes
	duplicateIDs := x.duplicateIDs
	_, client2Present := x.byClient["client-2"]
	x.mu.Unlock()

	if e == nil || e.ClientKey != "client-1" {
		t.Fatalf("byID[1]: got %+v, want the first entry (ClientKey client-1) kept", e)
	}
	if approxBytes != first.memBytes {
		t.Errorf("approxBytes: got %d, want %d (only the first entry's memBytes, the duplicate refused)", approxBytes, first.memBytes)
	}
	if duplicateIDs != 1 {
		t.Errorf("duplicateIDs: got %d, want 1", duplicateIDs)
	}
	if client2Present {
		t.Error("byClient[client-2]: got present, want absent (the duplicate never reaches the client bookkeeping)")
	}
}

// TestIndexApproxBytesMatchesLiveEntries: after a mix of adds (entries of
// different sizes, so a wrong per-entry estimate cannot cancel out),
// evictions and a refused duplicate, approxBytes must equal the sum of the
// memBytes actually charged for every entry still in byID: no more, no
// less. The budget test's own bound only ever compares the estimate with
// itself (indexBudget +- slack), which passes even when approxBytes has
// drifted from what byID actually holds.
//
// Mutant (index.go, evictSegment): changing `x.approxBytes -= e.memBytes`
// to subtract twice makes this RED: approxBytes falls below the live sum
// by the evicted entries' own memBytes.
func TestIndexApproxBytesMatchesLiveEntries(t *testing.T) {
	x := newIndex()
	now := time.Now()

	entry := func(id uint64, seg int64, clientKey, path string) exchangeEntry {
		return exchangeEntry{
			Summary: Summary{ID: id, ClientKey: clientKey, Started: now, Ended: now, Method: "GET", Path: path},
			segment: seg,
			// Varying sizes so a fixed-size mutant (e.g. always
			// indexFixedEntryBytes) cannot pass by coincidence.
			memBytes: indexEntryBytes("GET", path, clientKey, clientKey+"-sfdi", ""),
		}
	}

	x.add(entry(1, 0, "client-1", "/a"))
	x.add(entry(2, 0, "client-2", "/bb"))
	x.add(entry(3, 1, "client-1", "/ccc"))
	x.add(entry(4, 1, "client-3", "/dddd"))
	x.add(entry(3, 2, "client-4", "/e")) // duplicate id 3: must be refused, not charged

	x.evictSegment(0) // drops ids 1 and 2

	x.add(entry(5, 2, "client-2", "/fffff"))

	x.mu.Lock()
	var wantSum int64
	for _, e := range x.byID {
		wantSum += e.memBytes
	}
	gotApprox := x.approxBytes
	liveCount := len(x.byID)
	x.mu.Unlock()

	if liveCount != 3 {
		t.Fatalf("live entries: got %d, want 3 (ids 3, 4, 5; 1 and 2 evicted, the duplicate 3 refused)", liveCount)
	}
	if gotApprox != wantSum {
		t.Errorf("approxBytes: got %d, want %d (the sum of every live entry's own memBytes)", gotApprox, wantSum)
	}
}

// TestEvictSegmentIsLinearNotQuadratic: removeSorted's per-id slice shift,
// one call per evicted id under idx.mu, made evicting many ids from one
// busy client's slice quadratic (10k of 100k ids took 338ms). This puts
// all of one segment's ids on a single client and times the eviction
// against a budget well under that.
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
	// shape that was measured as quadratic before the fix.
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
