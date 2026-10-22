// Package devicelog is the one owner of the per-device log files: it appends
// JSON lines to <LFDI>.jsonl in one directory, rotates a file to
// <LFDI>.1.jsonl before it would pass the size limit, and keeps that one
// rotated file. Nothing else in the server opens files in the log directory.
package devicelog

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/logsafe"
	sep2lfdi "github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/lfdi"
)

const (
	defaultQueueSize    = 1024
	defaultMaxOpenFiles = 64

	// MaxLineBytes caps one encoded line, so the last line of a file is
	// always found in the tail window used to recover the sequence number.
	MaxLineBytes = 32 << 10
	tailBytes    = 2 * MaxLineBytes
)

var (
	// ErrInvalidLFDI means the LFDI is not 40 upper-case hex digits. The
	// check is what keeps a file name from carrying a path.
	ErrInvalidLFDI = errors.New("device log: LFDI is not 40 upper-case hex digits")
	// ErrQueueFull means the line was dropped because the writer is behind.
	ErrQueueFull = errors.New("device log: queue full, line dropped")
	// ErrLineTooLarge means the encoded line passes the line cap or the file limit.
	ErrLineTooLarge = errors.New("device log: line too large, dropped")
	// ErrClosed means Close has been called.
	ErrClosed = errors.New("device log: writer closed")
	// ErrInvalidEntry means the entry cannot be encoded as a line.
	ErrInvalidEntry = errors.New("device log: invalid entry")
)

// Config configures a Writer.
type Config struct {
	// Dir is an existing directory, normally the result of PrepareDir.
	Dir string
	// MaxBytes is the limit for one file; a write that would pass it rotates
	// first.
	MaxBytes int64
	// QueueSize bounds the hand-off channel; zero means 1024.
	QueueSize int
	// MaxOpenFiles bounds open file handles; the least recently used is
	// closed first. Zero means 64.
	MaxOpenFiles int
	// Now supplies the line timestamp; nil means time.Now.
	Now func() time.Time
	// Logger receives write failures; nil means the standard logger.
	Logger *log.Logger

	// onDequeue runs in the writer goroutine before each line is written;
	// tests use it to stall the writer.
	onDequeue func()
}

// Resolution is a Resolver's answer for one request line, taken from what is
// already known: it never waits on a store.
type Resolution struct {
	// Known reports that the actor's registration was answered. When false
	// the lookup is still running, or failed (Err), or was refused for
	// capacity.
	Known bool
	// Registered reports whether the actor has an EndDevice record or a
	// management pair. Meaningful only when Known.
	Registered bool
	// Target is the canonical LFDI of the EndDevice the path names, or ""
	// when it is not known to resolve.
	Target string
	// Err is the failure of the actor's lookup when that is what made Known
	// false. The line is then filed by the existence of the actor's own
	// file and counted.
	Err error
}

// Resolver answers AppendResolved lines in the writer goroutine. It must not
// wait on a store: NewResolver's does a cache read, and starts at most one
// background lookup per missing key.
type Resolver func(actor, edev string) Resolution

// Entry is one line to record. Kind is required. Fields are merged into the
// line; the reserved names seq, t, kind and actor are set by the writer and
// any caller value for them is ignored.
type Entry struct {
	Kind   string
	Fields map[string]any
	// At is the time the event happened; zero means when the line is encoded.
	// A caller whose line waits in the queue sets it so the line carries the
	// event's time and not the writer's.
	At time.Time
}

type item struct {
	// lfdi is the file key: a canonical LFDI or UnregisteredFile.
	lfdi string
	body []byte
	// pending is set instead of lfdi and body for a line the writer
	// goroutine still has to resolve.
	pending *pendingLine
}

type pendingLine struct {
	actor   string
	edev    string
	entry   Entry
	resolve Resolver
}

type devFile struct {
	f    *os.File
	size int64
	seq  uint64
	used uint64
}

