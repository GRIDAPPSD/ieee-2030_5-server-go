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
}

func (s *stubFRQReader) Get(_ context.Context, parentID, id string) (sep2.FlowReservationRequest, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.getCalls++
	if s.failGetTimes > 0 {
		s.failGetTimes--
		return sep2.FlowReservationRequest{}, errors.New("boom: transient get failure")
	}
	frq, ok := s.requests[parentID+"/"+id]
	if !ok {
		return sep2.FlowReservationRequest{}, store.ErrNotFound
	}
	return frq, nil
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

func (s *stubFRPStore) Create(_ context.Context, parentID, id string, resource sep2.FlowReservationResponse) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.createCalls++
	if s.failCreateTimes > 0 {
		s.failCreateTimes--
		return errors.New("boom: transient create failure")
	}
	if s.created == nil {
		s.created = map[string]sep2.FlowReservationResponse{}
	}
	s.created[parentID+"/"+id] = resource
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
	q := NewQueue(frqStore, frpStore, PermissiveCommitmentChecker{}, Config{Deadline: time.Hour}, nil)
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

			q := NewQueue(frqStore, frpStore, PermissiveCommitmentChecker{}, Config{Deadline: time.Millisecond, RetryBackoff: time.Millisecond}, nil)
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
	q := NewQueue(frqStore, frpStore, PermissiveCommitmentChecker{}, Config{Deadline: time.Millisecond, RetryBackoff: 2 * time.Millisecond, RetryAttempts: 3}, nil)
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
	q := NewQueue(frqStore, frpStore, PermissiveCommitmentChecker{}, Config{Deadline: time.Millisecond, RetryBackoff: 2 * time.Millisecond, RetryAttempts: 10}, nil)
	t.Cleanup(q.Close)

	frqStore.delete("dev1", "frq1") // the request is gone before the timer ever fires
	q.Submit("dev1", "frq1", frq, 0)

	time.Sleep(50 * time.Millisecond)
	if n := frpStore.count(); n != 0 {
		t.Errorf("responses created = %d, want 0", n)
	}

	q.mu.Lock()
	_, stillTracked := q.timers[queueKey("dev1", "frq1")]
	q.mu.Unlock()
	if stillTracked {
		t.Error("timers still tracks a request the fallback already gave up on")
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
			q := NewQueue(&stubFRQReader{}, &stubFRPStore{}, PermissiveCommitmentChecker{}, tc.cfg, nil)
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
	q := NewQueue(&stubFRQReader{}, &stubFRPStore{}, PermissiveCommitmentChecker{}, Config{Deadline: DefaultDeadline}, nil)
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
	q := NewQueue(frqStore, frpStore, PermissiveCommitmentChecker{}, Config{Deadline: time.Hour}, nil)
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
	q := NewQueue(frqStore, frpStore, PermissiveCommitmentChecker{}, Config{Deadline: time.Millisecond, RetryBackoff: time.Millisecond}, nil)
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
