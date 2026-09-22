package sep2capture

import (
	"context"
	"testing"
)

// TestDuplicateExchangeIDGoesNowhere is the security re-review's new LOW 10
// at f69fba3: a refused duplicate id must reach neither disk nor a
// subscriber, only the DuplicateIndexIDs counter (index.go,
// refuseIfDuplicate). Two Recorders on one Store each start their own
// atomic id counter (recorder.go), so the same id reaching writeOne twice
// is a real production shape, not a test-only one.
//
// Mutant (segment_writer.go, writeOne): dropping the refuseIfDuplicate
// early return leaves both exchanges written and published, so
// BytesOnDisk covers two records and Exchanges("second", ...) returns 1.
func TestDuplicateExchangeIDGoesNowhere(t *testing.T) {
	dir := t.TempDir()
	st, err := NewStore(StoreConfig{Dir: dir})
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { closeStore(t, st) })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sub := st.Subscribe(ctx, nil)

	st.Record(makeExchange(7, 1, "first", 25, 25))
	st.Record(makeExchange(7, 2, "second", 25, 25))
	waitQueueDrained(t, st)

	stats := st.Stats()
	if stats.DuplicateIndexIDs != 1 {
		t.Errorf("DuplicateIndexIDs: got %d, want 1", stats.DuplicateIndexIDs)
	}

	wantRecLen := int64(recordHeaderLen) + 25 + 25
	if stats.BytesOnDisk != wantRecLen {
		t.Errorf("BytesOnDisk: got %d, want %d (one record; the duplicate must never reach disk)", stats.BytesOnDisk, wantRecLen)
	}
	if got := dirSize(t, dir); got != wantRecLen {
		t.Errorf("dir size: got %d, want %d (one record on disk, the duplicate never written)", got, wantRecLen)
	}

	ex, err := st.Exchange(7)
	if err != nil {
		t.Fatalf("Exchange(7): %v", err)
	}
	if ex.ClientLFDI != "first" {
		t.Errorf("Exchange(7).ClientLFDI: got %q, want %q (first entry stays authoritative)", ex.ClientLFDI, "first")
	}

	if got := st.Exchanges("second", 0, 0); len(got) != 0 {
		t.Errorf("Exchanges(second, ...): got %d, want 0 (the duplicate never reaches the second client's bookkeeping)", len(got))
	}

	select {
	case sum, ok := <-sub:
		if !ok {
			t.Fatal("subscriber: channel closed, want the first exchange's Summary")
		}
		if sum.ClientKey != "first" {
			t.Errorf("published Summary.ClientKey: got %q, want %q", sum.ClientKey, "first")
		}
	default:
		t.Fatal("subscriber: got no Summary, want the first exchange's")
	}
	select {
	case sum, ok := <-sub:
		if ok {
			t.Errorf("subscriber: got a second Summary %+v, want none (the duplicate must never be published)", sum)
		}
	default:
		// Nothing else queued: the duplicate was never published.
	}
}
