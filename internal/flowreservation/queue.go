package flowreservation

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math"
	"sync"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/handlers/sep2time"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
)

// FRQReader is the subset of store.ScopedStore[sep2.FlowReservationRequest]
// Queue needs: it only ever reads a request back to answer it, never writes
// one (POST /edev/{id}/frq owns that write).
type FRQReader interface {
	Get(ctx context.Context, parentID, id string) (sep2.FlowReservationRequest, error)
}

// FRPStore is the subset of store.ScopedStore[sep2.FlowReservationResponse]
// Queue needs: Get is the exactly-once check (a response already exists
// under this request's own id), Create stores the built answer.
type FRPStore interface {
	Get(ctx context.Context, parentID, id string) (sep2.FlowReservationResponse, error)
	Create(ctx context.Context, parentID, id string, resource sep2.FlowReservationResponse) error
}

// timer is the *time.Timer subset Queue uses, so a test can substitute a
// fake without waiting on real time.
type timer interface {
	Stop() bool
}

// ErrQueueClosed is Submit's outcome after Close: the queue schedules no
// more fallbacks once shut down.
var ErrQueueClosed = errors.New("flowreservation: queue is closed")

// Queue holds every FlowReservationRequest awaiting an answer and, for each,
// fires the deadline fallback (#666 D1/D2) if the operator has not answered
// by then: grant as asked when the fleet's window is free, deny otherwise.
//
// The zero value is not usable: construct with NewQueue.
type Queue struct {
	frq     FRQReader
	frp     FRPStore
	checker CommitmentChecker
	cfg     Config
	pen     *uint32

	// after schedules f to run after d and returns a stoppable handle;
	// production uses time.AfterFunc, tests substitute a short-deadline or
	// synchronous stand-in so no test waits out a real 300 s bound or the
	// retry backoff. It is used for both the main deadline and a fallback
	// retry's backoff, so a test can assert either through one seam.
	after func(d time.Duration, f func()) timer

	// mu guards closed, timers and keyLocks. It is never held across the
	// store I/O or the network-shaped work in build: keyLocks[key] is what
	// serializes concurrent Submit/Answer/fallback calls for one request,
	// so two callers racing to answer the same request cannot both win.
	// There is no "answered" map: whether a request already has a response
	// is answered by frp.Get under frqID (see build), which stays correct
	// however long the process runs, rather than growing one entry per
	// request answered for the process lifetime.
	mu       sync.Mutex
	closed   bool
	timers   map[string]timer
	keyLocks map[string]*sync.Mutex
}

// NewQueue builds a Queue. checker nil takes PermissiveCommitmentChecker
// (the default until #714 lands the real commitment ledger). cfg's zero
// fields take the package defaults. pen is threaded straight to
// newFRPMRID, same meaning as RouterConfig.PEN.
func NewQueue(frq FRQReader, frp FRPStore, checker CommitmentChecker, cfg Config, pen *uint32) *Queue {
	if checker == nil {
		checker = PermissiveCommitmentChecker{}
	}
	return &Queue{
		frq:      frq,
		frp:      frp,
		checker:  checker,
		cfg:      cfg.withDefaults(),
		pen:      pen,
		after:    defaultAfter,
		timers:   make(map[string]timer),
		keyLocks: make(map[string]*sync.Mutex),
	}
}

func defaultAfter(d time.Duration, f func()) timer {
	return time.AfterFunc(d, f)
}

func queueKey(edevID, frqID string) string {
	return edevID + "/" + frqID
}