// Writer appends lines from one goroutine. Append never blocks.
type Writer struct {
	dir      string
	max      int64
	maxOpen  int
	now      func() time.Time
	logger   *log.Logger
	stall    func()
	queue    chan item
	done     chan struct{}
	mu       sync.RWMutex
	closed   bool
	dropped  atomic.Uint64
	failed   atomic.Uint64
	unknown  atomic.Uint64
	closeErr error
	noteMu   sync.Mutex
	notes    map[string]uint64
	files    map[string]*devFile
	useClock uint64
}

// New starts a Writer. It does not create the directory; see PrepareDir.
func New(cfg Config) (*Writer, error) {
	if cfg.Dir == "" {
		return nil, errors.New("device log: directory is empty")
	}
	fi, err := os.Stat(cfg.Dir)
	if err != nil {
		return nil, fmt.Errorf("device log: %s: %w", cfg.Dir, err)
	}
	if !fi.IsDir() {
		return nil, fmt.Errorf("device log: %s is not a directory", cfg.Dir)
	}
	if cfg.MaxBytes <= 0 {
		return nil, fmt.Errorf("device log: limit %d is not positive", cfg.MaxBytes)
	}
	w := &Writer{
		dir:     cfg.Dir,
		max:     cfg.MaxBytes,
		maxOpen: cfg.MaxOpenFiles,
		now:     cfg.Now,
		logger:  cfg.Logger,
		stall:   cfg.onDequeue,
		done:    make(chan struct{}),
		files:   make(map[string]*devFile),
		notes:   make(map[string]uint64),
	}
	if w.maxOpen <= 0 {
		w.maxOpen = defaultMaxOpenFiles
	}
	if w.now == nil {
		w.now = time.Now
	}
	if w.logger == nil {
		w.logger = log.Default()
	}
	qs := cfg.QueueSize
	if qs <= 0 {
		qs = defaultQueueSize
	}
	w.queue = make(chan item, qs)
	go w.run()
	return w, nil
}

// UnregisteredFile is the file key shared by every certificate that has no
// EndDevice record and no management pair. It is not a canonical LFDI, so
// only AppendResolved can reach it and a caller-supplied LFDI never can.
const UnregisteredFile = "unregistered"

// Append queues one line for the file of the device whose certificate LFDI is
// lfdi, with no registration check. Production callers use AppendResolved,
// which is the one place that decides a line's file. Append returns at once: ErrQueueFull and ErrLineTooLarge mean the line was
// dropped and counted, and ErrInvalidLFDI means no file was named.
func (w *Writer) Append(lfdi string, e Entry) error {
	if !sep2lfdi.IsCanonical(lfdi) {
		return ErrInvalidLFDI
	}
	return w.enqueue(lfdi, lfdi, e)
}

// enqueue encodes one line for the file named by key and hands it to the
// writer goroutine. key is a canonical LFDI or UnregisteredFile, and actor a
// canonical LFDI; both are checked by the exported callers.
func (w *Writer) enqueue(key, actor string, e Entry) error {
	if e.Kind == "" {
		return fmt.Errorf("%w: kind is empty", ErrInvalidEntry)
	}
	body, err := w.encode(actor, e)
	if err != nil {
		w.dropped.Add(1)
		if errors.Is(err, ErrLineTooLarge) {
			w.note(key, "line too large, dropped", nil)
		}
		return err
	}

	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.send(item{lfdi: key, body: body}, key)
}

// send hands it to the writer goroutine without blocking, the one place a
// line is refused for a closed writer or a full queue, so both are counted
// and logged the same way for every caller. The caller holds w.mu for
// reading.
func (w *Writer) send(it item, noteKey string) error {
	if w.closed {
		w.dropped.Add(1)
		w.note(noteKey, "writer closed, line dropped", nil)
		return ErrClosed
	}
	select {
	case w.queue <- it:
		return nil
	default:
		w.dropped.Add(1)
		w.note(noteKey, "queue full, line dropped", nil)
		return ErrQueueFull
	}
}

