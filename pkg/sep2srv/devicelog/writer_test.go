package devicelog

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

const (
	lfdiA = "0123456789ABCDEF0123456789ABCDEF01234567"
	lfdiB = "FEDCBA9876543210FEDCBA9876543210FEDCBA98"
)

func newTestWriter(t *testing.T, dir string, cfg Config) *Writer {
	t.Helper()
	cfg.Dir = dir
	if cfg.MaxBytes == 0 {
		cfg.MaxBytes = 1_000_000
	}
	w, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return w
}

func closeWriter(t *testing.T, w *Writer) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := w.Close(ctx); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

type line struct {
	Seq   uint64 `json:"seq"`
	T     string `json:"t"`
	Kind  string `json:"kind"`
	Actor string `json:"actor"`
	N     int    `json:"n"`
}

func readLines(t *testing.T, path string) []line {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer func() { _ = f.Close() }()
	var out []line
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1<<16), 1<<20)
	for sc.Scan() {
		var l line
		if err := json.Unmarshal(sc.Bytes(), &l); err != nil {
			t.Fatalf("%s: line %q is not JSON: %v", path, sc.Text(), err)
		}
		out = append(out, l)
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("scan %s: %v", path, err)
	}
	return out
}

func listDir(t *testing.T, dir string) []string {
	t.Helper()
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	var names []string
	for _, e := range ents {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	return names
}

func fileSize(t *testing.T, path string) int64 {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	return fi.Size()
}

func appendN(t *testing.T, w *Writer, lfdi string, from, to int) {
	t.Helper()
	for i := from; i <= to; i++ {
		for {
			err := w.Append(lfdi, Entry{Kind: "request", Fields: map[string]any{"n": i, "pad": strings.Repeat("x", 40)}})
			if err == nil {
				break
			}
			if !errors.Is(err, ErrQueueFull) {
				t.Fatalf("Append: %v", err)
			}
			time.Sleep(time.Millisecond)
		}
	}
}

func TestRotationAtLimit(t *testing.T) {
	dir := t.TempDir()
	const limit = 2000
	w := newTestWriter(t, dir, Config{MaxBytes: limit})
	appendN(t, w, lfdiA, 1, 100)
	closeWriter(t, w)

	cur := filepath.Join(dir, lfdiA+".jsonl")
	old := filepath.Join(dir, lfdiA+".1.jsonl")
	if got := listDir(t, dir); len(got) != 2 {
		t.Fatalf("directory = %v; want exactly the current and one rotated file", got)
	}
	for _, p := range []string{cur, old} {
		if sz := fileSize(t, p); sz > limit {
			t.Errorf("%s is %d bytes, over the %d limit", p, sz, limit)
		}
	}
	oldLines, curLines := readLines(t, old), readLines(t, cur)
	if len(oldLines) == 0 || len(curLines) == 0 {
		t.Fatalf("rotated=%d current=%d lines; both must hold lines", len(oldLines), len(curLines))
	}
	if oldLines[len(oldLines)-1].Seq+1 != curLines[0].Seq {
		t.Errorf("seq jumps across rotation: rotated ends %d, current starts %d", oldLines[len(oldLines)-1].Seq, curLines[0].Seq)
	}
	last := curLines[len(curLines)-1]
	if last.Seq != 100 || last.N != 100 {
		t.Errorf("last line seq=%d n=%d; want 100 and 100 (seq counts every line, not the surviving ones)", last.Seq, last.N)
	}
	for i := 1; i < len(curLines); i++ {
		if curLines[i].Seq != curLines[i-1].Seq+1 {
			t.Fatalf("current file seq not contiguous at %d: %d after %d", i, curLines[i].Seq, curLines[i-1].Seq)
		}
	}
}

func TestLineFields(t *testing.T) {
	dir := t.TempDir()
	at := time.Date(2026, 10, 21, 12, 30, 0, 123456789, time.UTC)
	w := newTestWriter(t, dir, Config{Now: func() time.Time { return at }})
	// Caller fields must not forge the reserved ones.
	if err := w.Append(lfdiA, Entry{Kind: "request", Fields: map[string]any{"seq": 999, "actor": "EVIL", "t": "x", "kind": "evil", "status": 200}}); err != nil {
		t.Fatal(err)
	}
	closeWriter(t, w)
	raw, err := os.ReadFile(filepath.Join(dir, lfdiA+".jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("not JSON: %v: %s", err, raw)
	}
	want := map[string]any{"seq": float64(1), "t": "2026-10-21T12:30:00.123456789Z", "kind": "request", "actor": lfdiA, "status": float64(200)}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("field %s = %v; want %v", k, got[k], v)
		}
	}
	if len(got) != len(want) {
		t.Errorf("line has %d fields %v; want %d", len(got), got, len(want))
	}
	if !strings.HasSuffix(string(raw), "\n") || strings.Count(string(raw), "\n") != 1 {
		t.Errorf("line is not exactly one newline-terminated record: %q", raw)
	}
}

