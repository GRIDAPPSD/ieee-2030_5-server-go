package sep2capture

import (
	"fmt"
	"runtime"
	"testing"
	"time"
)

// TestIndexMemoryAtFullCap measures the index alone (Q4's INFERRED
// "tens of MB... PR 3 measures it"), not the whole Store: it drives
// index.add directly for the same 300,000-entry volume the design
// estimated from, split across 200 clients so Clients()/Exchanges()'
// per-client slices are exercised too, and reads runtime.MemStats'
// HeapAlloc before and after a GC on each side to isolate what these
// entries actually cost. The number this prints is what the PR
// description reports; there is no pass/fail line on it, only a sanity
// ceiling against a gross regression (a leak or an accidental duplicate
// allocation per entry).
func TestIndexMemoryAtFullCap(t *testing.T) {
	const (
		entries = 300000
		clients = 200
	)

	runtime.GC()
	var before runtime.MemStats
	runtime.ReadMemStats(&before)

	x := newIndex()
	now := time.Now()
	for i := 0; i < entries; i++ {
		id := uint64(i + 1)
		client := fmt.Sprintf("client-%03d", i%clients)
		x.add(exchangeEntry{
			Summary: Summary{
				ID:          id,
				ConnID:      id,
				ClientKey:   client,
				Started:     now,
				Ended:       now,
				Mark:        MarkHandled,
				HandlerRuns: 1,
				Method:      "GET",
				Path:        "/edev/0/reg",
				Status:      200,
				ReqStored:   200,
				RespStored:  180,
			},
			clientSFDI: client + "-sfdi",
			segment:    int64(i / 1000),
			offset:     int64(i % 1000 * 400),
		})
	}

	runtime.GC()
	var after runtime.MemStats
	runtime.ReadMemStats(&after)

	// Keep x alive through the measurement above so the GC calls cannot
	// collect it early and understate the delta.
	if len(x.byID) != entries {
		t.Fatalf("byID entries: got %d, want %d", len(x.byID), entries)
	}

	deltaBytes := int64(after.HeapAlloc) - int64(before.HeapAlloc)
	perEntry := float64(deltaBytes) / float64(entries)
	t.Logf("index memory for %d entries across %d clients: %d bytes total, %.1f bytes/entry (runtime.MemStats.HeapAlloc, GC'd before and after)", entries, clients, deltaBytes, perEntry)

	// A generous ceiling, set above the ~307 bytes/entry this test
	// actually measures (92 MB total for 300,000 entries): anything well
	// past that is a regression worth a fresh look, not a number this
	// test tries to pin exactly.
	const ceilingBytesPerEntry = 500
	if perEntry > ceilingBytesPerEntry {
		t.Errorf("index memory per entry: got %.1f bytes, want <= %d (sanity ceiling, not a tight bound)", perEntry, ceilingBytesPerEntry)
	}
}
