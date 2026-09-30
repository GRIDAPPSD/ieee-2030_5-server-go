package flowreservation

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sync"
	"testing"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
)

// stubFRQReader answers Get from a fixed map, guarded by a mutex so a test
// can seed it from one goroutine while Queue reads it from the timer's.
// failGetTimes makes the next that many Get calls fail with a transient
// (non-NotFound) error, for the fallback retry tests; a missing id answers
// store.ErrNotFound, since attemptFallback treats that as "the request is
// gone" rather than something to retry.
type stubFRQReader struct {
	mu           sync.Mutex
	requests     map[string]sep2.FlowReservationRequest
	failGetTimes int
	getCalls     int
	// deleteAfterGetCall, when equal to the 1-based getCalls count of a
	// successful lookup, removes that entry immediately after returning
	// it, so the NEXT Get for the same key sees store.ErrNotFound. Used to
	// simulate the request disappearing between attemptFallback's own Get
	// and build's internal one (build's NotFound branch).
	deleteAfterGetCall int
}

func (s *stubFRQReader) Get(_ context.Context, parentID, id string) (sep2.FlowReservationRequest, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.getCalls++
	if s.failGetTimes > 0 {
		s.failGetTimes--
		return sep2.FlowReservationRequest{}, errors.New("boom: transient get failure")
	}
	key := parentID + "/" + id
	frq, ok := s.requests[key]
	if !ok {
		return sep2.FlowReservationRequest{}, store.ErrNotFound
	}
	if s.deleteAfterGetCall == s.getCalls {
		delete(s.requests, key)
	}
	return frq, nil
}

func (s *stubFRQReader) getCallCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.getCalls
}

func (s *stubFRQReader) put(parentID, id string, frq sep2.FlowReservationRequest) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.requests == nil {
		s.requests = map[string]sep2.FlowReservationRequest{}
	}
	s.requests[parentID+"/"+id] = frq
}

func (s *stubFRQReader) delete(parentID, id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.requests, parentID+"/"+id)
}

// stubFRPStore is FRPStore backed by a map, guarded by a mutex.
// failCreateTimes makes the next that many Create calls fail, for the
// fallback retry tests.
type stubFRPStore struct {
	mu              sync.Mutex
	created         map[string]sep2.FlowReservationResponse
	failCreateTimes int
	createCalls     int

	// forceAlreadyExists, when true, makes every Create answer
	// store.ErrAlreadyExists regardless of the map's own state: it isolates
	// build's mapping of that error to ErrAlreadyAnswered from whatever a
	// real store's own duplicate-key logic would need to trigger it.
	forceAlreadyExists bool
}

func (s *stubFRPStore) Get(_ context.Context, parentID, id string) (sep2.FlowReservationResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.created[parentID+"/"+id]
	if !ok {
		return sep2.FlowReservationResponse{}, store.ErrNotFound
	}
	return r, nil
}

// Create refuses a duplicate key with store.ErrAlreadyExists, matching
// memory.ScopedStore's real contract: #736's re-review found that
// exactly-once rests entirely on this refusal once a failed attempt has
// pruned its keyLock entry (see queue.go's build and lockKey comments), and
// a stub that silently overwrote instead could not catch a regression
// there.
func (s *stubFRPStore) Create(_ context.Context, parentID, id string, resource sep2.FlowReservationResponse) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.createCalls++
	if s.failCreateTimes > 0 {
		s.failCreateTimes--
		return errors.New("boom: transient create failure")
	}
	if s.forceAlreadyExists {
		return store.ErrAlreadyExists
	}
	key := parentID + "/" + id
	if _, exists := s.created[key]; exists {
		return store.ErrAlreadyExists
	}
	if s.created == nil {
		s.created = map[string]sep2.FlowReservationResponse{}
	}
	s.created[key] = resource
	return nil
}

func (s *stubFRPStore) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.created)
}

func (s *stubFRPStore) createCallCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.createCalls
}

// fakeTimer is the timer Queue.after returns when a test substitutes a
// capturing func for it: Stop is never asserted on by these tests, so it
// only needs to satisfy the interface.
type fakeTimer struct{}

func (fakeTimer) Stop() bool { return true }

