package devicelog

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"testing"
	"time"
)

// 200 unregistered certificates against a wedged store: every line must land
// in the shared file, and no device file may appear.
func TestWriterWithWedgedStoreCreatesNoDeviceFiles(t *testing.T) {
	dir := t.TempDir()
	w := newTestWriter(t, dir, Config{})
	block := make(chan struct{})
	r := NewResolver(ResolverConfig{
		Registered: func(context.Context, string) (bool, error) { <-block; return true, nil },
	})
	const actors = 200
	for i := 0; i < actors; i++ {
		actor := fmt.Sprintf("%040X", i+1)
		if err := w.AppendResolved(actor, "", r.Resolve, Entry{Kind: "request"}); err != nil {
			t.Fatal(err)
		}
	}
	closeWriter(t, w)
	names := listDir(t, dir)
	if len(names) != 1 || names[0] != UnregisteredFile+".jsonl" {
		t.Fatalf("directory holds %d files; want only the shared file: %v", len(names), names)
	}
	if lines := readLines(t, filepath.Join(dir, UnregisteredFile+".jsonl")); len(lines) != actors {
		t.Errorf("shared file has %d lines; want %d", len(lines), actors)
	}
	close(block)
	r.wg.Wait()
}

// A slow store that honours its context must drop nothing while the queue
// has room: 10 devices polling 10 lines/s for 3 s, a 64-line queue and
// 100 ms lookups. The lines wait in the park, not in the queue.
func TestWriterWithSlowStoreDropsNothing(t *testing.T) {
	dir := t.TempDir()
	w := newTestWriter(t, dir, Config{QueueSize: 64})
	r := NewResolver(ResolverConfig{
		LookupTimeout: time.Second,
		Registered: func(ctx context.Context, _ string) (bool, error) {
			select {
			case <-time.After(100 * time.Millisecond):
				return true, nil
			case <-ctx.Done():
				return false, ctx.Err()
			}
		},
	})
	const devices, rounds = 10, 30
	for n := 0; n < rounds; n++ {
		for d := 0; d < devices; d++ {
			actor := fmt.Sprintf("%040X", d+1)
			if err := w.AppendResolved(actor, "", r.Resolve, Entry{Kind: "request"}); err != nil {
				t.Fatalf("round %d device %d: %v", n, d, err)
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	closeWriter(t, w)
	if w.Dropped() != 0 {
		t.Errorf("Dropped = %d with a slow store; want 0", w.Dropped())
	}
	total := 0
	for _, name := range listDir(t, dir) {
		total += len(readLines(t, filepath.Join(dir, name)))
	}
	if total != devices*rounds {
		t.Errorf("%d lines on disk; want %d", total, devices*rounds)
	}
	// Every device was registered within the first lookup, so its own file
	// holds the lines made after that and the shared file none of them.
	if _, err := os.Stat(filepath.Join(dir, UnregisteredFile+".jsonl")); err == nil {
		t.Error("registered devices put lines in the shared file under a slow store")
	}
	r.wg.Wait()
}

// 200 unregistered certificates against a store that blocks past its context:
// one file, every line written within the park wait, and no goroutine left
// over once the store is released.
func TestParkedLinesAreWrittenByTheirDeadlineAndGoroutinesReturn(t *testing.T) {
	before := runtime.NumGoroutine()
	dir := t.TempDir()
	w := newTestWriter(t, dir, Config{ParkWait: 60 * time.Millisecond})
	block := make(chan struct{})
	r := NewResolver(ResolverConfig{
		Registered: func(context.Context, string) (bool, error) { <-block; return true, nil },
	})
	const actors = 200
	start := time.Now()
	for i := 0; i < actors; i++ {
		if err := w.AppendResolved(fmt.Sprintf("%040X", i+1), "", r.Resolve, Entry{Kind: "request"}); err != nil {
			t.Fatal(err)
		}
	}
	// The writer is still open, so only the deadline can have written them:
	// ParkWait plus a re-check, with a wide margin for a loaded machine.
	waitForLines(t, dir, actors)
	if d := time.Since(start); d > 2*time.Second {
		t.Errorf("200 parked lines took %v to be written with a 60 ms park wait", d)
	}
	names := listDir(t, dir)
	if len(names) != 1 || names[0] != UnregisteredFile+".jsonl" {
		t.Fatalf("directory holds %v; want only the shared file", names)
	}
	if w.Unsettled() != actors {
		t.Errorf("Unsettled = %d; want %d lines released by their deadline", w.Unsettled(), actors)
	}
	close(block)
	r.wg.Wait()
	closeWriter(t, w)
	for i := 0; i < 100 && runtime.NumGoroutine() > before; i++ {
		time.Sleep(10 * time.Millisecond)
	}
	if after := runtime.NumGoroutine(); after > before {
		t.Errorf("goroutines %d after release, %d before", after, before)
	}
}

// A line past ParkCap is written at once, not dropped and not held.
func TestParkOverflowWritesTheLineInsteadOfHoldingOrDroppingIt(t *testing.T) {
	dir := t.TempDir()
	w := newTestWriter(t, dir, Config{ParkCap: 3, ParkWait: time.Minute})
	pending := func(_, _ string) Resolution { return Resolution{Pending: true} }
	const lines = 10
	for i := 0; i < lines; i++ {
		if err := w.AppendResolved(fmt.Sprintf("%040X", i+1), "", pending, Entry{Kind: "request"}); err != nil {
			t.Fatal(err)
		}
	}
	waitForLines(t, dir, lines-3) // the seven past the cap, long before the one-minute wait
	if got := linesOnDisk(t, dir); got != lines-3 {
		t.Errorf("%d lines on disk before the deadline; want the %d past the cap", got, lines-3)
	}
	closeWriter(t, w) // Close releases the three still parked
	if got := linesOnDisk(t, dir); got != lines {
		t.Errorf("%d lines on disk after Close; want all %d", got, lines)
	}
	if w.Dropped() != 0 || w.Unsettled() != lines {
		t.Errorf("Dropped/Unsettled = %d/%d; want 0/%d", w.Dropped(), w.Unsettled(), lines)
	}
}

// A device's lines keep their order: a later line that resolves at once parks
// behind an earlier one that is still pending, and another device's settled
// line is not delayed by either.
func TestParkKeepsADevicesOrderAndDoesNotDelayOthers(t *testing.T) {
	dir := t.TempDir()
	w := newTestWriter(t, dir, Config{ParkWait: time.Minute})
	var gate atomic.Bool
	slow := func(_, _ string) Resolution {
		if gate.Load() {
			return Resolution{Known: true, Registered: true}
		}
		return Resolution{Pending: true}
	}
	settled := func(_, _ string) Resolution { return Resolution{Known: true, Registered: true} }
	if err := w.AppendResolved(lfdiA, "", slow, Entry{Kind: "request", Fields: map[string]any{"n": 1}}); err != nil {
		t.Fatal(err)
	}
	if err := w.AppendResolved(lfdiB, "", settled, Entry{Kind: "request", Fields: map[string]any{"n": 1}}); err != nil {
		t.Fatal(err)
	}
	if err := w.AppendResolved(lfdiA, "", settled, Entry{Kind: "request", Fields: map[string]any{"n": 2}}); err != nil {
		t.Fatal(err)
	}
	waitForLines(t, dir, 1)
	time.Sleep(50 * time.Millisecond)
	if names := listDir(t, dir); len(names) != 1 || names[0] != lfdiB+".jsonl" {
		t.Fatalf("directory holds %v; want only B's line while A's first is pending (A's second must not overtake it)", names)
	}
	gate.Store(true)
	waitForLines(t, dir, 3)
	closeWriter(t, w)
	got := readLines(t, filepath.Join(dir, lfdiA+".jsonl"))
	if len(got) != 2 || got[0].N != 1 || got[1].N != 2 || got[0].Seq != 1 || got[1].Seq != 2 {
		t.Errorf("A's lines = %+v; want n=1 then n=2 with seq 1 then 2", got)
	}
}