func TestSeqResumesAfterReopen(t *testing.T) {
	dir := t.TempDir()
	w := newTestWriter(t, dir, Config{})
	appendN(t, w, lfdiA, 1, 5)
	closeWriter(t, w)

	w = newTestWriter(t, dir, Config{})
	appendN(t, w, lfdiA, 6, 8)
	closeWriter(t, w)

	lines := readLines(t, filepath.Join(dir, lfdiA+".jsonl"))
	if len(lines) != 8 {
		t.Fatalf("%d lines; want 8", len(lines))
	}
	for i, l := range lines {
		if l.Seq != uint64(i+1) || l.N != i+1 {
			t.Errorf("line %d: seq=%d n=%d; want %d", i, l.Seq, l.N, i+1)
		}
	}
}

func TestSeqResumesFromRotatedFileWhenCurrentIsEmpty(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, lfdiA+".1.jsonl"), []byte(`{"seq":6,"kind":"request"}`+"\n"+`{"seq":7,"kind":"request"}`+"\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	w := newTestWriter(t, dir, Config{})
	appendN(t, w, lfdiA, 1, 1)
	closeWriter(t, w)
	lines := readLines(t, filepath.Join(dir, lfdiA+".jsonl"))
	if len(lines) != 1 || lines[0].Seq != 8 {
		t.Fatalf("lines = %+v; want one line with seq 8", lines)
	}
}

func TestTornLastLineDoesNotMergeWithNextLine(t *testing.T) {
	dir := t.TempDir()
	cur := filepath.Join(dir, lfdiA+".jsonl")
	if err := os.WriteFile(cur, []byte(`{"seq":1,"kind":"request"}`+"\n"+`{"seq":2,"kind":"requ`), 0o640); err != nil {
		t.Fatal(err)
	}
	w := newTestWriter(t, dir, Config{})
	appendN(t, w, lfdiA, 1, 1)
	closeWriter(t, w)
	raw, err := os.ReadFile(cur)
	if err != nil {
		t.Fatal(err)
	}
	rows := strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n")
	if len(rows) != 3 {
		t.Fatalf("rows = %q; want the two old rows and one new", rows)
	}
	var l line
	if err := json.Unmarshal([]byte(rows[2]), &l); err != nil || l.Seq != 2 {
		t.Fatalf("new row %q: seq=%d err=%v; want seq 2 (resumes from the last complete line)", rows[2], l.Seq, err)
	}
}

func TestFileOverLoweredLimitRotatesAtOpen(t *testing.T) {
	dir := t.TempDir()
	cur := filepath.Join(dir, lfdiA+".jsonl")
	big := strings.Repeat(`{"seq":3,"kind":"request","pad":"`+strings.Repeat("y", 60)+`"}`+"\n", 50)
	if err := os.WriteFile(cur, []byte(big), 0o640); err != nil {
		t.Fatal(err)
	}
	const limit = 1000
	w := newTestWriter(t, dir, Config{MaxBytes: limit})
	appendN(t, w, lfdiA, 1, 1)
	closeWriter(t, w)
	if got := fileSize(t, cur); got > limit {
		t.Errorf("current file is %d bytes after open, over the %d limit", got, limit)
	}
	if got := fileSize(t, filepath.Join(dir, lfdiA+".1.jsonl")); got != int64(len(big)) {
		t.Errorf("rotated file is %d bytes; want the old %d", got, len(big))
	}
}

func TestNonCanonicalLFDINeverNamesAFile(t *testing.T) {
	dir := t.TempDir()
	w := newTestWriter(t, dir, Config{})
	bad := []string{
		"",
		strings.ToLower(lfdiA),
		lfdiA[:39],
		lfdiA + "0",
		" " + lfdiA[1:],
		lfdiA + "\n",
		"../" + lfdiA[3:],
		strings.Repeat("G", 40),
		lfdiA[:20] + "/" + lfdiA[21:],
		lfdiA[:20] + "\x00" + lfdiA[21:],
		"unregistered",
		lfdiA + ".1",
	}
	for _, in := range bad {
		if err := w.Append(in, Entry{Kind: "request"}); !errors.Is(err, ErrInvalidLFDI) {
			t.Errorf("Append(%q) = %v; want ErrInvalidLFDI", in, err)
		}
	}
	closeWriter(t, w)
	if got := listDir(t, dir); len(got) != 0 {
		t.Errorf("directory holds %v after only invalid LFDIs; want nothing", got)
	}
	if parent := listDir(t, filepath.Dir(dir)); len(parent) != 1 {
		t.Errorf("parent of the log directory holds %v; a traversal LFDI escaped", parent)
	}
}

