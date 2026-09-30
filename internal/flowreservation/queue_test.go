package flowreservation_test

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/flowreservation"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

// shortDeadline is small enough to keep these tests fast, and large enough
// that a scheduling jitter of a few milliseconds under load does not fire
// the timer before the assertion that nothing has happened yet runs.
const shortDeadline = 30 * time.Millisecond

// waitForResponse polls frpStore for up to 2s, the same poll-with-deadline
// idiom internal/server's admin tests use for an async side effect with no
// signal channel of its own.
func waitForResponse(t *testing.T, frpStore *memory.ScopedStore[sep2.FlowReservationResponse], edevID string) []sep2.FlowReservationResponse {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		got, err := frpStore.List(context.Background(), edevID, store.ListOptions{Limit: 10})
		if err != nil {
			t.Fatalf("list responses: %v", err)
		}
		if len(got.Items) > 0 {
			return got.Items
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("no FlowReservationResponse stored under %q within 2s", edevID)
	return nil
}

func storeRequest(t *testing.T, frqStore *memory.ScopedStore[sep2.FlowReservationRequest], edevID, frqID string, frq sep2.FlowReservationRequest) {
	t.Helper()
	if err := frqStore.Create(context.Background(), edevID, frqID, frq); err != nil {
		t.Fatalf("store request: %v", err)
	}
}

func testRequest() sep2.FlowReservationRequest {
	return sep2.FlowReservationRequest{
		MRID:              "REQ1",
		EnergyRequested:   &sep2.SignedRealEnergy{Value: 10000},
		PowerRequested:    &sep2.ActivePower{Value: 5000},
		IntervalRequested: &sep2.DateTimeInterval{Start: time.Now().Add(time.Hour).Unix(), Duration: 3600},
	}
}

// alwaysFree and alwaysCommitted are CommitmentChecker stand-ins for the
// deadline fallback tests: #714 has not landed the real ledger, so these
// exercise the two branches Queue.fallback must choose between.
type alwaysFree struct{}

func (alwaysFree) Committed(context.Context, string, int64, uint32) (bool, error) { return false, nil }

type alwaysCommitted struct{}

func (alwaysCommitted) Committed(context.Context, string, int64, uint32) (bool, error) {
	return true, nil
}

type checkerError struct{ err error }

func (c checkerError) Committed(context.Context, string, int64, uint32) (bool, error) {
	return false, c.err
}

// TestQueue_SubmitCreatesNoResponseUntilAnswered is #666's first criterion:
// storing a request and calling Submit builds nothing by itself.
func TestQueue_SubmitCreatesNoResponseUntilAnswered(t *testing.T) {
	t.Parallel()
	frqStore := memory.NewScopedStore[sep2.FlowReservationRequest]()
	frpStore := memory.NewScopedStore[sep2.FlowReservationResponse]()
	q := flowreservation.NewQueue(frqStore, frpStore, alwaysFree{}, flowreservation.Config{Deadline: time.Hour}, nil)
	t.Cleanup(q.Close)

	frq := testRequest()
	storeRequest(t, frqStore, "dev1", "frq1", frq)
	q.Submit("dev1", "frq1", frq, time.Now().Unix())

	time.Sleep(20 * time.Millisecond)
	got, err := frpStore.List(context.Background(), "dev1", store.ListOptions{Limit: 10})
	if err != nil {
		t.Fatalf("list responses: %v", err)
	}
	if len(got.Items) != 0 {
		t.Fatalf("responses stored = %d, want 0 before any answer", len(got.Items))
	}
}

// TestQueue_Answer_ExactlyOneResponse is #666's first criterion, the other
// half: Answer builds exactly one response, and a second Answer call for
// the same request neither builds a second one nor changes the first.
func TestQueue_Answer_ExactlyOneResponse(t *testing.T) {
	t.Parallel()
	frqStore := memory.NewScopedStore[sep2.FlowReservationRequest]()
	frpStore := memory.NewScopedStore[sep2.FlowReservationResponse]()
	q := flowreservation.NewQueue(frqStore, frpStore, alwaysFree{}, flowreservation.Config{Deadline: time.Hour}, nil)
	t.Cleanup(q.Close)

	frq := testRequest()
	storeRequest(t, frqStore, "dev1", "frq1", frq)
	q.Submit("dev1", "frq1", frq, time.Now().Unix())

	before := time.Now().Unix()
	first, err := q.Answer(context.Background(), "dev1", "frq1", flowreservation.Decision{})
	after := time.Now().Unix()
	if err != nil {
		t.Fatalf("first Answer: %v", err)
	}
	if first.Subject != frq.MRID {
		t.Errorf("Subject = %q, want the request mRID %q", first.Subject, frq.MRID)
	}
	if first.CreationTime < before || first.CreationTime > after {
		t.Errorf("CreationTime = %d, want in [%d, %d]", first.CreationTime, before, after)
	}
	if first.Href == "" {
		t.Error("Href is empty")
	}

	_, err = q.Answer(context.Background(), "dev1", "frq1", flowreservation.Decision{Kind: flowreservation.Deny})
	if !errors.Is(err, flowreservation.ErrAlreadyAnswered) {
		t.Fatalf("second Answer err = %v, want ErrAlreadyAnswered", err)
	}

	got, err := frpStore.List(context.Background(), "dev1", store.ListOptions{Limit: 10})
	if err != nil {
		t.Fatalf("list responses: %v", err)
	}
	if len(got.Items) != 1 {
		t.Fatalf("responses stored = %d, want 1", len(got.Items))
	}
	if got.Items[0].MRID != first.MRID {
		t.Errorf("stored response MRID = %q, want the first Answer's %q; the refused second call must not have altered it", got.Items[0].MRID, first.MRID)
	}
	if got.Items[0].Interval.Duration == 0 {
		t.Errorf("stored response is a denial; the refused second Answer(Deny) must not have overwritten the first grant")
	}
}

// TestQueue_Answer_CancelsFallbackTimer proves an operator answer stops the
// deadline fallback from also firing: waiting well past the deadline still
// finds exactly one response.
func TestQueue_Answer_CancelsFallbackTimer(t *testing.T) {
	t.Parallel()
	frqStore := memory.NewScopedStore[sep2.FlowReservationRequest]()
	frpStore := memory.NewScopedStore[sep2.FlowReservationResponse]()
	q := flowreservation.NewQueue(frqStore, frpStore, alwaysFree{}, flowreservation.Config{Deadline: shortDeadline}, nil)
	t.Cleanup(q.Close)

	frq := testRequest()
	storeRequest(t, frqStore, "dev1", "frq1", frq)
	q.Submit("dev1", "frq1", frq, time.Now().Unix())

	if _, err := q.Answer(context.Background(), "dev1", "frq1", flowreservation.Decision{}); err != nil {
		t.Fatalf("Answer: %v", err)
	}

	time.Sleep(shortDeadline * 4)
	got, err := frpStore.List(context.Background(), "dev1", store.ListOptions{Limit: 10})
	if err != nil {
		t.Fatalf("list responses: %v", err)
	}
	if len(got.Items) != 1 {
		t.Fatalf("responses stored = %d, want 1; the cancelled fallback must not have built a second one", len(got.Items))
	}
}

// TestQueue_DeadlineFallback_GrantsWhenUncommitted is #666's fourth
// criterion, the grant branch: with no operator answer, once the deadline
// passes, an uncontested fleet window is granted as asked.
func TestQueue_DeadlineFallback_GrantsWhenUncommitted(t *testing.T) {
	t.Parallel()
	frqStore := memory.NewScopedStore[sep2.FlowReservationRequest]()
	frpStore := memory.NewScopedStore[sep2.FlowReservationResponse]()
	q := flowreservation.NewQueue(frqStore, frpStore, alwaysFree{}, flowreservation.Config{Deadline: shortDeadline}, nil)
	t.Cleanup(q.Close)

	frq := testRequest()
	storeRequest(t, frqStore, "dev1", "frq1", frq)
	q.Submit("dev1", "frq1", frq, time.Now().Unix())

	items := waitForResponse(t, frpStore, "dev1")
	if len(items) != 1 {
		t.Fatalf("responses stored = %d, want 1", len(items))
	}
	frp := items[0]
	if frp.Interval == nil || frp.Interval.Duration == 0 {
		t.Fatalf("fallback response = %+v, want a grant (non-zero duration)", frp.Interval)
	}
	if *frp.EnergyAvailable != *frq.EnergyRequested {
		t.Errorf("EnergyAvailable = %+v, want the requested %+v (grant as asked)", frp.EnergyAvailable, frq.EnergyRequested)
	}
}

// TestQueue_DeadlineFallback_DeniesWhenCommitted is #666's fourth
// criterion, the deny branch: an already-committed fleet window is denied
// automatically at the deadline.
func TestQueue_DeadlineFallback_DeniesWhenCommitted(t *testing.T) {
	t.Parallel()
	frqStore := memory.NewScopedStore[sep2.FlowReservationRequest]()
	frpStore := memory.NewScopedStore[sep2.FlowReservationResponse]()
	q := flowreservation.NewQueue(frqStore, frpStore, alwaysCommitted{}, flowreservation.Config{Deadline: shortDeadline}, nil)
	t.Cleanup(q.Close)

	frq := testRequest()
	storeRequest(t, frqStore, "dev1", "frq1", frq)
	q.Submit("dev1", "frq1", frq, time.Now().Unix())

	items := waitForResponse(t, frpStore, "dev1")
	frp := items[0]
	if frp.Interval == nil || frp.Interval.Duration != 0 {
		t.Fatalf("fallback response = %+v, want a denial (duration 0)", frp.Interval)
	}
}

// TestQueue_DeadlineFallback_ChecksFailClosed proves the fallback denies,
// rather than grants, when the commitment check itself fails: an
// indeterminate answer must not be read as "free capacity" (secure-coding
// fail-closed rule).
func TestQueue_DeadlineFallback_ChecksFailClosed(t *testing.T) {
	t.Parallel()
	frqStore := memory.NewScopedStore[sep2.FlowReservationRequest]()
	frpStore := memory.NewScopedStore[sep2.FlowReservationResponse]()
	q := flowreservation.NewQueue(frqStore, frpStore, checkerError{err: errors.New("backend unreachable")}, flowreservation.Config{Deadline: shortDeadline}, nil)
	t.Cleanup(q.Close)

	frq := testRequest()
	storeRequest(t, frqStore, "dev1", "frq1", frq)
	q.Submit("dev1", "frq1", frq, time.Now().Unix())

	items := waitForResponse(t, frpStore, "dev1")
	frp := items[0]
	if frp.Interval == nil || frp.Interval.Duration != 0 {
		t.Fatalf("fallback response = %+v, want a denial (duration 0) when the commitment check errors", frp.Interval)
	}
}

// TestQueue_DeadlineCappedAtRequestedStart is D1's "never later than the
// requested start" bound: a configured deadline far longer than the time
// left before the request's own start must not delay the fallback past
// that start.
func TestQueue_DeadlineCappedAtRequestedStart(t *testing.T) {
	t.Parallel()
	frqStore := memory.NewScopedStore[sep2.FlowReservationRequest]()
	frpStore := memory.NewScopedStore[sep2.FlowReservationResponse]()
	// Configured deadline is generous; the request's own start is soon.
	q := flowreservation.NewQueue(frqStore, frpStore, alwaysFree{}, flowreservation.Config{Deadline: time.Hour}, nil)
	t.Cleanup(q.Close)

	createdAt := time.Now().Unix()
	frq := testRequest()
	frq.IntervalRequested = &sep2.DateTimeInterval{Start: createdAt, Duration: 3600} // starts "now"
	storeRequest(t, frqStore, "dev1", "frq1", frq)
	q.Submit("dev1", "frq1", frq, createdAt)

	// The cap resolves to ~0s, not the 1h configured bound, so the response
	// must appear well inside this test's normal poll window.
	waitForResponse(t, frpStore, "dev1")
}

// TestQueue_ConcurrentAnswerAndFallback_ExactlyOneResponse races the
// deadline fallback against a concurrent operator Answer call: whichever
// reaches Queue's single build path first wins, the other gets
// ErrAlreadyAnswered, and the store never holds two responses for one
// request (#666's first criterion under concurrency).
func TestQueue_ConcurrentAnswerAndFallback_ExactlyOneResponse(t *testing.T) {
	t.Parallel()
	frqStore := memory.NewScopedStore[sep2.FlowReservationRequest]()
	frpStore := memory.NewScopedStore[sep2.FlowReservationResponse]()
	q := flowreservation.NewQueue(frqStore, frpStore, alwaysFree{}, flowreservation.Config{Deadline: shortDeadline}, nil)
	t.Cleanup(q.Close)

	frq := testRequest()
	storeRequest(t, frqStore, "dev1", "frq1", frq)
	q.Submit("dev1", "frq1", frq, time.Now().Unix())

	var wg sync.WaitGroup
	results := make([]error, 20)
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := q.Answer(context.Background(), "dev1", "frq1", flowreservation.Decision{})
			results[i] = err
		}(i)
	}
	wg.Wait()

	waitForResponse(t, frpStore, "dev1")
	got, err := frpStore.List(context.Background(), "dev1", store.ListOptions{Limit: 10})
	if err != nil {
		t.Fatalf("list responses: %v", err)
	}
	if len(got.Items) != 1 {
		t.Fatalf("responses stored = %d, want 1 under concurrent Answer calls plus the fallback", len(got.Items))
	}

	wins := 0
	for _, err := range results {
		if err == nil {
			wins++
		} else if !errors.Is(err, flowreservation.ErrAlreadyAnswered) {
			t.Errorf("Answer error = %v, want nil or ErrAlreadyAnswered", err)
		}
	}
	if wins > 1 {
		t.Errorf("Answer calls that returned nil = %d, want at most 1", wins)
	}
}

