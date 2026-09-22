package sep2capture

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math/rand/v2"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"
)

// defaultCapBytes and defaultSegmentBytes are Q4's operator-approved
// defaults (2026-09-21): a 600 MB hard cap on the capture directory, in
// roughly 64 MB segments. defaultIndexMemBudgetBytes is the operator's
// 2026-09-22 follow-up: a 256 MB budget on the in-memory index, enforced
// by evicting segments early exactly as the disk cap does, so a client
// sending small exchanges (cheap on disk, costly in index entries) cannot
// push the index past this regardless of how far it is from the disk cap.
const (
	defaultCapBytes            = 600 * 1024 * 1024
	defaultSegmentBytes        = 64 * 1024 * 1024
	defaultIndexMemBudgetBytes = 256 * 1024 * 1024

	// writeQueueByteCap bounds bytes in flight between Record and the
	// writer goroutine (Q5): a stalled disk can only ever hold this many
	// bytes of backlog before Record starts dropping, never blocking.
	writeQueueByteCap = 64 * 1024 * 1024

	// writeChanCapacity is a count-based backstop behind the byte cap
	// above; at 4 MiB per direction (perDirectionCap) an exchange can be
	// up to 8 MiB, so writeQueueByteCap alone already limits flight to a
	// handful of exchanges. This just keeps the channel itself from ever
	// being the thing that decides a drop.
	writeChanCapacity = 1024
)

// subscriberBufferSize bounds how far a live SSE reader (PR 4) may fall
// behind before publish drops its update and counts it in SlowSubscribers
// rather than blocking the writer goroutine on a slow reader. A var, not a
// const, so a test can shrink it to force that drop deterministically
// without writing hundreds of exchanges to outrun a real subscriber's
// socket buffer first.
var subscriberBufferSize = 64

// ErrEvicted is returned by Exchange for an id whose segment has since
// been deleted to stay under CapBytes, or whose segment file could not be
// opened for any other reason (Q4: "a read that loses the race to
// eviction returns ErrEvicted").
var ErrEvicted = errors.New("sep2capture: exchange evicted")

// ErrNotFound is returned by Exchange for an id higher than any this Store
// has ever recorded: it was never handed to Record, as opposed to having
// been recorded and later evicted (ErrEvicted). An id below the high-water
// mark that this Store nonetheless never indexed (an exchange with no
// bytes in either direction, dropped upstream before it ever reached
// Record) cannot be told apart from an evicted one by id alone, and is
// also reported as ErrEvicted: both mean "this Store does not have it".
var ErrNotFound = errors.New("sep2capture: exchange not found")

// StoreConfig configures NewStore. Dir is required; every other field
// falls back to Q4's operator-approved default when zero.
type StoreConfig struct {
	// Dir is the segment directory. NewStore resets it (see the guarded
	// reset in reset.go) before returning, so nothing may write there
	// concurrently with NewStore.
	Dir string
	// CapBytes is the hard cap on the directory's total size. Default
	// 600 MB.
	CapBytes int64
	// SegmentBytes is the rollover size for one segment file. Default
	// 64 MB.
	SegmentBytes int64
	// IndexMemBudgetBytes is the operator's 2026-09-22 cap on the
	// in-memory index: once a new entry would cross it, segments are
	// evicted early, the same way and on the same schedule as the disk
	// cap. Default 256 MB.
	IndexMemBudgetBytes int64
	// ErrorLog receives write-error and reset diagnostics. Nil uses the
	// standard logger, matching net/http's own ErrorLog default.
	ErrorLog *log.Logger
}

// segmentFile is the writer goroutine's handle on the segment it is
// currently appending to. Only the writer goroutine ever touches one.
type segmentFile struct {
	number int64
	f      *os.File
	size   int64
}

// liveSegment is the writer goroutine's record of one segment still on
// disk, oldest first, used to decide what ensureRoomFor evicts next and to
// know how many bytes evicting it frees.
type liveSegment struct {
	number int64
	size   int64
}