// waitForCount polls get for up to 2s until it returns want, the
// poll-with-deadline idiom this package's other tests already use for an
// async effect with no signal channel.
func waitForCount(t *testing.T, get func() int, want int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if get() >= want {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("count did not reach %d within 2s (last seen %d)", want, get())
}

// TestQueue_Build_MintFailureLeavesRequestUnanswered is the whitebox half of
// what was pkg/sep2srv/handlers/flow_reservation's mint-failure test before
// #666 moved minting into Queue.build: a mint failure must not mark the
// request answered, so a later retry (Answer, or the operator's admin route
// once #670 exists) can still build the one response #666 requires.
func TestQueue_Build_MintFailureLeavesRequestUnanswered(t *testing.T) {
	orig := frpRandRead
	defer func() { frpRandRead = orig }()

	frq := sep2.FlowReservationRequest{MRID: "FRQ001", EnergyRequested: &sep2.SignedRealEnergy{Value: 10000}}
	frqStore := &stubFRQReader{}
	frqStore.put("dev1", "frq1", frq)
	frpStore := &stubFRPStore{}
	q := NewQueue(frqStore, frpStore, PermissiveGate{}, Config{Deadline: time.Hour}, nil)
	t.Cleanup(q.Close)

	frpRandRead = func(b []byte) (int, error) { return 0, errors.New("boom") }
	if _, err := q.Answer(context.Background(), "dev1", "frq1", Decision{}); err == nil {
		t.Fatal("Answer with a failing mint returned nil error, want the mint failure")
	}
	if n := frpStore.count(); n != 0 {
		t.Fatalf("responses created after a mint failure = %d, want 0", n)
	}

	frpRandRead = orig
	frp, err := q.Answer(context.Background(), "dev1", "frq1", Decision{})
	if err != nil {
		t.Fatalf("retry Answer after mint recovers: %v", err)
	}
	if n := frpStore.count(); n != 1 {
		t.Fatalf("responses created after the retry = %d, want 1", n)
	}
	if frp.MRID == "" {
		t.Error("retried Answer returned an empty MRID")
	}
}

// TestQueue_Fallback_RetriesEachInfrastructureFailureKind is #736's
// error-handling MEDIUM and coverage MEDIUM: a failed Get, mint or Create
// in the deadline fallback is retried with a bounded backoff, ending with
// exactly 1 response, rather than leaving the request unanswered after one
// failure.
func TestQueue_Fallback_RetriesEachInfrastructureFailureKind(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(frqStore *stubFRQReader, frpStore *stubFRPStore) func()
	}{
		{"Get fails once", func(frqStore *stubFRQReader, frpStore *stubFRPStore) func() {
			frqStore.failGetTimes = 1
			return func() {}
		}},
		{"Create fails once", func(frqStore *stubFRQReader, frpStore *stubFRPStore) func() {
			frpStore.failCreateTimes = 1
			return func() {}
		}},
		{"mint fails once", func(frqStore *stubFRQReader, frpStore *stubFRPStore) func() {
			orig := frpRandRead
			calls := 0
			frpRandRead = func(b []byte) (int, error) {
				calls++
				if calls == 1 {
					return 0, errors.New("boom: transient mint failure")
				}
				return orig(b)
			}
			return func() { frpRandRead = orig }
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			frq := sep2.FlowReservationRequest{MRID: "FRQ001", EnergyRequested: &sep2.SignedRealEnergy{Value: 10000}}
			frqStore := &stubFRQReader{}
			frqStore.put("dev1", "frq1", frq)
			frpStore := &stubFRPStore{}
			cleanup := tc.setup(frqStore, frpStore)
			defer cleanup()

			q := NewQueue(frqStore, frpStore, PermissiveGate{}, Config{Deadline: time.Millisecond, RetryBackoff: time.Millisecond}, nil)
			t.Cleanup(q.Close)

			q.Submit("dev1", "frq1", frq, 0)
			waitForCount(t, frpStore.count, 1)

			if n := frpStore.count(); n != 1 {
				t.Fatalf("responses created = %d, want exactly 1 after the retry", n)
			}
		})
	}
}