// TestQueue_Answer_NoRequestedInterval_EventStatusActive covers a request
// naming no window at all (DurationRequested but no IntervalRequested is a
// real shape a conforming body can send): a grant with nil Interval has
// nothing for deriveEventStatus to read, so build defaults it to Active,
// matching every response's status before #666 rather than serving one
// with no EventStatus at all.
func TestQueue_Answer_NoRequestedInterval_EventStatusActive(t *testing.T) {
	t.Parallel()
	frqStore := memory.NewScopedStore[sep2.FlowReservationRequest]()
	frpStore := memory.NewScopedStore[sep2.FlowReservationResponse]()
	q := flowreservation.NewQueue(frqStore, frpStore, alwaysFree{}, flowreservation.Config{Deadline: time.Hour}, nil)
	t.Cleanup(q.Close)

	frq := sep2.FlowReservationRequest{MRID: "REQ1"} // no IntervalRequested
	storeRequest(t, frqStore, "dev1", "frq1", frq)
	q.Submit("dev1", "frq1", frq, time.Now().Unix())

	frp, err := q.Answer(context.Background(), "dev1", "frq1", flowreservation.Decision{})
	if err != nil {
		t.Fatalf("Answer: %v", err)
	}
	if frp.EventStatus == nil {
		t.Fatal("EventStatus is nil, want Active")
	}
	if frp.EventStatus.CurrentStatus != sep2.EventStatusActive {
		t.Errorf("CurrentStatus = %d, want %d (Active)", frp.EventStatus.CurrentStatus, sep2.EventStatusActive)
	}
}

