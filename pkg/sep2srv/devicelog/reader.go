package devicelog

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	sep2lfdi "github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/lfdi"
)

const (
	// readSkew is how far a line's t can trail the lines written before it
	// in the same file: a line past the park cap is written ahead of its
	// device's earlier parked lines, which follow within ParkWait (500 ms).
	// A backward scan stops only once it is this far below what it needs,
	// so such a line is still seen. A line written later than this after
	// its event time can be missed by a page that stopped early.
	readSkew = 10 * time.Second
)

var (
	// ErrNotFound means no file exists for any source of the query.
	ErrNotFound = errors.New("device log: no log file")
	// ErrBadCursor means a cursor is not one this reader issued.
	ErrBadCursor = errors.New("device log: bad cursor")
	// ErrBadQuery means the query names no source, a bad file key or a
	// non-positive limit.
	ErrBadQuery = errors.New("device log: bad query")
	// ErrNotRegular means a log path is a symlink or not a regular file; the
	// reader refuses it rather than follow it.
	ErrNotRegular = errors.New("device log: log path is not a regular file")
)

// Reader reads the files a Writer produces. It is the only reader of the
// log directory: nothing else in the server opens those files.
type Reader struct {
	dir     string
	dropped func() uint64
	// betweenOpens runs between the two opens of one source; tests use it to
	// rotate the files at that moment.
	betweenOpens func()
}

// WithDropped sets the count a Page reports as Dropped, normally the sum of
// the Writer's Dropped and Failed. It returns r.
func (r *Reader) WithDropped(f func() uint64) *Reader {
	r.dropped = f
	return r
}

// NewReader returns a Reader for dir, which must be a real directory, not a
// symlink: the directory is the boundary every file name is joined to, so it
// is checked as the files are. Pass the directory PrepareDir resolved.
func NewReader(dir string) (*Reader, error) {
	if dir == "" {
		return nil, errors.New("device log: directory is empty")
	}
	r := &Reader{dir: dir}
	if err := r.checkDir(); err != nil {
		return nil, err
	}
	return r, nil
}

// checkDir refuses a directory that is not a real directory. It runs on
// every read, so a directory swapped for a symlink after NewReader is not
// followed.
func (r *Reader) checkDir() error {
	fi, err := os.Lstat(r.dir)
	if err != nil {
		return fmt.Errorf("device log: %s: %w", r.dir, err)
	}
	if !fi.IsDir() {
		return fmt.Errorf("%w: %s is not a directory (a symlink is refused)", ErrNotRegular, r.dir)
	}
	return nil
}

// Source is one stream of lines: the file named File and its rotated
// sibling.
type Source struct {
	// File is a canonical LFDI or UnregisteredFile. Nothing else names a
	// file.
	File string
	// Actors, when non-nil, keeps only lines whose actor is in the set. The
	// shared unregistered file holds many devices' lines.
	Actors map[string]bool
	// Target, when set, keeps only lines whose target equals it: a
	// manager's lines that name one managed device.
	Target string
	// Label is reported on each line from this source ("" for a device's
	// own file).
	Label string
}

// Query selects one page of lines, newest first.
type Query struct {
	Sources []Source
	// Limit is the page size; it must be positive.
	Limit int
	// Before returns the page of lines older than the cursor; After returns
	// the oldest Limit lines newer than it, so a client that follows
	// NextAfter never skips one. At most one is set; neither returns the
	// newest page.
	Before, After string
	// Kinds keeps only these line kinds; empty keeps all.
	Kinds []string
}

// Line is one log line with its place in the merged order.
type Line struct {
	// Cursor identifies the line for Before and After. It is opaque and
	// stable across a rotation.
	Cursor string `json:"cursor"`
	// Source is the Label of the source it came from, omitted for a
	// device's own file.
	Source string `json:"source,omitempty"`
	// Line is the stored JSON object, unchanged.
	Line json.RawMessage `json:"line"`
}