// Store is the durable Sink for #611: an append-only, segmented,
// size-capped log with an in-memory index and the reader methods the HTTP
// handler (PR 4) calls. Everything about it is wiped at process start
// (the guarded reset in reset.go); the index is the only source of truth
// while the process runs, and the segment files exist so a crash can still
// be read offline before the next start deletes them.
//
// Record (Sink) never blocks: it hands off through a byte-bounded queue to
// a single writer goroutine, so a stalled disk can never stall the
// Recorder that calls it. Every other exported method may be called
// concurrently with Record and with each other.
type Store struct {
	dir            string
	capBytes       int64
	segBytes       int64
	indexMemBudget int64
	errorLog       *log.Logger

	// epoch identifies this process's incarnation of the store, drawn
	// fresh on every NewStore: nextPublishSeq always restarts at 1 on a
	// restart (the guarded reset in reset.go wipes every segment), so
	// Seq alone cannot tell a resume point left over from a previous
	// incarnation apart from a valid one in this one, and a fresh
	// Store's small Seq range can coincidentally contain an old value
	// (PR 620 review, MEDIUM: measured both a false 400 and a silent
	// skip of this run's own early events against a fresh Store). GET
	// /stream stamps every "id:" with it (handler_sse.go), and a resume
	// naming a different epoch is treated as unresolvable within this
	// incarnation rather than compared against maxPublishSeq at all.
	epoch uint64

	writeCh    chan Exchange
	writerDone chan struct{}

	intakeMu  sync.RWMutex // guards closed; RLock in Record, Lock once in Close
	closed    bool
	closeOnce sync.Once

	inFlightBytes atomic.Int64

	// Writer-goroutine-owned state: touched only inside writeLoop, so it
	// needs no lock of its own.
	active         *segmentFile
	nextSegNum     int64
	liveSegs       []liveSegment
	nextPublishSeq uint64 // Summary.Seq source; see its doc in index.go

	// maxPublishSeq mirrors nextPublishSeq for readers outside the writer
	// goroutine (handleStream's after= validation, handler_sse.go): the
	// highest Seq ever assigned, monotonic even across eviction, so a
	// resume point above it can never be a Seq this Store actually issued.
	maxPublishSeq atomic.Uint64

	idx *index

	subMu sync.Mutex
	subs  map[*subscription]struct{}

	writeErrLogMu sync.Mutex
	writeErrLogAt time.Time // zero until the first logged write error

	sseDeadlineErrLogMu sync.Mutex
	sseDeadlineErrLogAt time.Time // zero until the first logged SSE deadline error

	droppedQueueFull     atomic.Uint64
	droppedWriteError    atomic.Uint64
	abandoned            atomic.Uint64
	truncated            atomic.Uint64
	evictedSegments      atomic.Uint64
	indexMemoryEvictions atomic.Uint64
	slowSubscribers      atomic.Uint64
	totalOnDisk          atomic.Int64

	// testBeforeWrite, when set by a test in this package, runs on the
	// writer goroutine before each record's file write. Production never
	// sets it.
	testBeforeWrite func()
}

var _ Sink = (*Store)(nil)

// NewStore resets cfg.Dir (guarded: refuses a symlink or a directory
// holding anything but this package's own files, per data-invariants rule
// 3) and starts the store's one writer goroutine. Capture is off on any
// error: nothing under Dir is touched when the reset refuses.
func NewStore(cfg StoreConfig) (*Store, error) {
	if cfg.Dir == "" {
		return nil, errors.New("sep2capture: NewStore: Dir is required")
	}
	if cfg.CapBytes <= 0 {
		cfg.CapBytes = defaultCapBytes
	}
	if cfg.SegmentBytes <= 0 {
		cfg.SegmentBytes = defaultSegmentBytes
	}
	if cfg.IndexMemBudgetBytes <= 0 {
		cfg.IndexMemBudgetBytes = defaultIndexMemBudgetBytes
	}
	if cfg.ErrorLog == nil {
		cfg.ErrorLog = log.Default()
	}

	if err := resetDir(cfg.Dir); err != nil {
		return nil, err
	}

	s := &Store{
		dir:            cfg.Dir,
		capBytes:       cfg.CapBytes,
		segBytes:       cfg.SegmentBytes,
		indexMemBudget: cfg.IndexMemBudgetBytes,
		errorLog:       cfg.ErrorLog,
		epoch:          rand.Uint64(),
		writeCh:        make(chan Exchange, writeChanCapacity),
		writerDone:     make(chan struct{}),
		idx:            newIndex(),
		subs:           make(map[*subscription]struct{}),
	}
	go s.writeLoop()
	return s, nil
}

// Record implements Sink. See the Store doc: it never blocks.
func (s *Store) Record(ex Exchange) {
	size := queuedSize(ex)

	s.intakeMu.RLock()
	defer s.intakeMu.RUnlock()
	if s.closed {
		s.droppedQueueFull.Add(1)
		return
	}

	for {
		cur := s.inFlightBytes.Load()
		if cur+size > writeQueueByteCap {
			s.droppedQueueFull.Add(1)
			return
		}
		if s.inFlightBytes.CompareAndSwap(cur, cur+size) {
			break
		}
	}

	select {
	case s.writeCh <- ex:
	default:
		s.inFlightBytes.Add(-size)
		s.droppedQueueFull.Add(1)
	}
}

