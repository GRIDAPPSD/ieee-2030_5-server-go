package devicelog

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// testClock is a clock the test moves by hand, shared by the resolver's
// cache and the writer's deadlines.
type testClock struct {
	mu sync.Mutex
	t  time.Time
}

func newTestClock() *testClock { return &testClock{t: time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)} }

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *testClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// linesOnDisk counts the lines in every file of dir.
func linesOnDisk(t *testing.T, dir string) int {
	t.Helper()
	total := 0
	for _, name := range listDir(t, dir) {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		for _, c := range b {
			if c == '\n' {
				total++
			}
		}
	}
	return total
}

// waitForLines waits until dir holds n lines, so the next poll does not start
// before the writer has placed the last one.
func waitForLines(t *testing.T, dir string, n int) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(2 * time.Millisecond) {
		if linesOnDisk(t, dir) >= n {
			return
		}
	}
	t.Fatalf("directory never reached %d lines (has %d)", n, linesOnDisk(t, dir))
}

// M1: a registered device that polls less often than the positive TTL misses
// the cache on every line. Each of its lines must still reach its own file
// with its target, and an unregistered certificate's lines must not.
func TestRarelyPollingRegisteredDeviceKeepsItsOwnFile(t *testing.T) {
	dir := t.TempDir()
	clock := newTestClock()
	w := newTestWriter(t, dir, Config{Now: clock.Now})
	r := NewResolver(ResolverConfig{
		Registered: func(_ context.Context, actor string) (bool, error) { return actor == lfdiA, nil },
		Target:     func(context.Context, string) (string, error) { return lfdiA, nil },
	})
	r.now = clock.Now

	const polls = 5
	for i := 0; i < polls; i++ {
		clock.Advance(31 * time.Second) // past the 30 s positive TTL every time
		if err := w.AppendResolved(lfdiA, "7", r.Resolve, Entry{Kind: "request", Fields: map[string]any{"edev": "7"}}); err != nil {
			t.Fatal(err)
		}
		if err := w.AppendResolved(lfdiB, "9", r.Resolve, Entry{Kind: "request"}); err != nil {
			t.Fatal(err)
		}
		waitForLines(t, dir, 2*(i+1))
	}
	closeWriter(t, w)

	own := readLines(t, filepath.Join(dir, lfdiA+".jsonl"))
	if len(own) != polls {
		t.Fatalf("device file has %d of %d lines", len(own), polls)
	}
	for i := 0; i < polls; i++ {
		if own[i].Seq != uint64(i+1) || own[i].Actor != lfdiA {
			t.Errorf("line %d seq/actor = %d/%s", i, own[i].Seq, own[i].Actor)
		}
	}
	for i, m := range readRawLines(t, filepath.Join(dir, lfdiA+".jsonl")) {
		if m["target"] != lfdiA {
			t.Errorf("line %d target = %v; want %s", i, m["target"], lfdiA)
		}
	}
	// Control: the unregistered certificate's lines are all in the shared file
	// and it made no file of its own.
	shared := readLines(t, filepath.Join(dir, UnregisteredFile+".jsonl"))
	if len(shared) != polls {
		t.Errorf("shared file has %d lines; want the unregistered certificate's %d and none of the device's", len(shared), polls)
	}
	for _, l := range shared {
		if l.Actor != lfdiB {
			t.Errorf("shared file holds a line for %s; want only %s", l.Actor, lfdiB)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, lfdiB+".jsonl")); err == nil {
		t.Error("the unregistered certificate got a file of its own")
	}
}

func readRawLines(t *testing.T, path string) []map[string]any {
	t.Helper()
	var out []map[string]any
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	start := 0
	for i, c := range b {
		if c == '\n' {
			m := map[string]any{}
			if err := json.Unmarshal(b[start:i], &m); err != nil {
				t.Fatalf("bad line: %v", err)
			}
			out = append(out, m)
			start = i + 1
		}
	}
	return out
}