// Submit enqueues frq, already stored under (edevID, frqID), for the
// deadline fallback. The caller (POST /edev/{id}/frq) creates no response
// itself: this is the only thing it does after the store write, so the
// request's answer waits for Answer or the fallback, per #666's first
// criterion.
//
// createdAt is the Unix-second instant frq was stored, used to cap the
// fallback delay at the requested start (D1: "never later than the
// requested start"). Submit after Close does nothing: it logs and returns,
// since the handler's Submitter interface has no error return for it to
// carry ErrQueueClosed through.
func (q *Queue) Submit(edevID, frqID string, frq sep2.FlowReservationRequest, createdAt int64) {
	key := queueKey(edevID, frqID)
	delay := q.deadlineDelay(frq, createdAt)

	q.mu.Lock()
	defer q.mu.Unlock()
	if q.closed {
		log.Printf("flowreservation: Submit %s/%s after Close: %v", edevID, frqID, ErrQueueClosed)
		return
	}
	q.timers[key] = q.after(delay, func() {
		q.attemptFallback(context.Background(), edevID, frqID, 1)
	})
}

// deadlineDelay is the configured bound, capped so the fallback never fires
// later than the requested start. A request with no IntervalRequested has
// no start to cap against, so it gets the configured bound unmodified.
func (q *Queue) deadlineDelay(frq sep2.FlowReservationRequest, createdAt int64) time.Duration {
	delay := q.cfg.Deadline
	if frq.IntervalRequested == nil {
		return delay
	}
	secondsUntilStart := frq.IntervalRequested.Start - createdAt
	if secondsUntilStart < 0 {
		secondsUntilStart = 0
	}
	untilStart := secondsToDuration(secondsUntilStart)
	if untilStart < delay {
		return untilStart
	}
	return delay
}

// secondsToDuration converts a count of seconds to a time.Duration,
// saturating at the largest representable Duration instead of overflowing.
// time.Duration(seconds)*time.Second wraps for any seconds count past
// about 292 years (math.MaxInt64 nanoseconds / 1e9): a client-supplied
// IntervalRequested.Start far enough in the future turned that wrap into a
// NEGATIVE delay, which deadlineDelay's own <0 guard then clamped to zero,
// granting the request at once instead of holding it for the deadline.
// Saturating keeps the result larger than any configured Deadline, so
// deadlineDelay's own min() still picks Deadline, never the (wrong) instant
// answer.
func secondsToDuration(seconds int64) time.Duration {
	if seconds < 0 {
		return 0
	}
	const maxSeconds = int64(math.MaxInt64) / int64(time.Second)
	if seconds > maxSeconds {
		return time.Duration(math.MaxInt64)
	}
	return time.Duration(seconds) * time.Second
}

// attemptFallback is the deadline path: D2, grant as asked when the fleet's
// window is uncommitted, deny otherwise; a Cancelled request (10.9.3.1) is
// denied without consulting the commitment checker at all, since a
// withdrawn request is never granted regardless of fleet state. A
// commitment check that cannot complete denies rather than grants (fail
// closed): an indeterminate answer must not be read as "free capacity".
//
// attempt is 1 on the timer's own fire; retryFallback re-invokes this with
// attempt+1 after a backoff, so every attempt re-reads the request fresh
// rather than reusing a stale copy.
func (q *Queue) attemptFallback(ctx context.Context, edevID, frqID string, attempt int) {
	frq, err := q.frq.Get(ctx, edevID, frqID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			// The request itself is gone (for example, its EndDevice was
			// deleted and the delete cascaded it away, #701): there is
			// nothing left to answer, and retrying would only repeat this
			// same NotFound forever.
			log.Printf("flowreservation: deadline fallback: %s/%s no longer exists, not answering: %v", edevID, frqID, err)
			q.forgetTimer(edevID, frqID)
			return
		}
		q.retryOrGiveUp(ctx, edevID, frqID, attempt, fmt.Errorf("get request: %w", err))
		return
	}

	decision := Decision{Kind: Grant}
	switch {
	case frq.RequestStatus.RequestStatus == sep2.RequestStatusCancelled:
		decision = Decision{Kind: Deny}
	case frq.IntervalRequested != nil && frq.IntervalRequested.Duration == 0:
		// A zero-duration request can never be granted as asked (10.9.3.2
		// reserves that shape for a denial; answerFor refuses it with
		// ErrGrantZeroDuration), so this is decided as a denial up front
		// rather than tried as a Grant and treated as a retryable failure
		// when answerFor correctly refuses it.
		decision = Decision{Kind: Deny}
	default:
		committed, err := q.checker.Committed(ctx, edevID, requestedStart(frq), requestedDuration(frq))
		switch {
		case err != nil:
			log.Printf("flowreservation: deadline fallback: commitment check %s/%s: %v; denying (fail closed)", edevID, frqID, err)
			decision = Decision{Kind: Deny}
		case committed:
			decision = Decision{Kind: Deny}
		}
	}

	if _, err := q.build(ctx, edevID, frqID, decision); err != nil {
		if errors.Is(err, ErrAlreadyAnswered) {
			return
		}
		if errors.Is(err, store.ErrNotFound) {
			log.Printf("flowreservation: deadline fallback: %s/%s no longer exists, not answering: %v", edevID, frqID, err)
			q.forgetTimer(edevID, frqID)
			return
		}
		q.retryOrGiveUp(ctx, edevID, frqID, attempt, fmt.Errorf("answer: %w", err))
	}
}