// TestQueue_Fallback_GivesUpAfterBoundedRetries proves the retry bound is
// real: a Create that never succeeds stops being retried after
// Config.RetryAttempts, rather than retrying forever, and no response is
// ever built for it.
func TestQueue_Fallback_GivesUpAfterBoundedRetries(t *testing.T) {
	frq := sep2.FlowReservationRequest{MRID: "FRQ001", EnergyRequested: &sep2.SignedRealEnergy{Value: 10000}}
	frqStore := &stubFRQReader{}
	frqStore.put("dev1", "frq1", frq)
	frpStore := &stubFRPStore{failCreateTimes: 1000} // never succeeds within the bound
	q := NewQueue(frqStore, frpStore, PermissiveGate{}, Config{Deadline: time.Millisecond, RetryBackoff: 2 * time.Millisecond, RetryAttempts: 3}, nil)
	t.Cleanup(q.Close)

	q.Submit("dev1", "frq1", frq, 0)

	waitForCount(t, frpStore.createCallCount, 3)
	// Give it well past 3 more backoff intervals, then confirm the count
	// stayed at 3 rather than continuing to climb.
	time.Sleep(50 * time.Millisecond)
	if n := frpStore.createCallCount(); n != 3 {
		t.Errorf("Create calls = %d, want exactly RetryAttempts (3), the retry bound must stop growth past it", n)
	}
	if n := frpStore.count(); n != 0 {
		t.Errorf("responses created = %d, want 0 (Create never succeeded)", n)
	}
	if got := q.GivenUpCount(); got != 1 {
		t.Errorf("GivenUpCount() = %d, want 1", got)
	}
	q.mu.Lock()
	_, stillTracked := q.timers[queueKey("dev1", "frq1")]
	q.mu.Unlock()
	if stillTracked {
		t.Error("timers still holds an entry after giving up: the give-up branch must forget it")
	}
}

// TestQueue_Fallback_RequestGoneStopsRetrying is #736's error-handling
// MEDIUM's other half: when the request itself is gone (store.ErrNotFound,
// the shape an EndDevice delete cascade leaves), the fallback gives up at
// once rather than retrying a request that will never come back.
func TestQueue_Fallback_RequestGoneStopsRetrying(t *testing.T) {
	frq := sep2.FlowReservationRequest{MRID: "FRQ001", EnergyRequested: &sep2.SignedRealEnergy{Value: 10000}}
	frqStore := &stubFRQReader{}
	frqStore.put("dev1", "frq1", frq)
	frpStore := &stubFRPStore{}
	q := NewQueue(frqStore, frpStore, PermissiveGate{}, Config{Deadline: time.Millisecond, RetryBackoff: 2 * time.Millisecond, RetryAttempts: 10}, nil)
	t.Cleanup(q.Close)

	frqStore.delete("dev1", "frq1") // the request is gone before the timer ever fires
	q.Submit("dev1", "frq1", frq, 0)

	time.Sleep(50 * time.Millisecond)
	if n := frpStore.count(); n != 0 {
		t.Errorf("responses created = %d, want 0", n)
	}

	// The precise count is what distinguishes "stopped at once" from
	// "retried all the way to the RetryAttempts=10 bound and then gave
	// up", which would also end at 0 responses and empty timers: exactly
	// 1 means attemptFallback's own Get saw NotFound and returned without
	// ever calling retryOrGiveUp.
	if got := frqStore.getCallCount(); got != 1 {
		t.Errorf("frq.Get calls = %d, want exactly 1 (stopped at once, not retried to the bound)", got)
	}
	if got := q.GivenUpCount(); got != 0 {
		t.Errorf("GivenUpCount() = %d, want 0: a gone request is not a give-up, it is answered by definition", got)
	}

	q.mu.Lock()
	_, stillTracked := q.timers[queueKey("dev1", "frq1")]
	q.mu.Unlock()
	if stillTracked {
		t.Error("timers still tracks a request the fallback already gave up on")
	}
}

