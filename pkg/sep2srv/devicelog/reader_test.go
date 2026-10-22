package devicelog

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var readerEpoch = time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)

// stamp is the t of a line written sec seconds after readerEpoch.
func stamp(sec float64) string {
	return readerEpoch.Add(time.Duration(sec * float64(time.Second))).Format(time.RFC3339Nano)
}

type fileLine struct {
	seq    uint64
	sec    float64
	kind   string
	actor  string
	target string
}

func (l fileLine) encode() string {
	kind := l.kind
	if kind == "" {
		kind = KindRequest
	}
	s := fmt.Sprintf(`{"seq":%d,"t":%q,"kind":%q,"actor":%q`, l.seq, stamp(l.sec), kind, l.actor)
	if l.target != "" {
		s += fmt.Sprintf(`,"target":%q`, l.target)
	}
	return s + `,"path":"/dcap"}`
}

func writeLogFile(t *testing.T, dir, name string, lines ...fileLine) {
	t.Helper()
	var b strings.Builder
	for _, l := range lines {
		b.WriteString(l.encode())
		b.WriteByte('\n')
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
}

func newTestReader(t *testing.T) (*Reader, string) {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	r, err := NewReader(dir)
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}
	return r, dir
}

// pageKeys names a returned line by its source file and seq, from the line
// itself, so a test checks what the API would show.
func pageKeys(t *testing.T, p Page) []string {
	t.Helper()
	var out []string
	for _, e := range p.Entries {
		var h struct {
			Seq   uint64 `json:"seq"`
			Actor string `json:"actor"`
		}
		if err := json.Unmarshal(e.Line, &h); err != nil {
			t.Fatalf("entry line %q: %v", e.Line, err)
		}
		src := e.Source
		if src == "" {
			src = "own"
		}
		out = append(out, fmt.Sprintf("%s:%s:%d", src, h.Actor[:2], h.Seq))
	}
	return out
}

// walkBefore pages through the whole query newest first and returns every
// entry key in order, failing on a repeated cursor.
func walkBefore(t *testing.T, r *Reader, q Query) []string {
	t.Helper()
	var all []string
	seen := map[string]bool{}
	for range 1000 {
		p, err := r.Read(q)
		if err != nil {
			t.Fatalf("Read: %v", err)
		}
		for _, e := range p.Entries {
			if seen[e.Cursor] {
				t.Fatalf("cursor %q returned twice", e.Cursor)
			}
			seen[e.Cursor] = true
		}
		all = append(all, pageKeys(t, p)...)
		if p.NextBefore == "" {
			return all
		}
		q.Before = p.NextBefore
	}
	t.Fatal("paging did not end")
	return nil
}

func seqRange(prefix string, from, to int) []string {
	var out []string
	for s := from; s >= to; s-- {
		out = append(out, fmt.Sprintf("%s:%d", prefix, s))
	}
	return out
}

func eq(a, b []string) bool { return strings.Join(a, ",") == strings.Join(b, ",") }

