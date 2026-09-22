package sep2capture

import (
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"testing"
)

// deletedOpenFDs counts this process's open file descriptors whose target
// (readlink) names a deleted segment file, read from /proc/self/fd (the
// consumer's own view of what the kernel still holds open), not from any
// of the Store's counters.
func deletedOpenFDs(t testing.TB) int {
	t.Helper()
	entries, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Fatalf("os.ReadDir(/proc/self/fd): %v", err)
	}
	n := 0
	for _, e := range entries {
		target, err := os.Readlink(filepath.Join("/proc/self/fd", e.Name()))
		if err != nil {
			// The fd for this ReadDir call's own directory handle can
			// close out from under Readlink; that race is not what
			// this check is for.
			continue
		}
		if strings.Contains(target, "seg-") && strings.Contains(target, "(deleted)") {
			n++
		}
	}
	return n
}

// TestRollSegmentClosesThePreviousHandle: rollSegment must close the
// segment it is leaving before replacing s.active, or a deleted segment's
// disk space is only freed whenever the GC finalizer gets around to it. GC
// is disabled for the whole test so nothing but this fix can close a
// handle.
//
// Mutant (segment_writer.go, rollSegment): dropping the prev.f.Close()
// call makes this RED: /proc/self/fd holds "(deleted)" targets for the
// evicted segments, since nothing but a GC finalizer can ever close them
// and GC cannot run.
func TestRollSegmentClosesThePreviousHandle(t *testing.T) {
	old := debug.SetGCPercent(-1)
	defer debug.SetGCPercent(old)

	dir := t.TempDir()
	st, err := NewStore(StoreConfig{Dir: dir, CapBytes: 16 * 1024, SegmentBytes: 1024})
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	for i := 1; i <= 200; i++ {
		id := uint64(i)
		st.Record(makeExchange(id, id, "client-1", 200, 200))
	}
	waitQueueDrained(t, st)

	if got := deletedOpenFDs(t); got > 0 {
		t.Errorf("open fds on a deleted segment file: got %d, want 0", got)
	}

	closeStore(t, st)
}