// capturingChecker records every Committed call, so a test can assert the
// exact window Queue passed it rather than only the bool/error it returned.
type capturingChecker struct {
	mu    sync.Mutex
	calls []checkerCall
}

type checkerCall struct {
	fleetKey string
	start    int64
	duration uint32
}

func (c *capturingChecker) Committed(_ context.Context, fleetKey string, start int64, duration uint32) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls = append(c.calls, checkerCall{fleetKey, start, duration})
	return false, nil
}

func (c *capturingChecker) snapshot() []checkerCall {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]checkerCall(nil), c.calls...)
}

// TestQueue_DeadlineFallback_PassesTheRequestedWindowToTheChecker is #736's
// small item: the fleet key and the request's own interval reach
// CommitmentChecker.Committed unchanged, not zero values a mutant dropping
// the arguments would also pass under alwaysFree/alwaysCommitted.
func TestQueue_DeadlineFallback_PassesTheRequestedWindowToTheChecker(t *testing.T) {
	t.Parallel()
	frqStore := memory.NewScopedStore[sep2.FlowReservationRequest]()
	frpStore := memory.NewScopedStore[sep2.FlowReservationResponse]()
	checker := &capturingChecker{}
	q := flowreservation.NewQueue(frqStore, frpStore, checker, flowreservation.Config{Deadline: shortDeadline}, nil)
	t.Cleanup(q.Close)

	frq := testRequest()
	frq.IntervalRequested = &sep2.DateTimeInterval{Start: 424242, Duration: 1800}
	storeRequest(t, frqStore, "dev1", "frq1", frq)
	q.Submit("dev1", "frq1", frq, time.Now().Unix())

	waitForResponse(t, frpStore, "dev1")

	calls := checker.snapshot()
	if len(calls) != 1 {
		t.Fatalf("Committed calls = %d, want 1", len(calls))
	}
	want := checkerCall{fleetKey: "dev1", start: 424242, duration: 1800}
	if calls[0] != want {
		t.Errorf("Committed call = %+v, want %+v", calls[0], want)
	}
}