// TestQueue_Build_NotFoundBetweenTheTwoGets covers build's OWN internal
// frq.Get, distinct from attemptFallback's: the request exists when
// attemptFallback reads it (so a decision is computed), and is gone by the
// time build re-reads it (the shape an EndDevice delete landing between
// the two would leave). build's own NotFound wrapping must still surface
// as store.ErrNotFound, so attemptFallback recognizes it as "gone" rather
// than retrying.
func TestQueue_Build_NotFoundBetweenTheTwoGets(t *testing.T) {
	frq := sep2.FlowReservationRequest{MRID: "FRQ001", EnergyRequested: &sep2.SignedRealEnergy{Value: 10000}}
	frqStore := &stubFRQReader{deleteAfterGetCall: 1}
	frqStore.put("dev1", "frq1", frq)
	frpStore := &stubFRPStore{}
	q := NewQueue(frqStore, frpStore, PermissiveGate{}, Config{Deadline: time.Millisecond, RetryBackoff: 2 * time.Millisecond, RetryAttempts: 10}, nil)
	t.Cleanup(q.Close)

	q.Submit("dev1", "frq1", frq, 0)

	time.Sleep(50 * time.Millisecond)
	if n := frpStore.count(); n != 0 {
		t.Errorf("responses created = %d, want 0", n)
	}
	if got := frqStore.getCallCount(); got != 2 {
		t.Errorf("frq.Get calls = %d, want exactly 2 (attemptFallback's own, then build's internal one finding it gone)", got)
	}
	q.mu.Lock()
	_, stillTracked := q.timers[queueKey("dev1", "frq1")]
	q.mu.Unlock()
	if stillTracked {
		t.Error("timers still tracks a request build found gone on its own internal Get")
	}
}

// TestQueue_Submit_DeadlineThroughAfterSeam is #736's coverage MEDIUM: the
// duration actually passed to q.after, not merely "a response eventually
// appears" (which a mutant scaling the delay, or changing DefaultDeadline,
// would still pass).
func TestQueue_Submit_DeadlineThroughAfterSeam(t *testing.T) {
	for _, tc := range []struct {
		name      string
		frq       sep2.FlowReservationRequest
		cfg       Config
		createdAt int64
		want      time.Duration
	}{
		{"default 300s, no requested window", sep2.FlowReservationRequest{}, Config{}, 1000, DefaultDeadline},
		{"capped at the requested start", sep2.FlowReservationRequest{IntervalRequested: &sep2.DateTimeInterval{Start: 1100}}, Config{Deadline: time.Hour}, 1000, 100 * time.Second},
		{"configured deadline, start far enough away not to cap", sep2.FlowReservationRequest{IntervalRequested: &sep2.DateTimeInterval{Start: 100000}}, Config{Deadline: 30 * time.Second}, 1000, 30 * time.Second},
		{"start at or before createdAt fires at once", sep2.FlowReservationRequest{IntervalRequested: &sep2.DateTimeInterval{Start: 500}}, Config{Deadline: time.Hour}, 1000, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			q := NewQueue(&stubFRQReader{}, &stubFRPStore{}, PermissiveGate{}, tc.cfg, nil)
			t.Cleanup(q.Close)

			var got time.Duration
			q.after = func(d time.Duration, f func()) timer {
				got = d
				return fakeTimer{}
			}

			q.Submit("dev1", "frq1", tc.frq, tc.createdAt)
			if got != tc.want {
				t.Errorf("delay passed to after = %s, want %s", got, tc.want)
			}
		})
	}
}

// TestQueue_Submit_FarFutureStartHoldsTheFullDeadline is #736's security
// MEDIUM: a start up to math.MaxInt64 seconds away must saturate rather
// than overflow into a near-zero delay, which would grant the request at
// once instead of holding it for the full configured deadline.
func TestQueue_Submit_FarFutureStartHoldsTheFullDeadline(t *testing.T) {
	q := NewQueue(&stubFRQReader{}, &stubFRPStore{}, PermissiveGate{}, Config{Deadline: DefaultDeadline}, nil)
	t.Cleanup(q.Close)

	var got time.Duration
	q.after = func(d time.Duration, f func()) timer {
		got = d
		return fakeTimer{}
	}

	frq := sep2.FlowReservationRequest{IntervalRequested: &sep2.DateTimeInterval{Start: math.MaxInt64}}
	q.Submit("dev1", "frq1", frq, 0)

	if got != DefaultDeadline {
		t.Errorf("delay passed to after = %s, want the full configured deadline %s", got, DefaultDeadline)
	}
}