// AppendResolved queues a request line whose file and target the writer
// goroutine decides: resolve (nil means registered, no target) reports whether actor is registered and
// which LFDI edev names, and the line goes to the actor's file or to
// UnregisteredFile, carrying "target" when one resolved. The caller does no
// store read and never waits. e.Fields must not be changed after the call.
func (w *Writer) AppendResolved(actor, edev string, resolve Resolver, e Entry) error {
	if !sep2lfdi.IsCanonical(actor) {
		return ErrInvalidLFDI
	}
	if e.Kind == "" {
		return fmt.Errorf("%w: kind is empty", ErrInvalidEntry)
	}
	if e.At.IsZero() {
		e.At = w.now()
	}
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.send(item{pending: &pendingLine{actor: actor, edev: edev, entry: e, resolve: resolve}}, actor)
}

// encode builds the JSON body of a line, less the seq the file adds.
func (w *Writer) encode(actor string, e Entry) ([]byte, error) {
	fields := make(map[string]any, len(e.Fields)+3)
	for k, v := range e.Fields {
		fields[k] = v
	}
	delete(fields, "seq")
	fields["kind"] = e.Kind
	at := e.At
	if at.IsZero() {
		at = w.now()
	}
	fields["t"] = at.UTC().Format(time.RFC3339Nano)
	fields["actor"] = actor
	body, err := json.Marshal(fields)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidEntry, err)
	}
	if int64(len(body)+seqPrefixMax) > min(int64(MaxLineBytes), w.max) {
		return nil, ErrLineTooLarge
	}
	return body, nil
}

// resolvePending asks the Resolver about p and encodes the line. The file is
// decided from the answer alone, so the writer never waits on a store and a
// store failure cannot create files:
//   - registered: the actor's own file, with the target;
//   - not registered: the shared file;
//   - not known: the actor's own file only when it already exists, else the
//     shared file, with no target.
//
// A failed lookup is counted and its first occurrence per device logged.
func (w *Writer) resolvePending(p *pendingLine) item {
	res := Resolution{Known: true, Registered: true}
	if p.resolve != nil {
		res = p.resolve(p.actor, p.edev)
	}
	e := p.entry
	if res.Target != "" && res.Known {
		fields := make(map[string]any, len(e.Fields)+1)
		for k, v := range e.Fields {
			fields[k] = v
		}
		fields["target"] = res.Target
		e.Fields = fields
	}
	key := p.actor
	switch {
	case res.Known && !res.Registered:
		key = UnregisteredFile
	case !res.Known:
		if res.Err != nil {
			w.unknown.Add(1)
			w.note(p.actor, "lookup failed, line filed by its device file's existence", res.Err)
		}
		if !w.ownFileExists(p.actor) {
			key = UnregisteredFile
		}
	}
	body, err := w.encode(p.actor, e)
	if err != nil {
		w.dropped.Add(1)
		w.note(key, "resolved line dropped", err)
		return item{}
	}
	return item{lfdi: key, body: body}
}

// ownFileExists reports whether the actor's current file is open or on disk.
func (w *Writer) ownFileExists(actor string) bool {
	if _, ok := w.files[actor]; ok {
		return true
	}
	cur, _ := w.paths(actor)
	_, err := os.Lstat(cur)
	return err == nil
}

// Dropped counts lines refused because the queue was full or the line was
// too large.
func (w *Writer) Dropped() uint64 { return w.dropped.Load() }

// Failed counts lines lost to a file error.
func (w *Writer) Failed() uint64 { return w.failed.Load() }

// Unresolved counts request lines whose registration lookup failed; each was
// filed by whether its device's own file already existed.
func (w *Writer) Unresolved() uint64 { return w.unknown.Load() }