// TestQueue_DeadlineFallback_CancelledRequestIsDenied is #736's
// error-handling LOW: a request posted with RequestStatus Cancelled is
// denied at the deadline, never granted, and the commitment checker is
// never even consulted for it.
func TestQueue_DeadlineFallback_CancelledRequestIsDenied(t *testing.T) {
	t.Parallel()
	frqStore := memory.NewScopedStore[sep2.FlowReservationRequest]()
	frpStore := memory.NewScopedStore[sep2.FlowReservationResponse]()
	checker := &capturingChecker{}
	q := flowreservation.NewQueue(frqStore, frpStore, checker, flowreservation.Config{Deadline: shortDeadline}, nil)
	t.Cleanup(q.Close)

	frq := testRequest()
	frq.RequestStatus.RequestStatus = sep2.RequestStatusCancelled
	storeRequest(t, frqStore, "dev1", "frq1", frq)
	q.Submit("dev1", "frq1", frq, time.Now().Unix())

	items := waitForResponse(t, frpStore, "dev1")
	frp := items[0]
	if frp.Interval == nil || frp.Interval.Duration != 0 {
		t.Fatalf("fallback response for a cancelled request = %+v, want a denial (duration 0)", frp.Interval)
	}
	if calls := checker.snapshot(); len(calls) != 0 {
		t.Errorf("Committed calls for a cancelled request = %d, want 0", len(calls))
	}
}