// Page is the answer to a Query.
type Page struct {
	Entries []Line
	// NextBefore is the cursor of the oldest entry when older lines remain
	// in a newest or Before page, else "".
	NextBefore string
	// NextAfter is the cursor of the newest entry, else "".
	NextAfter string
	// RotatedRead reports that a rotated file was read for this page.
	RotatedRead bool
	// Dropped is the count set by WithDropped: lines the writer could not
	// record since it started, so a reader knows the log has gaps.
	Dropped uint64
}

// key orders lines: time, then file key, then the file's sequence number.
// seq is per file, so it breaks ties only inside one file; time is the
// order across files.
type key struct {
	t    int64
	file string
	seq  uint64
}

func (a key) cmp(b key) int {
	switch {
	case a.t != b.t:
		return cmpInt(a.t, b.t)
	case a.file != b.file:
		return strings.Compare(a.file, b.file)
	}
	return cmpInt(a.seq, b.seq)
}

func cmpInt[T int64 | uint64](a, b T) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

const cursorVersion = "1"

func (k key) encode() string {
	raw := cursorVersion + "." + strconv.FormatInt(k.t, 10) + "." + k.file + "." + strconv.FormatUint(k.seq, 10)
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

func decodeCursor(s string) (key, error) {
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return key{}, ErrBadCursor
	}
	parts := strings.Split(string(raw), ".")
	if len(parts) != 4 || parts[0] != cursorVersion || !validFileKey(parts[2]) {
		return key{}, ErrBadCursor
	}
	t, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		return key{}, ErrBadCursor
	}
	seq, err := strconv.ParseUint(parts[3], 10, 64)
	if err != nil {
		return key{}, ErrBadCursor
	}
	return key{t: t, file: parts[2], seq: seq}, nil
}

func validFileKey(s string) bool {
	return s == UnregisteredFile || sep2lfdi.IsCanonical(s)
}

type candidate struct {
	k      key
	source string
	raw    []byte
}

// collector keeps the best Limit lines seen so far in one merged order, so
// memory stays bounded however large the files are.
type collector struct {
	items  []candidate // newest first
	limit  int
	after  bool
	cursor key
	has    bool
}

func (c *collector) capacity() int {
	if c.after {
		return c.limit
	}
	return c.limit + 1
}

// wants reports whether a line with key k can still belong on the page.
func (c *collector) wants(k key) bool {
	if c.has {
		if c.after && k.cmp(c.cursor) <= 0 {
			return false
		}
		if !c.after && k.cmp(c.cursor) >= 0 {
			return false
		}
	}
	if len(c.items) < c.capacity() {
		return true
	}
	if c.after {
		return k.cmp(c.items[0].k) < 0
	}
	return k.cmp(c.items[len(c.items)-1].k) > 0
}

func (c *collector) offer(cand candidate) {
	i, found := slices.BinarySearchFunc(c.items, cand, func(a, b candidate) int { return b.k.cmp(a.k) })
	if found {
		return
	}
	c.items = slices.Insert(c.items, i, cand)
	if len(c.items) > c.capacity() {
		if c.after {
			c.items = c.items[1:]
		} else {
			c.items = c.items[:len(c.items)-1]
		}
	}
}

// floor is the time below which no further line can belong on the page.
func (c *collector) floor() (int64, bool) {
	if c.after {
		return c.cursor.t, c.has
	}
	if len(c.items) == c.capacity() {
		return c.items[len(c.items)-1].k.t, true
	}
	return 0, false
}

type lineHead struct {
	Seq    *uint64 `json:"seq"`
	T      string  `json:"t"`
	Kind   string  `json:"kind"`
	Actor  string  `json:"actor"`
	Target string  `json:"target"`
}