func queuedSize(ex Exchange) int64 {
	return int64(len(ex.Request.Bytes) + len(ex.Response.Bytes))
}

// Close stops intake, ends every open GET /stream subscription (so a
// handler blocked in handleStream's select returns immediately instead of
// holding its connection until http.Server.Shutdown's own budget runs
// out), waits for the writer goroutine to drain whatever was already
// queued, and closes the active segment, all but the subscriber close
// bounded by ctx. If ctx ends first, every record still in the queue is
// counted Abandoned rather than written, and Close returns an error naming
// ctx.Err() and how many were abandoned. It is safe to call more than
// once: the first call closes intake and ends every subscriber, and a
// later call just repeats the same bounded wait against whatever record
// backlog is left, which by then is normally nothing.
//
// Close does not itself guard against a single write syscall that never
// returns (a wedged disk or a stuck NFS mount): the writer goroutine has
// no independent path to cancel a write in progress. That mirrors
// Recorder.Close's own documented limit for a hung Sink; ordinary local
// disk I/O does not exhibit it.
func (s *Store) Close(ctx context.Context) error {
	s.closeOnce.Do(func() {
		s.intakeMu.Lock()
		s.closed = true
		s.intakeMu.Unlock()
		close(s.writeCh)
		s.closeAllSubscribers()
	})

	select {
	case <-s.writerDone:
		return nil
	case <-ctx.Done():
	}

	n := s.abandonRemaining()
	return fmt.Errorf("sep2capture: store Close: %w with %d record(s) abandoned", ctx.Err(), n)
}

// abandonRemaining drains whatever is still buffered in writeCh, counting
// each as Abandoned, without waiting for the writer goroutine to do it.
// Safe to run concurrently with writeLoop's own receive from the same
// channel: a non-blocking receive from multiple goroutines is an ordinary,
// safe use of a Go channel, and whichever goroutine wins a given item is
// the only one that processes it.
func (s *Store) abandonRemaining() int {
	n := 0
	for {
		select {
		case ex, ok := <-s.writeCh:
			if !ok {
				return n
			}
			n++
			s.inFlightBytes.Add(-queuedSize(ex))
			s.abandoned.Add(1)
		default:
			return n
		}
	}
}

// Stats reports the drop and loss counters the tab shows (Q5), plus the
// directory's own tracked size and the index's current entry count.
// EvictedSegments and IndexMemoryEvictions are disjoint and sum to every
// eviction: EvictedSegments counts evictions caused by CapBytes alone, and
// IndexMemoryEvictions counts every eviction where the index memory budget
// (the operator's 2026-09-22 decision) was also over, so summing the two
// gives the true total without double counting. IndexApproxBytes is the
// index's own indexEntryBytes estimate of its current cost: an estimate
// used to decide when to evict, not a measurement of actual heap bytes.
type Stats struct {
	DroppedQueueFull     uint64
	DroppedWriteError    uint64
	Abandoned            uint64
	Truncated            uint64
	EvictedSegments      uint64
	IndexMemoryEvictions uint64
	// SlowSubscribers counts GET /stream readers ended for falling behind
	// (publish, segment_writer.go): one per ended subscription, not one
	// per event it missed, matching the route doc (handler.go).
	SlowSubscribers  uint64
	BytesOnDisk      int64
	IndexEntries     int
	IndexApproxBytes int64
	// DuplicateIndexIDs counts exchanges Record was handed with an id
	// already present in the index: refused rather than indexed, so the
	// first entry for that id stays authoritative (index.go, add).
	DuplicateIndexIDs uint64
}

func (s *Store) Stats() Stats {
	s.idx.mu.Lock()
	entries := len(s.idx.byID)
	approxBytes := s.idx.approxBytes
	duplicateIDs := s.idx.duplicateIDs
	s.idx.mu.Unlock()
	return Stats{
		DroppedQueueFull:     s.droppedQueueFull.Load(),
		DroppedWriteError:    s.droppedWriteError.Load(),
		Abandoned:            s.abandoned.Load(),
		Truncated:            s.truncated.Load(),
		EvictedSegments:      s.evictedSegments.Load(),
		IndexMemoryEvictions: s.indexMemoryEvictions.Load(),
		SlowSubscribers:      s.slowSubscribers.Load(),
		BytesOnDisk:          s.totalOnDisk.Load(),
		IndexEntries:         entries,
		IndexApproxBytes:     approxBytes,
		DuplicateIndexIDs:    duplicateIDs,
	}
}

func (s *Store) segmentPath(num int64) string {
	return filepath.Join(s.dir, fmt.Sprintf("seg-%06d.log", num))
}