// TestQueue_DeadlineFallback_ZeroDurationRequestIsDenied proves a request
// whose own IntervalRequested.Duration is 0 is decided as a denial at the
// deadline rather than retried forever: grant-as-asked would build a
// zero-duration interval, which answerFor correctly refuses
// (ErrGrantZeroDuration), and without the explicit check in
// attemptFallback that refusal reads as an infrastructure failure and the
// fallback would retry it up to the bound and then give up silently,
// never answering a legitimate request.
func TestQueue_DeadlineFallback_ZeroDurationRequestIsDenied(t *testing.T) {
	t.Parallel()
	frqStore := memory.NewScopedStore[sep2.FlowReservationRequest]()
	frpStore := memory.NewScopedStore[sep2.FlowReservationResponse]()
	q := flowreservation.NewQueue(frqStore, frpStore, alwaysFree{}, flowreservation.Config{Deadline: shortDeadline, RetryBackoff: shortDeadline}, nil)
	t.Cleanup(q.Close)

	frq := testRequest()
	frq.IntervalRequested = &sep2.DateTimeInterval{Start: frq.IntervalRequested.Start, Duration: 0}
	storeRequest(t, frqStore, "dev1", "frq1", frq)
	q.Submit("dev1", "frq1", frq, time.Now().Unix())

	items := waitForResponse(t, frpStore, "dev1")
	if len(items) != 1 {
		t.Fatalf("responses stored = %d, want 1", len(items))
	}
	if items[0].Interval == nil || items[0].Interval.Duration != 0 {
		t.Fatalf("response Interval = %+v, want duration 0", items[0].Interval)
	}
	if items[0].EnergyAvailable == nil || items[0].EnergyAvailable.Value != 0 {
		t.Errorf("EnergyAvailable = %+v, want 0 (a denial, not an echoed grant)", items[0].EnergyAvailable)
	}
}

