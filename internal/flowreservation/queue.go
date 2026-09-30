package flowreservation

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/handlers/sep2time"
)

// FRQReader is the subset of store.ScopedStore[sep2.FlowReservationRequest]
// Queue needs: it only ever reads a request back to answer it, never writes
// one (POST /edev/{id}/frq owns that write).
type FRQReader interface {
	Get(ctx context.Context, parentID, id string) (sep2.FlowReservationRequest, error)
}

// FRPCreator is the subset of store.ScopedStore[sep2.FlowReservationResponse]
// Queue needs to store a built answer.
type FRPCreator interface {
	Create(ctx context.Context, parentID, id string, resource sep2.FlowReservationResponse) error
}

// timer is the *time.Timer subset Queue uses, so a test can substitute a
// fake without waiting on real time.
type timer interface {
	Stop() bool
}

// Queue holds every FlowReservationRequest awaiting an answer and, for each,
// fires the deadline fallback (#666 D1/D2) if the operator has not answered
// by then: grant as asked when the fleet's window is free, deny otherwise.
//
// The zero value is not usable: construct with NewQueue.
type Queue struct {
	frq     FRQReader
	frp     FRPCreator
	checker CommitmentChecker
	cfg     Config
	pen     *uint32

	// after schedules f to run after d and returns a stoppable handle;
	// production uses time.AfterFunc, tests substitute a short-deadline or
	// synchronous stand-in so no test waits out a real 300 s bound.
	after func(d time.Duration, f func()) timer

	// mu guards answered, timers and keyLocks. It is never held across the
	// store I/O or the network-shaped work in build: keyLocks[key] is what
	// serializes concurrent Submit/Answer/fallback calls for one request,
	// so two callers racing to answer the same request cannot both win.
	mu       sync.Mutex
	answered map[string]bool
	timers   map[string]timer
	keyLocks map[string]*sync.Mutex
}

// NewQueue builds a Queue. checker nil takes PermissiveCommitmentChecker
// (the default until #714 lands the real commitment ledger). cfg's zero
// Deadline takes DefaultDeadline. pen is threaded straight to newFRPMRID,
// same meaning as RouterConfig.PEN.
func NewQueue(frq FRQReader, frp FRPCreator, checker CommitmentChecker, cfg Config, pen *uint32) *Queue {
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
		answered: make(map[string]bool),
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
// requested start").
func (q *Queue) Submit(edevID, frqID string, frq sep2.FlowReservationRequest, createdAt int64) {
	key := queueKey(edevID, frqID)
	delay := q.deadlineDelay(frq, createdAt)

	q.mu.Lock()
	q.timers[key] = q.after(delay, func() {
		q.fallback(context.Background(), edevID, frqID)
	})
	q.mu.Unlock()
}

// deadlineDelay is the configured bound, capped so the fallback never fires
// later than the requested start. A request with no IntervalRequested has
// no start to cap against, so it gets the configured bound unmodified.
func (q *Queue) deadlineDelay(frq sep2.FlowReservationRequest, createdAt int64) time.Duration {
	delay := q.cfg.Deadline
	if frq.IntervalRequested == nil {
		return delay
	}
	untilStart := time.Duration(frq.IntervalRequested.Start-createdAt) * time.Second
	if untilStart < 0 {
		untilStart = 0
	}
	if untilStart < delay {
		return untilStart
	}
	return delay
}

// fallback is the deadline path: D2, grant as asked when the fleet's
// window is uncommitted, deny otherwise. A commitment check that cannot
// complete denies rather than grants (fail closed): an indeterminate
// answer must not be read as "free capacity".
func (q *Queue) fallback(ctx context.Context, edevID, frqID string) {
	frq, err := q.frq.Get(ctx, edevID, frqID)
	if err != nil {
		log.Printf("flowreservation: deadline fallback: get %s/%s: %v", edevID, frqID, err)
		return
	}

	decision := Decision{Kind: Grant}
	committed, err := q.checker.Committed(ctx, edevID, requestedStart(frq), requestedDuration(frq))
	switch {
	case err != nil:
		log.Printf("flowreservation: deadline fallback: commitment check %s/%s: %v; denying (fail closed)", edevID, frqID, err)
		decision = Decision{Kind: Deny}
	case committed:
		decision = Decision{Kind: Deny}
	}

	if _, err := q.build(ctx, edevID, frqID, decision); err != nil && !errors.Is(err, ErrAlreadyAnswered) {
		log.Printf("flowreservation: deadline fallback: answer %s/%s: %v", edevID, frqID, err)
	}
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
// (#666's first criterion): Submit's fallback and Answer both call it, and
// keyLock serializes them per request so only the first caller to reach it
// builds a response; the other gets ErrAlreadyAnswered.
func (q *Queue) build(ctx context.Context, edevID, frqID string, decision Decision) (sep2.FlowReservationResponse, error) {
	key := queueKey(edevID, frqID)
	unlock := q.lockKey(key)
	defer unlock()

	q.mu.Lock()
	already := q.answered[key]
	q.mu.Unlock()
	if already {
		return sep2.FlowReservationResponse{}, ErrAlreadyAnswered
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

	frpID := fmt.Sprintf("frp-%d", now.UnixNano())
	frp.Href = fmt.Sprintf("/edev/%s/frp/%s", edevID, frpID)

	if err := q.frp.Create(ctx, edevID, frpID, frp); err != nil {
		return sep2.FlowReservationResponse{}, fmt.Errorf("flowreservation: create FlowReservationResponse: %w", err)
	}

	q.mu.Lock()
	q.answered[key] = true
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

// Close stops every pending deadline timer without answering the requests
// they were scheduled for, so a test or a server shutdown leaves no timer
// goroutine running past this call. Answered requests are unaffected: their
// timers were already stopped by build.
func (q *Queue) Close() {
	q.mu.Lock()
	defer q.mu.Unlock()
	for key, t := range q.timers {
		t.Stop()
		delete(q.timers, key)
	}
}
