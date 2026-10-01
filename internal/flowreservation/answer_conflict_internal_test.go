package flowreservation

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

// racedAnswers replays the per-request lock race (#799) without goroutines:
// on the first Create, a racing attempt writes its answer record first, so
// this Create is refused, and the racer then fails its own response and
// takes its record back. No response exists afterwards.
type racedAnswers struct {
	*memory.ScopedStore[AnswerRecord]
	raced bool
}

func (s *racedAnswers) Create(ctx context.Context, parentID, id string, rec AnswerRecord) error {
	if s.raced {
		return s.ScopedStore.Create(ctx, parentID, id, rec)
	}
	s.raced = true
	racer := AnswerRecord{Action: ActionAnswer, By: Attribution{Kind: KindOperator, Principal: PrincipalAdminKey, At: 1}}
	if err := s.ScopedStore.Create(ctx, parentID, id, racer); err != nil {
		return err
	}
	err := s.ScopedStore.Create(ctx, parentID, id, rec)
	if derr := s.ScopedStore.Delete(ctx, parentID, id); derr != nil {
		return derr
	}
	return err
}

// failingTakeBack refuses to delete a record, so a take-back after a failed
// response Create cannot complete.
type failingTakeBack struct {
	*memory.ScopedStore[AnswerRecord]
	err error
}

func (s *failingTakeBack) Delete(context.Context, string, string) error { return s.err }

func conflictRequest() sep2.FlowReservationRequest {
	return sep2.FlowReservationRequest{MRID: "FRQ001", EnergyRequested: &sep2.SignedRealEnergy{Value: 10000}}
}

// An answer-record conflict is not an existing response: Answer must not
// report ErrAlreadyAnswered while no response is stored.
func TestAnswer_AnswerRecordConflictIsNotAlreadyAnswered(t *testing.T) {
	frqStore := &stubFRQReader{}
	frqStore.put("dev1", "frq1", conflictRequest())
	frpStore := &stubFRPStore{}
	q := NewQueue(frqStore, frpStore, PermissiveGate{}, Config{Deadline: time.Hour}, nil)
	q.RecordTimers()
	t.Cleanup(q.Close)
	q.RecordAnswers(NewAnswers(&racedAnswers{ScopedStore: memory.NewScopedStore[AnswerRecord]()}))

	_, err := q.Answer(context.Background(), "dev1", "frq1", Decision{By: Attribution{Kind: KindOperator, At: 2}})
	if err == nil || errors.Is(err, ErrAlreadyAnswered) {
		t.Fatalf("Answer err = %v, want a failure that is not ErrAlreadyAnswered", err)
	}
	if !errors.Is(err, store.ErrAlreadyExists) {
		t.Errorf("Answer err = %v, want it to carry the answer record's store.ErrAlreadyExists", err)
	}
	if n := frpStore.count(); n != 0 {
		t.Errorf("responses stored = %d, want 0", n)
	}
}

// The deadline fallback that loses the answer-record race keeps a retry
// armed, and that retry answers the request exactly once.
func TestFallback_AnswerRecordConflictKeepsTheTimerAndAnswersOnce(t *testing.T) {
	frqStore := &stubFRQReader{}
	frqStore.put("dev1", "frq1", conflictRequest())
	frpStore := &stubFRPStore{}
	q := NewQueue(frqStore, frpStore, PermissiveGate{}, Config{Deadline: time.Hour, RetryBackoff: time.Second, RetryAttempts: 3}, nil)
	timers := q.RecordTimers()
	t.Cleanup(q.Close)
	answers := &racedAnswers{ScopedStore: memory.NewScopedStore[AnswerRecord]()}
	q.RecordAnswers(NewAnswers(answers))

	q.attemptFallback(context.Background(), "dev1", "frq1", 1)

	if n := frpStore.count(); n != 0 {
		t.Fatalf("responses after the raced attempt = %d, want 0", n)
	}
	live := timers.Live()
	if len(live) != 1 || live[0].Delay != time.Second || q.PendingTimers() != 1 {
		t.Fatalf("after the raced attempt: live timers %d, pending %d; want one retry armed at the 1s backoff", len(live), q.PendingTimers())
	}
	if got := q.GivenUpCount(); got != 0 {
		t.Errorf("GivenUpCount = %d, want 0", got)
	}

	live[0].Fire()

	if n := frpStore.count(); n != 1 {
		t.Fatalf("responses after the retry = %d, want exactly 1", n)
	}
	frp, err := frpStore.Get(context.Background(), "dev1", "frq1")
	if err != nil {
		t.Fatal(err)
	}
	if frp.Subject != "FRQ001" || frp.Href != responseHref("dev1", "frq1") {
		t.Errorf("response subject %q href %q, want FRQ001 and %q", frp.Subject, frp.Href, responseHref("dev1", "frq1"))
	}
	rec, err := answers.Get(context.Background(), "dev1", "frq1")
	if err != nil || rec.By.Kind != KindDeadlineFallback || rec.Action != ActionAnswer {
		t.Errorf("answer record = %+v (err %v), want the fallback's answer", rec, err)
	}
	if q.PendingTimers() != 0 {
		t.Errorf("pending timers after the answer = %d, want 0", q.PendingTimers())
	}
}

// A response that already exists is still ErrAlreadyAnswered, and a failed
// take-back of this attempt's answer record reaches the caller with it.
func TestAnswer_FailedTakeBackReachesTheCaller(t *testing.T) {
	frqStore := &stubFRQReader{}
	frqStore.put("dev1", "frq1", conflictRequest())
	frpStore := &stubFRPStore{forceAlreadyExists: true}
	q := NewQueue(frqStore, frpStore, PermissiveGate{}, Config{Deadline: time.Hour}, nil)
	q.RecordTimers()
	t.Cleanup(q.Close)
	errDelete := errors.New("boom: delete answer record")
	answers := &failingTakeBack{ScopedStore: memory.NewScopedStore[AnswerRecord](), err: errDelete}
	q.RecordAnswers(NewAnswers(answers))

	_, err := q.Answer(context.Background(), "dev1", "frq1", Decision{By: Attribution{Kind: KindOperator, At: 2}})
	if !errors.Is(err, ErrAlreadyAnswered) {
		t.Errorf("Answer err = %v, want ErrAlreadyAnswered", err)
	}
	if !errors.Is(err, errDelete) {
		t.Errorf("Answer err = %v, want the take-back failure in it", err)
	}
	if rec, gerr := answers.Get(context.Background(), "dev1", "frq1"); gerr != nil || rec.By.Kind != KindOperator {
		t.Errorf("answer record left = %+v (err %v), want the record the take-back could not remove", rec, gerr)
	}
}