// TestQueue_Answer_RefusesGrantOnCancelledRequest is the operator-path half
// of the cancellation rule: #670's admin route will call Answer directly,
// and a Grant reaching it for a cancelled request must be refused, not
// silently satisfied.
func TestQueue_Answer_RefusesGrantOnCancelledRequest(t *testing.T) {
	t.Parallel()
	frqStore := memory.NewScopedStore[sep2.FlowReservationRequest]()
	frpStore := memory.NewScopedStore[sep2.FlowReservationResponse]()
	q := flowreservation.NewQueue(frqStore, frpStore, alwaysFree{}, flowreservation.Config{Deadline: time.Hour}, nil)
	t.Cleanup(q.Close)

	frq := testRequest()
	frq.RequestStatus.RequestStatus = sep2.RequestStatusCancelled
	storeRequest(t, frqStore, "dev1", "frq1", frq)
	q.Submit("dev1", "frq1", frq, time.Now().Unix())

	_, err := q.Answer(context.Background(), "dev1", "frq1", flowreservation.Decision{})
	if !errors.Is(err, flowreservation.ErrRequestCancelled) {
		t.Fatalf("Answer(Grant) on a cancelled request: err = %v, want ErrRequestCancelled", err)
	}

	got, err := frpStore.List(context.Background(), "dev1", store.ListOptions{Limit: 10})
	if err != nil {
		t.Fatalf("list responses: %v", err)
	}
	if len(got.Items) != 0 {
		t.Errorf("responses stored = %d, want 0; a refused Grant must not have built one", len(got.Items))
	}
}

// TestQueue_DeadlineFallback_ThenLateAnswer is #666/#736's coverage HIGH:
// the deadline fires first (waited for, not raced), and an operator Answer
// that arrives afterward gets ErrAlreadyAnswered rather than building a
// second response. TestQueue_ConcurrentAnswerAndFallback_ExactlyOneResponse
// covers the reverse order under a real race; this pins the order the
// coverage review found untested, since a mutant clearing the exactly-once
// state after the fallback answers survives a test where every Answer call
// finishes before the timer ever fires.
func TestQueue_DeadlineFallback_ThenLateAnswer(t *testing.T) {
	t.Parallel()
	frqStore := memory.NewScopedStore[sep2.FlowReservationRequest]()
	frpStore := memory.NewScopedStore[sep2.FlowReservationResponse]()
	q := flowreservation.NewQueue(frqStore, frpStore, alwaysFree{}, flowreservation.Config{Deadline: shortDeadline}, nil)
	t.Cleanup(q.Close)

	frq := testRequest()
	storeRequest(t, frqStore, "dev1", "frq1", frq)
	q.Submit("dev1", "frq1", frq, time.Now().Unix())

	waitForResponse(t, frpStore, "dev1")

	_, err := q.Answer(context.Background(), "dev1", "frq1", flowreservation.Decision{Kind: flowreservation.Deny})
	if !errors.Is(err, flowreservation.ErrAlreadyAnswered) {
		t.Fatalf("late Answer err = %v, want ErrAlreadyAnswered", err)
	}

	got, err := frpStore.List(context.Background(), "dev1", store.ListOptions{Limit: 10})
	if err != nil {
		t.Fatalf("list responses: %v", err)
	}
	if len(got.Items) != 1 {
		t.Fatalf("responses stored = %d, want exactly 1", len(got.Items))
	}
}

// TestQueue_PENLowBitsArePEN mirrors the handler-level PEN test: a
// configured PEN reaches the built response's mRID in its low 32 bits.
func TestQueue_PENLowBitsArePEN(t *testing.T) {
	t.Parallel()
	frqStore := memory.NewScopedStore[sep2.FlowReservationRequest]()
	frpStore := memory.NewScopedStore[sep2.FlowReservationResponse]()
	pen := uint32(0x40732001)
	q := flowreservation.NewQueue(frqStore, frpStore, alwaysFree{}, flowreservation.Config{Deadline: time.Hour}, &pen)
	t.Cleanup(q.Close)

	frq := testRequest()
	storeRequest(t, frqStore, "dev1", "frq1", frq)
	q.Submit("dev1", "frq1", frq, time.Now().Unix())

	frp, err := q.Answer(context.Background(), "dev1", "frq1", flowreservation.Decision{})
	if err != nil {
		t.Fatalf("Answer: %v", err)
	}
	raw, err := hex.DecodeString(frp.MRID)
	if err != nil {
		t.Fatalf("MRID %q is not hex: %v", frp.MRID, err)
	}
	if gotPEN := binary.BigEndian.Uint32(raw[12:]); gotPEN != pen {
		t.Fatalf("low 32 bits of MRID %q = %#x, want configured PEN %#x", frp.MRID, gotPEN, pen)
	}
}
