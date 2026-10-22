package devicelog

import (
	"context"
	"fmt"
	"path/filepath"
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
// has room: 10 devices at 30 lines each, a small queue and 100 ms lookups.
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
	const devices, perDevice = 10, 30
	for n := 0; n < perDevice; n++ {
		for d := 0; d < devices; d++ {
			actor := fmt.Sprintf("%040X", d+1)
			if err := w.AppendResolved(actor, "", r.Resolve, Entry{Kind: "request"}); err != nil {
				t.Fatalf("line %d for device %d: %v", n, d, err)
			}
		}
		time.Sleep(2 * time.Millisecond)
	}
	closeWriter(t, w)
	if w.Dropped() != 0 {
		t.Errorf("Dropped = %d with a slow store; want 0", w.Dropped())
	}
	total := 0
	for _, name := range listDir(t, dir) {
		total += len(readLines(t, filepath.Join(dir, name)))
	}
	if total != devices*perDevice {
		t.Errorf("%d lines on disk; want %d", total, devices*perDevice)
	}
	r.wg.Wait()
}