func TestReadPageSpansRotationInOrderWithNextBefore(t *testing.T) {
	r, dir := newTestReader(t)
	var old, cur []fileLine
	for s := 1; s <= 4; s++ {
		old = append(old, fileLine{seq: uint64(s), sec: float64(s) * 100, actor: lfdiA})
	}
	for s := 5; s <= 8; s++ {
		cur = append(cur, fileLine{seq: uint64(s), sec: float64(s) * 100, actor: lfdiA})
	}
	writeLogFile(t, dir, lfdiA+".1.jsonl", old...)
	writeLogFile(t, dir, lfdiA+".jsonl", cur...)

	p, err := r.Read(Query{Sources: []Source{{File: lfdiA}}, Limit: 6})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := pageKeys(t, p), []string{"own:01:8", "own:01:7", "own:01:6", "own:01:5", "own:01:4", "own:01:3"}; !eq(got, want) {
		t.Fatalf("page 1 = %v, want %v", got, want)
	}
	if !p.RotatedRead {
		t.Error("RotatedRead = false for a page that took lines from the rotated file")
	}
	k, err := decodeCursor(p.NextBefore)
	if err != nil || k.seq != 3 || k.file != lfdiA || k.t != readerEpoch.Add(300*time.Second).UnixNano() {
		t.Fatalf("NextBefore decodes to %+v (%v), want seq 3 at t+300s", k, err)
	}

	p2, err := r.Read(Query{Sources: []Source{{File: lfdiA}}, Limit: 6, Before: p.NextBefore})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := pageKeys(t, p2), []string{"own:01:2", "own:01:1"}; !eq(got, want) {
		t.Fatalf("page 2 = %v, want %v", got, want)
	}
	if p2.NextBefore != "" {
		t.Errorf("NextBefore = %q on the last page, want empty", p2.NextBefore)
	}

	// Lines are minutes apart, beyond the read skew, so a page the current
	// file satisfied stops before the rotated file. A page wholly in the current file does not report a rotated read.
	p3, err := r.Read(Query{Sources: []Source{{File: lfdiA}}, Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if p3.RotatedRead {
		t.Error("RotatedRead = true for a page the current file satisfied")
	}
}

func TestReadWalksEveryLineOnceAcrossRotationAtEveryPageSize(t *testing.T) {
	r, dir := newTestReader(t)
	var old, cur []fileLine
	for s := 1; s <= 7; s++ {
		old = append(old, fileLine{seq: uint64(s), sec: float64(s), actor: lfdiA})
	}
	for s := 8; s <= 13; s++ {
		cur = append(cur, fileLine{seq: uint64(s), sec: float64(s), actor: lfdiA})
	}
	writeLogFile(t, dir, lfdiA+".1.jsonl", old...)
	writeLogFile(t, dir, lfdiA+".jsonl", cur...)
	for limit := 1; limit <= 14; limit++ {
		got := walkBefore(t, r, Query{Sources: []Source{{File: lfdiA}}, Limit: limit})
		if want := seqRange("own:01", 13, 1); !eq(got, want) {
			t.Fatalf("limit %d: walk = %v, want %v", limit, got, want)
		}
	}
}

func TestReadMergesUnregisteredLinesByActorWithMergedCursor(t *testing.T) {
	r, dir := newTestReader(t)
	// Own file seq 1..3 at t 10, 20, 30. The shared file has A's lines at
	// t 5, 15, 25 (seq 1..3 of ITS file: seq alone repeats across files),
	// a line of B that must not appear, and a tie with the own file at t 20.
	writeLogFile(t, dir, lfdiA+".jsonl",
		fileLine{seq: 1, sec: 10, actor: lfdiA},
		fileLine{seq: 2, sec: 20, actor: lfdiA},
		fileLine{seq: 3, sec: 30, actor: lfdiA})
	writeLogFile(t, dir, UnregisteredFile+".jsonl",
		fileLine{seq: 1, sec: 5, actor: lfdiA},
		fileLine{seq: 2, sec: 7, actor: lfdiB},
		fileLine{seq: 3, sec: 15, actor: lfdiA},
		fileLine{seq: 4, sec: 20, actor: lfdiA},
		fileLine{seq: 5, sec: 25, actor: lfdiA})
	srcs := []Source{
		{File: lfdiA},
		{File: UnregisteredFile, Actors: map[string]bool{lfdiA: true}, Label: "unregistered"},
	}
	// The tie at t 20 orders by file key: the LFDI sorts before "unregistered"
	// in the key, so the shared line is the newer of the two.
	want := []string{"own:01:3", "unregistered:01:5", "unregistered:01:4", "own:01:2", "unregistered:01:3", "own:01:1", "unregistered:01:1"}
	for limit := 1; limit <= 8; limit++ {
		got := walkBefore(t, r, Query{Sources: srcs, Limit: limit})
		if !eq(got, want) {
			t.Fatalf("limit %d: walk = %v, want %v", limit, got, want)
		}
	}
	for _, k := range want {
		if strings.Contains(k, ":FE:") {
			t.Fatalf("another device's line in the view: %s", k)
		}
	}
}

func TestReadOrdersByTimeWhenAFileIsWrittenOutOfOrder(t *testing.T) {
	r, dir := newTestReader(t)
	// seq 3 was written ahead of seq 2's lookup settling: file order is
	// not time order.
	writeLogFile(t, dir, lfdiA+".jsonl",
		fileLine{seq: 1, sec: 10, actor: lfdiA},
		fileLine{seq: 2, sec: 13, actor: lfdiA},
		fileLine{seq: 3, sec: 11, actor: lfdiA},
		fileLine{seq: 4, sec: 14, actor: lfdiA})
	want := []string{"own:01:4", "own:01:2", "own:01:3", "own:01:1"}
	for limit := 1; limit <= 5; limit++ {
		got := walkBefore(t, r, Query{Sources: []Source{{File: lfdiA}}, Limit: limit})
		if !eq(got, want) {
			t.Fatalf("limit %d: walk = %v, want %v", limit, got, want)
		}
	}
}

// A backward scan stops once it is more than the skew below the oldest line
// it keeps, so a line that trails within the skew is still found: here the
// file's first line (26) is better than the 25 a stop at 22 would have kept.
func TestReadEarlyStopStillFindsALineThatTrailsWithinTheSkew(t *testing.T) {
	r, dir := newTestReader(t)
	writeLogFile(t, dir, lfdiA+".jsonl",
		fileLine{seq: 1, sec: 26, actor: lfdiA},
		fileLine{seq: 2, sec: 22, actor: lfdiA},
		fileLine{seq: 3, sec: 23, actor: lfdiA},
		fileLine{seq: 4, sec: 25, actor: lfdiA},
		fileLine{seq: 5, sec: 30, actor: lfdiA})
	p, err := r.Read(Query{Sources: []Source{{File: lfdiA}}, Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := pageKeys(t, p), []string{"own:01:5", "own:01:1"}; !eq(got, want) {
		t.Fatalf("first page = %v, want %v", got, want)
	}
}

// The same line twice (a replayed tail, or a rotated file read twice) is one
// line: the key names it.
func TestReadReturnsARepeatedLineOnce(t *testing.T) {
	r, dir := newTestReader(t)
	one := fileLine{seq: 1, sec: 1, actor: lfdiA}
	two := fileLine{seq: 2, sec: 2, actor: lfdiA}
	writeLogFile(t, dir, lfdiA+".1.jsonl", one, two)
	writeLogFile(t, dir, lfdiA+".jsonl", two, fileLine{seq: 3, sec: 3, actor: lfdiA})
	for limit := 1; limit <= 5; limit++ {
		got := walkBefore(t, r, Query{Sources: []Source{{File: lfdiA}}, Limit: limit})
		if want := seqRange("own:01", 3, 1); !eq(got, want) {
			t.Fatalf("limit %d: walk = %v, want %v", limit, got, want)
		}
	}
}

func TestReadAfterReturnsOldestNewerLinesSoFollowingSkipsNone(t *testing.T) {
	r, dir := newTestReader(t)
	var lines []fileLine
	for s := 1; s <= 10; s++ {
		lines = append(lines, fileLine{seq: uint64(s), sec: float64(s), actor: lfdiA})
	}
	writeLogFile(t, dir, lfdiA+".jsonl", lines...)
	srcs := []Source{{File: lfdiA}}

	first, err := r.Read(Query{Sources: srcs, Limit: 3})
	if err != nil {
		t.Fatal(err)
	}
	// Start from the third line, then follow NextAfter in pages of 3.
	cursor := first.Entries[len(first.Entries)-1].Cursor // seq 8
	var got []string
	for range 10 {
		p, err := r.Read(Query{Sources: srcs, Limit: 3, After: cursor})
		if err != nil {
			t.Fatal(err)
		}
		if len(p.Entries) == 0 {
			break
		}
		if p.NextBefore != "" {
			t.Errorf("NextBefore = %q on an after page, want empty", p.NextBefore)
		}
		got = append(got, pageKeys(t, p)...)
		cursor = p.NextAfter
	}
	if want := []string{"own:01:10", "own:01:9"}; !eq(got, want) {
		t.Fatalf("after seq 8 = %v, want %v", got, want)
	}

	// From the start, 3 at a time: oldest three first (newest first within
	// the page), then the next three.
	p, err := r.Read(Query{Sources: srcs, Limit: 3, After: mustCursor(t, lfdiA, 0.5, 0)})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := pageKeys(t, p), []string{"own:01:3", "own:01:2", "own:01:1"}; !eq(got, want) {
		t.Fatalf("first after page = %v, want %v", got, want)
	}
	p, err = r.Read(Query{Sources: srcs, Limit: 3, After: p.NextAfter})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := pageKeys(t, p), []string{"own:01:6", "own:01:5", "own:01:4"}; !eq(got, want) {
		t.Fatalf("second after page = %v, want %v", got, want)
	}
}

func mustCursor(t *testing.T, file string, sec float64, seq uint64) string {
	t.Helper()
	return key{t: readerEpoch.Add(time.Duration(sec * float64(time.Second))).UnixNano(), file: file, seq: seq}.encode()
}

func TestReadFiltersByKindAndTarget(t *testing.T) {
	r, dir := newTestReader(t)
	writeLogFile(t, dir, lfdiA+".jsonl",
		fileLine{seq: 1, sec: 1, actor: lfdiA, kind: KindRequest},
		fileLine{seq: 2, sec: 2, actor: lfdiA, kind: KindLogEvent},
		fileLine{seq: 3, sec: 3, actor: lfdiA, kind: KindRequest})
	// B manages A: its lines naming A are A's, its other lines are not.
	writeLogFile(t, dir, lfdiB+".jsonl",
		fileLine{seq: 1, sec: 4, actor: lfdiB, target: lfdiA},
		fileLine{seq: 2, sec: 5, actor: lfdiB},
		fileLine{seq: 3, sec: 6, actor: lfdiB, target: lfdiA, kind: KindLogEvent})

	got := walkBefore(t, r, Query{Sources: []Source{{File: lfdiA}}, Limit: 10, Kinds: []string{KindLogEvent}})
	if want := []string{"own:01:2"}; !eq(got, want) {
		t.Errorf("kind logevent = %v, want %v", got, want)
	}
	got = walkBefore(t, r, Query{
		Sources: []Source{{File: lfdiA}, {File: lfdiB, Target: lfdiA, Label: "manager"}},
		Limit:   10,
	})
	if want := []string{"manager:FE:3", "manager:FE:1", "own:01:3", "own:01:2", "own:01:1"}; !eq(got, want) {
		t.Errorf("with manager lines = %v, want %v", got, want)
	}
}

func TestReadAcrossReadChunksAndLongLines(t *testing.T) {
	r, dir := newTestReader(t)
	// More than 3 chunks of lines, so chunk edges cut lines in every
	// position, plus a line past the line cap, a torn final line and a
	// blank line.
	var b bytes.Buffer
	const n = 2500
	for s := 1; s <= n; s++ {
		l := fileLine{seq: uint64(s), sec: float64(s), actor: lfdiA}.encode()
		b.WriteString(l)
		b.WriteByte('\n')
		if s == 1200 {
			b.WriteString(`{"seq":99999,"t":"` + stamp(1200.5) + `","kind":"request","actor":"` + lfdiA + `","pad":"` + strings.Repeat("x", MaxLineBytes+10) + "\"}\n\n")
		}
	}
	b.WriteString(`{"seq":2501,"t":"` + stamp(2501) + `","kind":"requ`)
	if err := os.WriteFile(filepath.Join(dir, lfdiA+".jsonl"), b.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	if b.Len() < 3*readChunk {
		t.Fatalf("test file is %d bytes, want over 3 chunks", b.Len())
	}
	got := walkBefore(t, r, Query{Sources: []Source{{File: lfdiA}}, Limit: 400})
	if want := seqRange("own:01", n, 1); !eq(got, want) {
		t.Fatalf("walk has %d lines, want %d; first %v", len(got), len(want), got[:min(5, len(got))])
	}
}

func TestReadRefusesSymlinksAndBadInput(t *testing.T) {
	r, dir := newTestReader(t)
	outside := filepath.Join(t.TempDir(), "elsewhere.jsonl")
	if err := os.WriteFile(outside, []byte(fileLine{seq: 1, sec: 1, actor: lfdiA}.encode()+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	srcs := []Source{{File: lfdiA}}

	if err := os.Symlink(outside, filepath.Join(dir, lfdiA+".jsonl")); err != nil {
		t.Skipf("no symlinks here: %v", err)
	}
	if _, err := r.Read(Query{Sources: srcs, Limit: 5}); !errors.Is(err, ErrNotRegular) {
		t.Errorf("symlinked current file: err = %v, want ErrNotRegular", err)
	}
	if _, err := r.OpenDownload(lfdiA); !errors.Is(err, ErrNotRegular) {
		t.Errorf("symlinked current file download: err = %v, want ErrNotRegular", err)
	}
	if err := os.Remove(filepath.Join(dir, lfdiA+".jsonl")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, lfdiA+".1.jsonl")); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Read(Query{Sources: srcs, Limit: 5}); !errors.Is(err, ErrNotRegular) {
		t.Errorf("symlinked rotated file: err = %v, want ErrNotRegular", err)
	}
	// A dangling link is a link, not a missing file.
	if err := os.Remove(filepath.Join(dir, lfdiA+".1.jsonl")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(dir, "gone"), filepath.Join(dir, lfdiA+".jsonl")); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Read(Query{Sources: srcs, Limit: 5}); !errors.Is(err, ErrNotRegular) {
		t.Errorf("dangling link: err = %v, want ErrNotRegular", err)
	}

	// The directory itself may not be a link.
	link := filepath.Join(t.TempDir(), "logs")
	if err := os.Symlink(dir, link); err != nil {
		t.Fatal(err)
	}
	if _, err := NewReader(link); err == nil {
		t.Error("NewReader accepted a symlinked directory")
	}

	// Input that must not reach a path or a scan.
	for _, c := range []struct {
		name string
		q    Query
		want error
	}{
		{"traversal key", Query{Sources: []Source{{File: "../" + lfdiA}}, Limit: 5}, ErrBadQuery},
		{"lower-case LFDI", Query{Sources: []Source{{File: strings.ToLower(lfdiA)}}, Limit: 5}, ErrBadQuery},
		{"no source", Query{Limit: 5}, ErrBadQuery},
		{"limit 0", Query{Sources: srcs, Limit: 0}, ErrBadQuery},
		{"both cursors", Query{Sources: srcs, Limit: 5, Before: mustCursor(t, lfdiA, 1, 1), After: mustCursor(t, lfdiA, 1, 1)}, ErrBadQuery},
		{"duplicate source", Query{Sources: []Source{{File: lfdiA}, {File: lfdiA}}, Limit: 5}, ErrBadQuery},
		{"garbage cursor", Query{Sources: srcs, Limit: 5, Before: "!!!"}, ErrBadCursor},
		{"cursor naming another file", Query{Sources: srcs, Limit: 5, Before: key{t: 1, file: "../x", seq: 1}.encode()}, ErrBadCursor},
	} {
		if _, err := r.Read(c.q); !errors.Is(err, c.want) {
			t.Errorf("%s: err = %v, want %v", c.name, err, c.want)
		}
	}
	if _, err := r.OpenDownload("../" + lfdiA); !errors.Is(err, ErrBadQuery) {
		t.Errorf("download traversal: err = %v, want ErrBadQuery", err)
	}

	// No file at all is not found; a file with no matching line is an empty page.
	if _, err := r.Read(Query{Sources: []Source{{File: lfdiB}}, Limit: 5}); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing file: err = %v, want ErrNotFound", err)
	}
	writeLogFile(t, dir, lfdiB+".jsonl", fileLine{seq: 1, sec: 1, actor: lfdiB})
	p, err := r.Read(Query{Sources: []Source{{File: lfdiB}}, Limit: 5, Kinds: []string{KindLogEvent}})
	if err != nil || len(p.Entries) != 0 || p.NextBefore != "" {
		t.Errorf("empty match: page %+v err %v, want an empty page", p, err)
	}
}

func TestDownloadStreamsRotatedThenCurrentWithoutATornLine(t *testing.T) {
	r, dir := newTestReader(t)
	writeLogFile(t, dir, lfdiA+".1.jsonl", fileLine{seq: 1, sec: 1, actor: lfdiA}, fileLine{seq: 2, sec: 2, actor: lfdiA})
	cur := fileLine{seq: 3, sec: 3, actor: lfdiA}.encode() + "\n" + `{"seq":4,"t":"` + stamp(4) + `","ki`
	if err := os.WriteFile(filepath.Join(dir, lfdiA+".jsonl"), []byte(cur), 0o600); err != nil {
		t.Fatal(err)
	}
	d, err := r.OpenDownload(lfdiA)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = d.Close() }()
	var out bytes.Buffer
	n, err := d.WriteTo(&out)
	if err != nil || n != int64(out.Len()) {
		t.Fatalf("WriteTo = %d, %v; buffer %d", n, err, out.Len())
	}
	want := fileLine{seq: 1, sec: 1, actor: lfdiA}.encode() + "\n" + fileLine{seq: 2, sec: 2, actor: lfdiA}.encode() + "\n" + fileLine{seq: 3, sec: 3, actor: lfdiA}.encode() + "\n"
	if out.String() != want {
		t.Fatalf("download =\n%s\nwant\n%s", out.String(), want)
	}
	if _, err := r.OpenDownload(lfdiB); !errors.Is(err, ErrNotFound) {
		t.Errorf("download of a device with no file: err = %v, want ErrNotFound", err)
	}
}

func TestReadsWhatTheWriterRotated(t *testing.T) {
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	now := readerEpoch
	w := newTestWriter(t, dir, Config{MaxBytes: 1500, Now: func() time.Time { now = now.Add(time.Second); return now }})
	const total = 30
	for i := 1; i <= total; i++ {
		if err := w.Append(lfdiA, Entry{Kind: "request", Fields: map[string]any{"n": i}}); err != nil {
			t.Fatal(err)
		}
	}
	closeWriter(t, w)
	if _, err := os.Stat(filepath.Join(dir, lfdiA+".1.jsonl")); err != nil {
		t.Fatalf("writer did not rotate: %v", err)
	}
	r, err := NewReader(dir)
	if err != nil {
		t.Fatal(err)
	}
	var got []int
	q := Query{Sources: []Source{{File: lfdiA}}, Limit: 4}
	for range 100 {
		p, err := r.Read(q)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range p.Entries {
			var l struct {
				N int `json:"n"`
			}
			if err := json.Unmarshal(e.Line, &l); err != nil {
				t.Fatal(err)
			}
			got = append(got, l.N)
		}
		if p.NextBefore == "" {
			break
		}
		q.Before = p.NextBefore
	}
	// One rotated file is kept, so the oldest lines are gone; what remains
	// is a gap-free run ending at the newest line.
	if len(got) == 0 || len(got) >= total || got[0] != total {
		t.Fatalf("read %v: want a run from %d down, shorter than %d", got, total, total)
	}
	for i := 1; i < len(got); i++ {
		if got[i] != got[i-1]-1 {
			t.Fatalf("read %v: gap or repeat at index %d", got, i)
		}
	}
}