// TestQueue_Build_PrunesKeyLocksAfterEachAttempt is #736's code-quality
// MEDIUM: keyLocks does not grow by one entry per request answered over
// the life of the process.
func TestQueue_Build_PrunesKeyLocksAfterEachAttempt(t *testing.T) {
	frqStore := &stubFRQReader{}
	frpStore := &stubFRPStore{}
	q := NewQueue(frqStore, frpStore, PermissiveGate{}, Config{Deadline: time.Hour}, nil)
	t.Cleanup(q.Close)

	const n = 50
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("frq%d", i)
		frq := sep2.FlowReservationRequest{MRID: id, EnergyRequested: &sep2.SignedRealEnergy{Value: 1}}
		frqStore.put("dev1", id, frq)
		if _, err := q.Answer(context.Background(), "dev1", id, Decision{}); err != nil {
			t.Fatalf("Answer %s: %v", id, err)
		}
	}

	q.mu.Lock()
	keyLocks := len(q.keyLocks)
	q.mu.Unlock()
	if keyLocks != 0 {
		t.Errorf("keyLocks entries after %d answered requests = %d, want 0", n, keyLocks)
	}
	if got := frpStore.count(); got != n {
		t.Fatalf("responses created = %d, want %d (pruning must not have cost a response)", got, n)
	}
}

// TestQueue_Submit_AfterCloseIsRefused is #736's security/error-handling
// LOW: Close stops a later Submit from scheduling anything, not just the
// timers that already existed when Close ran.
func TestQueue_Submit_AfterCloseIsRefused(t *testing.T) {
	frq := sep2.FlowReservationRequest{MRID: "FRQ001"}
	frqStore := &stubFRQReader{}
	frqStore.put("dev1", "frq1", frq)
	frpStore := &stubFRPStore{}
	q := NewQueue(frqStore, frpStore, PermissiveGate{}, Config{Deadline: time.Millisecond, RetryBackoff: time.Millisecond}, nil)
	q.Close()

	q.Submit("dev1", "frq1", frq, 0)

	time.Sleep(50 * time.Millisecond)
	if n := frpStore.count(); n != 0 {
		t.Errorf("responses created after Submit-post-Close = %d, want 0", n)
	}
	q.mu.Lock()
	timers := len(q.timers)
	q.mu.Unlock()
	if timers != 0 {
		t.Errorf("timers entries after Submit-post-Close = %d, want 0", timers)
	}
}

// TestQueue_RetryOrGiveUp_ClosedSkipsScheduling is the third give-up-path
// mutant #736's coverage re-review named: Close between a failed attempt
// and its retry being scheduled must stop the retry from being scheduled
// at all, the same refusal Submit gives, not just stop timers that already
// existed when Close ran.
func TestQueue_RetryOrGiveUp_ClosedSkipsScheduling(t *testing.T) {
	frq := sep2.FlowReservationRequest{MRID: "FRQ001", EnergyRequested: &sep2.SignedRealEnergy{Value: 10000}}
	frqStore := &stubFRQReader{}
	frqStore.put("dev1", "frq1", frq)
	frpStore := &stubFRPStore{failCreateTimes: 1000}
	q := NewQueue(frqStore, frpStore, PermissiveGate{}, Config{Deadline: time.Millisecond, RetryBackoff: 40 * time.Millisecond, RetryAttempts: 10}, nil)

	q.Submit("dev1", "frq1", frq, 0)
	waitForCount(t, frpStore.createCallCount, 1) // the first (failed) attempt has run and scheduled a retry

	q.Close()
	time.Sleep(80 * time.Millisecond) // well past the 40ms backoff the pending retry would have fired at

	if got := frpStore.createCallCount(); got != 1 {
		t.Errorf("Create calls = %d, want exactly 1: Close must stop the pending retry from ever firing", got)
	}
}

