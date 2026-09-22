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

	// indexEntryBytes (index.go) is what ensureRoomFor trusts to decide
	// when the index memory budget is over; the ceiling above alone does
	// not pin it, since indexFixedEntryBytes surviving at 24 or at 2400
	// both still pass it. Band the estimate for this run's own entry shape
	// against the real heap measured above: wide enough for -race's shadow
	// memory to still pass at the real constant, tight enough that a 10x
	// miss in either direction fails.
	estimate := float64(indexEntryBytes("GET", "/edev/0/reg", "client-000", "client-000-sfdi", ""))
	const bandLow, bandHigh = 0.5, 2.0
	if ratio := estimate / perEntry; ratio < bandLow || ratio > bandHigh {
		t.Errorf("indexEntryBytes estimate: got %.1f bytes (%.2fx the measured %.1f bytes/entry), want within [%.1f, %.1f]x", estimate, ratio, perEntry, bandLow, bandHigh)
	}
}