func TestAppendNeverBlocksAndCountsDrops(t *testing.T) {
	dir := t.TempDir()
	release := make(chan struct{})
	started := make(chan struct{}, 1)
	w := newTestWriter(t, dir, Config{QueueSize: 2, onDequeue: func() {
		select {
		case started <- struct{}{}:
		default:
		}
		<-release
	}})
	if err := w.Append(lfdiA, Entry{Kind: "request"}); err != nil {
		t.Fatal(err)
	}
	<-started // the goroutine holds entry 1; the queue is empty and the consumer is stalled

	done := make(chan []error)
	go func() {
		var errs []error
		for i := 0; i < 5; i++ {
			errs = append(errs, w.Append(lfdiA, Entry{Kind: "request"}))
		}
		done <- errs
	}()
	var errs []error
	select {
	case errs = <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Append blocked while the writer goroutine was stalled")
	}
	var queued, full int
	for _, err := range errs {
		switch {
		case err == nil:
			queued++
		case errors.Is(err, ErrQueueFull):
			full++
		default:
			t.Fatalf("unexpected error %v", err)
		}
	}
	if queued != 2 || full != 3 {
		t.Errorf("queued=%d full=%d; want 2 and 3 for a queue of 2", queued, full)
	}
	if got := w.Dropped(); got != 3 {
		t.Errorf("Dropped() = %d; want 3", got)
	}
	close(release)
	closeWriter(t, w)
	lines := readLines(t, filepath.Join(dir, lfdiA+".jsonl"))
	if len(lines) != 3 {
		t.Fatalf("%d lines on disk; want the 1 in flight plus 2 queued", len(lines))
	}
	for i, l := range lines {
		if l.Seq != uint64(i+1) {
			t.Errorf("line %d has seq %d; dropped lines must not consume a seq", i, l.Seq)
		}
	}
}

func TestOversizeLineIsDroppedNotWritten(t *testing.T) {
	dir := t.TempDir()
	const limit = 1000
	w := newTestWriter(t, dir, Config{MaxBytes: limit})
	if err := w.Append(lfdiA, Entry{Kind: "request", Fields: map[string]any{"pad": strings.Repeat("z", limit)}}); !errors.Is(err, ErrLineTooLarge) {
		t.Fatalf("Append(oversize) = %v; want ErrLineTooLarge", err)
	}
	appendN(t, w, lfdiA, 1, 1)
	closeWriter(t, w)
	if got := w.Dropped(); got != 1 {
		t.Errorf("Dropped() = %d; want 1", got)
	}
	lines := readLines(t, filepath.Join(dir, lfdiA+".jsonl"))
	if len(lines) != 1 || lines[0].Seq != 1 {
		t.Errorf("lines = %+v; want only the small line, seq 1", lines)
	}
}

func TestOpenFileCapKeepsSeqPerDevice(t *testing.T) {
	dir := t.TempDir()
	w := newTestWriter(t, dir, Config{MaxOpenFiles: 1})
	for i := 1; i <= 6; i++ {
		appendN(t, w, lfdiA, i, i)
		appendN(t, w, lfdiB, i, i)
	}
	closeWriter(t, w)
	for _, id := range []string{lfdiA, lfdiB} {
		lines := readLines(t, filepath.Join(dir, id+".jsonl"))
		if len(lines) != 6 {
			t.Fatalf("%s: %d lines; want 6", id, len(lines))
		}
		for i, l := range lines {
			if l.Seq != uint64(i+1) || l.Actor != id {
				t.Errorf("%s line %d: seq=%d actor=%s", id, i, l.Seq, l.Actor)
			}
		}
	}
}

func TestAppendAfterCloseIsRefused(t *testing.T) {
	w := newTestWriter(t, t.TempDir(), Config{})
	closeWriter(t, w)
	if err := w.Append(lfdiA, Entry{Kind: "request"}); !errors.Is(err, ErrClosed) {
		t.Errorf("Append after Close = %v; want ErrClosed", err)
	}
	closeWriter(t, w)
}

func TestNewRefusesBadConfig(t *testing.T) {
	file := filepath.Join(t.TempDir(), "afile")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	cases := map[string]Config{
		"empty dir":      {MaxBytes: 1000},
		"missing dir":    {Dir: filepath.Join(t.TempDir(), "nope"), MaxBytes: 1000},
		"dir is file":    {Dir: file, MaxBytes: 1000},
		"zero limit":     {Dir: t.TempDir()},
		"negative limit": {Dir: t.TempDir(), MaxBytes: -1},
	}
	for name, cfg := range cases {
		if _, err := New(cfg); err == nil {
			t.Errorf("%s: New succeeded; want a refusal", name)
		}
	}
}