// TestQueue_Fallback_LateAnswerClearsTheRetryTimer is #736's error-handling
// LOW: when an Answer call wins between a failed attempt and its scheduled
// retry, the retry still fires and finds ErrAlreadyAnswered; it must
// forget its timers entry there rather than leaving a stale one, since
// nothing else will ever clean it up.
func TestQueue_Fallback_LateAnswerClearsTheRetryTimer(t *testing.T) {
	frq := sep2.FlowReservationRequest{MRID: "FRQ001", EnergyRequested: &sep2.SignedRealEnergy{Value: 10000}}
	frqStore := &stubFRQReader{}
	frqStore.put("dev1", "frq1", frq)
	frpStore := &stubFRPStore{failCreateTimes: 1}
	q := NewQueue(frqStore, frpStore, PermissiveGate{}, Config{Deadline: time.Millisecond, RetryBackoff: 40 * time.Millisecond}, nil)
	t.Cleanup(q.Close)

	q.Submit("dev1", "frq1", frq, 0)
	waitForCount(t, frpStore.createCallCount, 1) // the first (failed) attempt has run and scheduled a retry

	if _, err := q.Answer(context.Background(), "dev1", "frq1", Decision{}); err != nil {
		t.Fatalf("Answer: %v", err)
	}

	time.Sleep(80 * time.Millisecond) // let the scheduled retry fire and find ErrAlreadyAnswered

	q.mu.Lock()
	_, stillTracked := q.timers[queueKey("dev1", "frq1")]
	q.mu.Unlock()
	if stillTracked {
		t.Error("timers still holds an entry after the late-won retry fired and found ErrAlreadyAnswered")
	}
	if n := frpStore.count(); n != 1 {
		t.Errorf("responses stored = %d, want exactly 1", n)
	}
}

// TestQueue_Build_GetGuardAloneShortCircuitsBeforeCreate isolates the
// frp.Get pre-check: seeding an existing response directly (bypassing
// build) and then answering must refuse before ever calling Create, not
// merely end in the same ErrAlreadyAnswered that Create's own
// ErrAlreadyExists mapping would also produce.
func TestQueue_Build_GetGuardAloneShortCircuitsBeforeCreate(t *testing.T) {
	frq := sep2.FlowReservationRequest{MRID: "FRQ001", EnergyRequested: &sep2.SignedRealEnergy{Value: 10000}}
	frqStore := &stubFRQReader{}
	frqStore.put("dev1", "frq1", frq)
	frpStore := &stubFRPStore{}
	if err := frpStore.Create(context.Background(), "dev1", "frq1", sep2.FlowReservationResponse{Event: sep2.Event{MRID: "EXISTING"}}); err != nil {
		t.Fatalf("seed existing response: %v", err)
	}

	q := NewQueue(frqStore, frpStore, PermissiveGate{}, Config{Deadline: time.Hour}, nil)
	t.Cleanup(q.Close)

	if _, err := q.Answer(context.Background(), "dev1", "frq1", Decision{}); !errors.Is(err, ErrAlreadyAnswered) {
		t.Fatalf("Answer on an already-answered request: err = %v, want ErrAlreadyAnswered", err)
	}
	if got := frpStore.createCallCount(); got != 1 { // the 1 seed call only; the guard must add none
		t.Errorf("Create calls = %d, want 1 (the seed only): the frp.Get pre-check must refuse before ever attempting another Create", got)
	}
}

// TestQueue_Build_MapsCreateAlreadyExistsToErrAlreadyAnswered isolates
// build's OWN mapping of store.ErrAlreadyExists to ErrAlreadyAnswered,
// independent of whatever triggered it in a real store and independent of
// the frp.Get pre-check (which never fires here, since forceAlreadyExists
// answers Create's refusal without the key ever being in the stub's map,
// so Get still reports store.ErrNotFound).
func TestQueue_Build_MapsCreateAlreadyExistsToErrAlreadyAnswered(t *testing.T) {
	frq := sep2.FlowReservationRequest{MRID: "FRQ001", EnergyRequested: &sep2.SignedRealEnergy{Value: 10000}}
	frqStore := &stubFRQReader{}
	frqStore.put("dev1", "frq1", frq)
	frpStore := &stubFRPStore{forceAlreadyExists: true}
	q := NewQueue(frqStore, frpStore, PermissiveGate{}, Config{Deadline: time.Hour}, nil)
	t.Cleanup(q.Close)

	if _, err := q.Answer(context.Background(), "dev1", "frq1", Decision{}); !errors.Is(err, ErrAlreadyAnswered) {
		t.Fatalf("err = %v, want ErrAlreadyAnswered", err)
	}
	if got := frpStore.createCallCount(); got != 1 {
		t.Errorf("Create calls = %d, want 1 (the Get pre-check must not have refused it first)", got)
	}
}