// Read returns one page of the query's merged sources, newest first. It
// stops with ctx's error when ctx ends, so a large read does not outlive its
// caller.
func (r *Reader) Read(ctx context.Context, q Query) (Page, error) {
	if err := r.checkDir(); err != nil {
		return Page{}, err
	}
	if q.Limit <= 0 || len(q.Sources) == 0 || (q.Before != "" && q.After != "") {
		return Page{}, ErrBadQuery
	}
	seen := make(map[string]bool, len(q.Sources))
	for _, s := range q.Sources {
		if !validFileKey(s.File) || seen[s.File] {
			return Page{}, fmt.Errorf("%w: source file %q", ErrBadQuery, s.File)
		}
		seen[s.File] = true
	}
	c := &collector{limit: q.Limit, after: q.After != ""}
	cur := q.Before
	if c.after {
		cur = q.After
	}
	if cur != "" {
		k, err := decodeCursor(cur)
		if err != nil {
			return Page{}, err
		}
		c.cursor, c.has = k, true
	}
	var kinds map[string]bool
	if len(q.Kinds) > 0 {
		kinds = make(map[string]bool, len(q.Kinds))
		for _, k := range q.Kinds {
			kinds[k] = true
		}
	}

	var page Page
	if r.dropped != nil {
		page.Dropped = r.dropped()
	}
	anyFile := false
	for _, s := range q.Sources {
		found, rotated, err := r.scan(ctx, s, kinds, c)
		if err != nil {
			return Page{}, err
		}
		anyFile = anyFile || found
		page.RotatedRead = page.RotatedRead || rotated
	}
	if !anyFile {
		return Page{}, ErrNotFound
	}

	items := c.items
	if !c.after && len(items) > q.Limit {
		items = items[:q.Limit]
		page.NextBefore = items[len(items)-1].k.encode()
	}
	for _, it := range items {
		page.Entries = append(page.Entries, Line{Cursor: it.k.encode(), Source: it.source, Line: it.raw})
	}
	if len(items) > 0 {
		page.NextAfter = items[0].k.encode()
	}
	return page, nil
}

// ctxCheckEvery is how many lines a scan reads between looks at its context.
const ctxCheckEvery = 256

// scan reads one source newest line first into c. found reports that the
// source holds something for the query: for a plain source either file
// exists, and for one filtered by Actors or Target a line that matches the
// filter was seen, so a shared file that exists but holds nothing for the
// device is no log for it. rotated reports that the rotated file was read.
func (r *Reader) scan(ctx context.Context, s Source, kinds map[string]bool, c *collector) (found, rotated bool, err error) {
	if err := ctx.Err(); err != nil {
		return false, false, err
	}
	curPath, oldPath := logPaths(r.dir, s.File)
	// The current file is opened first. The writer rotates by renaming the
	// current file over the rotated one and then creating a new current
	// file, so a rotation between the two opens leaves us the file that was
	// current, now found as the rotated one, and the same file is read once
	// (below). Opened the other way, that file would be lost.
	cur, curSize, err := openForRead(curPath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return false, false, err
	}
	if cur != nil {
		defer func() { _ = cur.Close() }()
	}
	if r.betweenOpens != nil {
		r.betweenOpens()
	}
	old, oldSize, err := openForRead(oldPath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return false, false, err
	}
	if old != nil {
		defer func() { _ = old.Close() }()
		if cur != nil && sameFile(cur, old) {
			_ = old.Close()
			old = nil
		}
	}
	filtered := s.Actors != nil || s.Target != ""
	found = !filtered && (cur != nil || old != nil)

	stopped := false
	var ctxErr error
	seen := 0
	visit := func(line []byte) bool {
		if seen++; seen%ctxCheckEvery == 0 {
			if ctxErr = ctx.Err(); ctxErr != nil {
				return false
			}
		}
		var h lineHead
		if json.Unmarshal(line, &h) != nil || h.Seq == nil {
			return true
		}
		ts, perr := time.Parse(time.RFC3339Nano, h.T)
		if perr != nil {
			return true
		}
		k := key{t: ts.UnixNano(), file: s.File, seq: *h.Seq}
		if floor, ok := c.floor(); ok && k.t < floor-int64(readSkew) {
			stopped = true
			return false
		}
		if s.Actors != nil && !s.Actors[h.Actor] {
			return true
		}
		if s.Target != "" && h.Target != s.Target {
			return true
		}
		found = true
		if kinds != nil && !kinds[h.Kind] {
			return true
		}
		if !c.wants(k) {
			return true
		}
		c.offer(candidate{k: k, source: s.Label, raw: bytes.Clone(line)})
		return true
	}
	if cur != nil {
		if err := eachLineBackward(cur, curSize, visit); err != nil {
			return found, false, err
		}
	}
	if old != nil && !stopped && ctxErr == nil {
		rotated = true
		if err := eachLineBackward(old, oldSize, visit); err != nil {
			return found, rotated, err
		}
	}
	return found, rotated, ctxErr
}