// forgetTimer deletes key's timers entry with no replacement: used wherever
// attemptFallback decides it will never schedule another attempt for this
// request, so a request nobody will retry does not sit in timers forever.
func (q *Queue) forgetTimer(edevID, frqID string) {
	q.mu.Lock()
	delete(q.timers, queueKey(edevID, frqID))
	q.mu.Unlock()
}

// retryOrGiveUp logs cause and, while attempts remain under
// Config.RetryAttempts, reschedules attemptFallback after
// Config.RetryBackoff through the same after seam Submit uses. Once
// attempts are exhausted it logs, forgets the timer entry and stops: the
// request is left unanswered, which is the one case #736's error-handling
// review asked to be bounded rather than silent.
func (q *Queue) retryOrGiveUp(ctx context.Context, edevID, frqID string, attempt int, cause error) {
	if attempt >= q.cfg.RetryAttempts {
		log.Printf("flowreservation: deadline fallback: %s/%s: giving up after %d attempt(s): %v", edevID, frqID, attempt, cause)
		q.forgetTimer(edevID, frqID)
		return
	}
	log.Printf("flowreservation: deadline fallback: %s/%s: attempt %d failed, retrying in %s: %v", edevID, frqID, attempt, q.cfg.RetryBackoff, cause)

	key := queueKey(edevID, frqID)
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.closed {
		return
	}
	q.timers[key] = q.after(q.cfg.RetryBackoff, func() {
		q.attemptFallback(ctx, edevID, frqID, attempt+1)
	})
}

func requestedStart(frq sep2.FlowReservationRequest) int64 {
	if frq.IntervalRequested == nil {
		return 0
	}
	return frq.IntervalRequested.Start
}

func requestedDuration(frq sep2.FlowReservationRequest) uint32 {
	if frq.IntervalRequested == nil {
		return 0
	}
	return frq.IntervalRequested.Duration
}

// Answer is the operator's path: #670's admin route will call this to
// answer a pending request explicitly. It cancels the request's deadline
// timer on success. Returns ErrAlreadyAnswered if the request already has a
// response, whether from an earlier Answer call or because the deadline
// fallback fired first.
func (q *Queue) Answer(ctx context.Context, edevID, frqID string, decision Decision) (sep2.FlowReservationResponse, error) {
	return q.build(ctx, edevID, frqID, decision)
}