func TestSymlinkedDeviceFileIsNotFollowed(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(t.TempDir(), "ca.key")
	if err := os.WriteFile(target, []byte("KEY"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(dir, lfdiA+".jsonl")); err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	w := newTestWriter(t, dir, Config{Logger: log.New(&logs, "", 0)})
	if err := w.Append(lfdiA, Entry{Kind: "request"}); err != nil {
		t.Fatal(err)
	}
	closeWriter(t, w)
	got, err := os.ReadFile(target)
	if err != nil || string(got) != "KEY" {
		t.Errorf("symlink target = %q, %v; want it untouched", got, err)
	}
	if w.Failed() != 1 {
		t.Errorf("Failed() = %d; want 1 for the refused open", w.Failed())
	}
	if !strings.Contains(logs.String(), lfdiA) {
		t.Errorf("log %q does not name the device whose open was refused", logs.String())
	}
}

func TestSymlinkedRotatedFileIsNotReadForSeq(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(target, []byte(`{"seq":99}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(dir, lfdiA+".1.jsonl")); err != nil {
		t.Fatal(err)
	}
	w := newTestWriter(t, dir, Config{Logger: log.New(io.Discard, "", 0)})
	appendN(t, w, lfdiA, 1, 1)
	closeWriter(t, w)
	if w.Failed() != 1 {
		t.Errorf("Failed() = %d; want the symlinked rotated file refused", w.Failed())
	}
}

func lastRow(t *testing.T, path string) line {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	rows := bytes.Split(bytes.TrimSuffix(raw, []byte("\n")), []byte("\n"))
	var l line
	if err := json.Unmarshal(rows[len(rows)-1], &l); err != nil {
		t.Fatalf("last row %q: %v", rows[len(rows)-1], err)
	}
	return l
}

func TestSeqNeverGoesBackwardWhenTailIsUnreadable(t *testing.T) {
	good := `{"seq":1,"kind":"request"}` + "\n" + `{"seq":2,"kind":"request"}` + "\n" + `{"seq":3,"kind":"request"}` + "\n"
	cases := []struct {
		name, cur, old string
		want           uint64
		wantLog        bool
	}{
		{"valid lines before a long unreadable tail", good + strings.Repeat("\x00", 70000), "", 4, false},
		{"nothing parseable, rotated file known", strings.Repeat("\x00", 70000), `{"seq":1}` + "\n" + `{"seq":2}` + "\n", 3, true},
		{"rotated file higher than a damaged current", `{"seq":1,"kind":"request"}` + "\n" + strings.Repeat("\x00", 70000), `{"seq":7}` + "\n", 8, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			cur := filepath.Join(dir, lfdiA+".jsonl")
			if err := os.WriteFile(cur, []byte(c.cur), 0o640); err != nil {
				t.Fatal(err)
			}
			if c.old != "" {
				if err := os.WriteFile(filepath.Join(dir, lfdiA+".1.jsonl"), []byte(c.old), 0o640); err != nil {
					t.Fatal(err)
				}
			}
			var logs bytes.Buffer
			w := newTestWriter(t, dir, Config{Logger: log.New(&logs, "", 0)})
			appendN(t, w, lfdiA, 1, 1)
			closeWriter(t, w)
			if got := lastRow(t, cur).Seq; got != c.want {
				t.Errorf("seq = %d; want %d", got, c.want)
			}
			if c.wantLog && !strings.Contains(logs.String(), lfdiA) {
				t.Errorf("log %q does not report the unreadable file", logs.String())
			}
		})
	}
}

func TestDropsAreLoggedPerDevice(t *testing.T) {
	dir := t.TempDir()
	release := make(chan struct{})
	started := make(chan struct{}, 1)
	var logs bytes.Buffer
	w := newTestWriter(t, dir, Config{QueueSize: 1, MaxBytes: 1000, Logger: log.New(&logs, "", 0), onDequeue: func() {
		select {
		case started <- struct{}{}:
		default:
		}
		<-release
	}})
	_ = w.Append(lfdiA, Entry{Kind: "request"})
	<-started
	_ = w.Append(lfdiA, Entry{Kind: "request"}) // fills the queue
	_ = w.Append(lfdiA, Entry{Kind: "request"}) // dropped
	_ = w.Append(lfdiB, Entry{Kind: "request"}) // dropped, other device
	_ = w.Append(lfdiB, Entry{Kind: "request", Fields: map[string]any{"pad": strings.Repeat("z", 1000)}})
	close(release)
	closeWriter(t, w)
	out := logs.String()
	for _, want := range []string{lfdiA, lfdiB, "queue full", "too large"} {
		if !strings.Contains(out, want) {
			t.Errorf("log %q lacks %q", out, want)
		}
	}
	if n := strings.Count(out, lfdiA+" queue full"); n != 1 {
		t.Errorf("device A's queue-full drop logged %d times; want once (rate limited)", n)
	}
}