// Close stops accepting lines, writes what is queued, and closes every file.
// It is safe to call again.
func (w *Writer) Close(ctx context.Context) error {
	w.mu.Lock()
	if !w.closed {
		w.closed = true
		close(w.queue)
	}
	w.mu.Unlock()
	select {
	case <-w.done:
		return w.closeErr
	case <-ctx.Done():
		// The channel is closed, so len is what the writer had not taken yet.
		return fmt.Errorf("%w: %d queued lines not written at the deadline", ctx.Err(), len(w.queue))
	}
}

// seqPrefixMax is the bytes the writer adds around a body: {"seq":<20
// digits>, and the trailing newline, less the body's own opening brace.
const seqPrefixMax = len(`{"seq":,`) + 20 + len("\n")

func (w *Writer) run() {
	defer close(w.done)
	for it := range w.queue {
		if w.stall != nil {
			w.stall()
		}
		w.write(it)
	}
	var errs []error
	for k, df := range w.files {
		if err := df.f.Close(); err != nil {
			errs = append(errs, fmt.Errorf("closing %s: %w", k, err))
		}
		delete(w.files, k)
	}
	w.closeErr = errors.Join(errs...)
}

func (w *Writer) write(it item) {
	if it.pending != nil {
		it = w.resolvePending(it.pending)
		if it.body == nil {
			return
		}
	}
	df, err := w.open(it.lfdi)
	if err != nil {
		w.fail(it.lfdi, err)
		return
	}
	next := df.seq + 1
	line := make([]byte, 0, len(it.body)+seqPrefixMax)
	line = append(line, `{"seq":`...)
	line = strconv.AppendUint(line, next, 10)
	line = append(line, ',')
	line = append(line, it.body[1:]...)
	line = append(line, '\n')

	if df.size+int64(len(line)) > w.max {
		if err := w.rotate(it.lfdi, df); err != nil {
			w.fail(it.lfdi, err)
			return
		}
	}
	n, err := df.f.Write(line)
	if err != nil {
		_ = df.f.Close()
		delete(w.files, it.lfdi)
		w.fail(it.lfdi, err)
		return
	}
	df.size += int64(n)
	df.seq = next
}

func (w *Writer) fail(lfdi string, err error) {
	w.failed.Add(1)
	w.note(lfdi, "write failed", err)
}

// maxNoted bounds the per-device counters behind note; devices past it share
// one counter so a flood of distinct LFDIs cannot grow the map.
const maxNoted = 1024

// note logs the first occurrence of kind for a device and every 1000th
// after it, so one failing device neither floods the log nor hides another.
func (w *Writer) note(lfdi, kind string, err error) {
	w.noteMu.Lock()
	key := lfdi + " " + kind
	if _, seen := w.notes[key]; !seen && len(w.notes) >= maxNoted {
		lfdi = "other devices"
		key = lfdi + " " + kind
	}
	w.notes[key]++
	n := w.notes[key]
	w.noteMu.Unlock()
	if n != 1 && n%1000 != 0 {
		return
	}
	if err != nil {
		w.logger.Printf("device log: %s %s (%d so far): %s", logsafe.Str(lfdi), logsafe.Str(kind), n, logsafe.Str(err.Error()))
		return
	}
	w.logger.Printf("device log: %s %s (%d so far)", logsafe.Str(lfdi), logsafe.Str(kind), n)
}

func (w *Writer) paths(lfdi string) (cur, old string) {
	return filepath.Join(w.dir, lfdi+".jsonl"), filepath.Join(w.dir, lfdi+".1.jsonl")
}