// build is the one code path that ever constructs a FlowReservationResponse
// (#666's first criterion): the deadline fallback and Answer both call it,
// and keyLock serializes them per request so only the first caller to reach
// it builds a response; the other gets ErrAlreadyAnswered.
//
// The response is stored under frqID itself, the same id the request was
// stored under (in FRPStore, a separate collection from FRQReader's, so the
// two families never collide on it). That is what lets frp.Get answer
// "does this request already have a response" durably, in place of an
// in-memory map that would otherwise grow by one entry for every request
// ever answered over the life of the process.
func (q *Queue) build(ctx context.Context, edevID, frqID string, decision Decision) (sep2.FlowReservationResponse, error) {
	key := queueKey(edevID, frqID)
	unlock := q.lockKey(key)
	defer func() {
		q.mu.Lock()
		delete(q.keyLocks, key)
		q.mu.Unlock()
		unlock()
	}()

	if _, err := q.frp.Get(ctx, edevID, frqID); err == nil {
		return sep2.FlowReservationResponse{}, ErrAlreadyAnswered
	} else if !errors.Is(err, store.ErrNotFound) {
		return sep2.FlowReservationResponse{}, fmt.Errorf("flowreservation: check existing response %s/%s: %w", edevID, frqID, err)
	}

	frq, err := q.frq.Get(ctx, edevID, frqID)
	if err != nil {
		return sep2.FlowReservationResponse{}, fmt.Errorf("flowreservation: get FlowReservationRequest %s/%s: %w", edevID, frqID, err)
	}

	frp, err := answerFor(frq, decision)
	if err != nil {
		return sep2.FlowReservationResponse{}, err
	}

	mrid, err := newFRPMRID(q.pen)
	if err != nil {
		return sep2.FlowReservationResponse{}, fmt.Errorf("flowreservation: mint FlowReservationResponse mRID: %w", err)
	}
	frp.MRID = mrid
	frp.Subject = frq.MRID

	// One clock read serves creationTime, the EventStatus derivation input
	// and the store id, so all three describe the same instant (mirrors the
	// same reasoning in pkg/sep2srv/handlers/flow_reservation.go).
	now := sep2time.Now()
	frp.CreationTime = now.Unix()
	// A grant with no interval at all (the request named none, and nothing
	// overrode it) has no start to derive against; 0 is always <= now
	// (a real wall clock), so deriveEventStatus reads it Active, matching
	// the status every response carried before #666.
	var start int64
	if frp.Interval != nil {
		start = frp.Interval.Start
	}
	es := deriveEventStatus(start, frp.CreationTime, now.Unix())
	frp.EventStatus = &es
	frp.Href = fmt.Sprintf("/edev/%s/frp/%s", edevID, frqID)

	if err := q.frp.Create(ctx, edevID, frqID, frp); err != nil {
		if errors.Is(err, store.ErrAlreadyExists) {
			return sep2.FlowReservationResponse{}, ErrAlreadyAnswered
		}
		return sep2.FlowReservationResponse{}, fmt.Errorf("flowreservation: create FlowReservationResponse: %w", err)
	}

	q.mu.Lock()
	if t, ok := q.timers[key]; ok {
		t.Stop()
		delete(q.timers, key)
	}
	q.mu.Unlock()

	return frp, nil
}

// lockKey returns an unlock func for key, taken after this call returns.
// Callers must defer the returned func. Mirrors
// internal/dercontrol.Issuer.lockScope: a single mutex guards the map
// itself, never the per-key work.
//
// build removes key from keyLocks again once it is done (still holding l),
// so the map does not grow by one entry per request for the life of the
// process. That is safe: any goroutine already waiting on l got its
// reference before the deletion and is unaffected by it, and a goroutine
// that looks the key up afterward (necessarily after build's own q.mu
// section, since both go through q.mu) only does so once build has already
// made the durable, store-backed exactly-once check (frp.Get/Create) the
// correct answer, so a freshly made mutex for that same key is still
// exclusive in every way that matters.
func (q *Queue) lockKey(key string) func() {
	q.mu.Lock()
	l, ok := q.keyLocks[key]
	if !ok {
		l = &sync.Mutex{}
		q.keyLocks[key] = l
	}
	q.mu.Unlock()

	l.Lock()
	return l.Unlock
}

// Close marks the queue closed (Submit and a pending retry stop scheduling
// new timers) and stops every pending deadline timer without answering the
// requests they were scheduled for, so a test or a server shutdown leaves
// no timer goroutine running past this call. Answered requests are
// unaffected: their timers were already stopped by build.
func (q *Queue) Close() {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.closed = true
	for key, t := range q.timers {
		t.Stop()
		delete(q.timers, key)
	}
}