// TestStubFRPStore_CreateRefusesADuplicate isolates the OTHER exactly-once
// guard directly at the store level, independent of Queue's own locking:
// once a failed attempt has pruned its keyLock entry (see lockKey's
// comment), a caller that arrives fresh no longer serializes against one
// already in flight, so FRPStore.Create refusing a duplicate key is what
// is actually left to keep the result to exactly one response, the same
// contract memory.ScopedStore.Create already has to honor. Reverting this
// stub to overwrite-on-Create, the shape it had before #736's re-review,
// makes this fail immediately.
func TestStubFRPStore_CreateRefusesADuplicate(t *testing.T) {
	frpStore := &stubFRPStore{}
	first := sep2.FlowReservationResponse{Subject: "first"}
	second := sep2.FlowReservationResponse{Subject: "second"}

	if err := frpStore.Create(context.Background(), "dev1", "frq1", first); err != nil {
		t.Fatalf("first Create: %v", err)
	}
	if err := frpStore.Create(context.Background(), "dev1", "frq1", second); !errors.Is(err, store.ErrAlreadyExists) {
		t.Fatalf("second Create for the same key: err = %v, want store.ErrAlreadyExists", err)
	}

	got, err := frpStore.Get(context.Background(), "dev1", "frq1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Subject != "first" {
		t.Errorf("stored response Subject = %q, want %q: the refused second Create must not have overwritten it", got.Subject, "first")
	}
}

// TestDefaultDeadline_Value and TestDefaultRetryBackoff_Value pin the
// literal durations, not just each constant compared with itself (which a
// test using DefaultDeadline on both sides of an equality would still pass
// after the constant changed).
func TestDefaultDeadline_Value(t *testing.T) {
	if DefaultDeadline != 300*time.Second {
		t.Errorf("DefaultDeadline = %s, want 300s", DefaultDeadline)
	}
}

func TestDefaultRetryBackoff_Value(t *testing.T) {
	if DefaultRetryBackoff != 5*time.Second {
		t.Errorf("DefaultRetryBackoff = %s, want 5s", DefaultRetryBackoff)
	}
}

// TestQueue_Build_GatedCreateErrorsKeepTheirMeaning: for a grant with a
// window, whose Create runs inside the gate, a duplicate key still maps to
// ErrAlreadyAnswered and a transient failure stays a create failure,
// never ErrCommitmentCheck, so the fallback retries instead of denying.
func TestQueue_Build_GatedCreateErrorsKeepTheirMeaning(t *testing.T) {
	frq := sep2.FlowReservationRequest{
		MRID:              "FRQ001",
		EnergyRequested:   &sep2.SignedRealEnergy{Value: 10000},
		IntervalRequested: &sep2.DateTimeInterval{Start: 5000, Duration: 600},
	}
	for _, tc := range []struct {
		name  string
		frp   *stubFRPStore
		check func(error) bool
	}{
		{"duplicate", &stubFRPStore{forceAlreadyExists: true}, func(err error) bool { return errors.Is(err, ErrAlreadyAnswered) }},
		{"transient", &stubFRPStore{failCreateTimes: 1}, func(err error) bool {
			return err != nil && !errors.Is(err, ErrCommitmentCheck) && !errors.Is(err, ErrAlreadyAnswered)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			frqStore := &stubFRQReader{}
			frqStore.put("dev1", "frq1", frq)
			q := NewQueue(frqStore, tc.frp, PermissiveGate{}, Config{Deadline: time.Hour}, nil)
			t.Cleanup(q.Close)
			_, err := q.Answer(context.Background(), "dev1", "frq1", Decision{})
			if !tc.check(err) {
				t.Fatalf("err = %v", err)
			}
			if got := tc.frp.createCallCount(); got != 1 {
				t.Fatalf("Create calls = %d, want 1: the gate must have run the write", got)
			}
		})
	}
}