// sameFile reports whether a and b are the same file.
func sameFile(a, b *os.File) bool {
	fa, err := a.Stat()
	if err != nil {
		return false
	}
	fb, err := b.Stat()
	if err != nil {
		return false
	}
	return os.SameFile(fa, fb)
}

// Download is the open files of one device, ready to stream.
type Download struct {
	parts []downloadPart
}

type downloadPart struct {
	f   *os.File
	end int64
}

// OpenDownload opens the current file and then the rotated file of the
// device with canonical LFDI lfdi, for WriteTo to stream oldest first. The
// order matters for the reason given in scan, and a rotated file that is the
// current file seen again is dropped. It returns ErrNotFound when neither
// exists. The caller closes the Download.
func (r *Reader) OpenDownload(lfdi string) (*Download, error) {
	if !sep2lfdi.IsCanonical(lfdi) {
		return nil, fmt.Errorf("%w: %q", ErrBadQuery, lfdi)
	}
	if err := r.checkDir(); err != nil {
		return nil, err
	}
	curPath, oldPath := logPaths(r.dir, lfdi)
	cur, curSize, err := openForRead(curPath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if r.betweenOpens != nil {
		r.betweenOpens()
	}
	old, oldSize, err := openForRead(oldPath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		if cur != nil {
			_ = cur.Close()
		}
		return nil, err
	}
	if old != nil && cur != nil && sameFile(cur, old) {
		_ = old.Close()
		old = nil
	}
	d := &Download{}
	// Oldest first: the rotated file, then the current one.
	for _, p := range []struct {
		f    *os.File
		size int64
	}{{old, oldSize}, {cur, curSize}} {
		if p.f == nil {
			continue
		}
		end, err := completeEnd(p.f, p.size)
		if err != nil {
			_ = p.f.Close()
			_ = d.Close()
			for _, q := range []*os.File{old, cur} {
				if q != nil && q != p.f {
					_ = q.Close()
				}
			}
			return nil, err
		}
		d.parts = append(d.parts, downloadPart{f: p.f, end: end})
	}
	if len(d.parts) == 0 {
		return nil, ErrNotFound
	}
	return d, nil
}

// completeEnd is the offset just past the last newline in the first size
// bytes of f, so a line still being appended is left out.
func completeEnd(f *os.File, size int64) (int64, error) {
	if size == 0 {
		return 0, nil
	}
	n := min(size, int64(MaxLineBytes)+1)
	buf := make([]byte, n)
	if _, err := f.ReadAt(buf, size-n); err != nil {
		return 0, err
	}
	return size - n + int64(bytes.LastIndexByte(buf, '\n')) + 1, nil
}

// WriteTo streams the rotated file and then the current file to w.
func (d *Download) WriteTo(w io.Writer) (int64, error) {
	var total int64
	for _, p := range d.parts {
		n, err := io.Copy(w, io.NewSectionReader(p.f, 0, p.end))
		total += n
		if err != nil {
			return total, err
		}
	}
	return total, nil
}

// Close closes the files.
func (d *Download) Close() error {
	var first error
	for _, p := range d.parts {
		if err := p.f.Close(); err != nil && first == nil {
			first = err
		}
	}
	d.parts = nil
	return first
}