// open returns the device's open file, opening it and recovering its
// sequence number when it is not open.
func (w *Writer) open(lfdi string) (*devFile, error) {
	w.useClock++
	if df, ok := w.files[lfdi]; ok {
		df.used = w.useClock
		return df, nil
	}
	for len(w.files) >= w.maxOpen {
		w.evictOldest()
	}
	cur, old := w.paths(lfdi)
	f, err := os.OpenFile(cur, os.O_RDWR|os.O_CREATE|os.O_APPEND|noFollow, 0o640)
	if err != nil {
		return nil, err
	}
	df := &devFile{f: f, used: w.useClock}
	fi, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	df.size = fi.Size()

	seq, found, torn, err := scanSeq(f, df.size)
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	oldSeq, oldFound, err := rotatedSeq(old)
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	if df.size > 0 && !found {
		w.logger.Printf("device log: %s: no readable sequence number in %d bytes of the current file; continuing above the rotated file's (found: %v)", logsafe.Str(lfdi), df.size, oldFound)
	}
	if oldFound && oldSeq > seq {
		seq = oldSeq
	}
	df.seq = seq
	if torn {
		if _, err := f.Write([]byte{'\n'}); err != nil {
			_ = f.Close()
			return nil, err
		}
		df.size++
	}
	w.files[lfdi] = df
	if df.size > w.max {
		if err := w.rotate(lfdi, df); err != nil {
			return nil, err
		}
	}
	return df, nil
}

func (w *Writer) evictOldest() {
	var oldest string
	var oldestUse uint64
	first := true
	for k, df := range w.files {
		if first || df.used < oldestUse {
			oldest, oldestUse, first = k, df.used, false
		}
	}
	if first {
		return
	}
	if err := w.files[oldest].f.Close(); err != nil {
		w.note(oldest, "close failed", err)
	}
	delete(w.files, oldest)
}

// rotate replaces the rotated file with the current one and reopens an empty
// current file. On failure the device's handle is dropped so the next line
// reopens from disk.
func (w *Writer) rotate(lfdi string, df *devFile) error {
	cur, old := w.paths(lfdi)
	delete(w.files, lfdi)
	if err := df.f.Close(); err != nil {
		return err
	}
	if err := os.Rename(cur, old); err != nil {
		return err
	}
	f, err := os.OpenFile(cur, os.O_RDWR|os.O_CREATE|os.O_APPEND|noFollow, 0o640)
	if err != nil {
		return err
	}
	df.f, df.size = f, 0
	w.files[lfdi] = df
	return nil
}

// rotatedSeq reads the highest seq in the rotated file, which may be absent.
func rotatedSeq(path string) (seq uint64, found bool, err error) {
	f, err := os.OpenFile(path, os.O_RDONLY|noFollow, 0)
	if errors.Is(err, os.ErrNotExist) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	fi, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return 0, false, err
	}
	seq, found, _, err = scanSeq(f, fi.Size())
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return seq, found, err
}

// scanSeq walks f backward in tail-sized windows for the last line that
// parses and carries a seq, so a long unreadable tail cannot hide earlier
// lines. Each window starts MaxLineBytes inside the previous one, so a line
// cut by one window is whole in the next. torn reports a final line with no
// newline.
func scanSeq(f *os.File, size int64) (seq uint64, found, torn bool, err error) {
	if size == 0 {
		return 0, false, false, nil
	}
	end := size
	for first := true; ; first = false {
		start := max(0, end-int64(tailBytes))
		buf := make([]byte, end-start)
		if _, err := f.ReadAt(buf, start); err != nil {
			return 0, false, false, err
		}
		if first {
			torn = buf[len(buf)-1] != '\n'
		}
		rows := bytes.Split(buf, []byte{'\n'})
		for i := len(rows) - 1; i >= 0; i-- {
			if i == 0 && start > 0 {
				break
			}
			var rec struct {
				Seq *uint64 `json:"seq"`
			}
			if json.Unmarshal(rows[i], &rec) == nil && rec.Seq != nil {
				return *rec.Seq, true, torn, nil
			}
		}
		if start == 0 {
			return 0, false, torn, nil
		}
		end = start + MaxLineBytes
	}
}
