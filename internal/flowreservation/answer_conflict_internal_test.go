package flowreservation

import (
	"bytes"
	"context"
	"errors"
	"log"
	"strings"
	"sync"
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
	answers := &racedAnswers{ScopedStore: memory.NewScopedStore[AnswerRecord]()}
	q.RecordAnswers(NewAnswers(answers))

	_, err := q.Answer(context.Background(), "dev1", "frq1", Decision{By: Attribution{Kind: KindOperator, At: 2}})
	if err == nil || errors.Is(err, ErrAlreadyAnswered) {
		t.Fatalf("Answer err = %v, want a failure that is not ErrAlreadyAnswered", err)
	}
	if !errors.Is(err, errRecordMoved) || errors.Is(err, store.ErrAlreadyExists) {
		t.Errorf("Answer err = %v, want errRecordMoved and not the store's own ErrAlreadyExists", err)
	}
	if n := frpStore.count(); n != 0 {
		t.Errorf("responses stored = %d, want 0", n)
	}
	if _, gerr := answers.Get(context.Background(), "dev1", "frq1"); !errors.Is(gerr, store.ErrNotFound) {
		t.Errorf("answer record after the race: err %v, want none stored", gerr)
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

// vanishingAnswers replays the other side of the race: this attempt reads a
// racer's record, and the racer takes it back before this attempt's Update.
type vanishingAnswers struct {
	*memory.ScopedStore[AnswerRecord]
	vanished bool
}

func (s *vanishingAnswers) Update(ctx context.Context, parentID, id string, rec AnswerRecord) error {
	if !s.vanished {
		s.vanished = true
		if err := s.ScopedStore.Delete(ctx, parentID, id); err != nil {
			return err
		}
	}
	return s.ScopedStore.Update(ctx, parentID, id, rec)
}

func newVanishingAnswers(t *testing.T) *vanishingAnswers {
	t.Helper()
	s := &vanishingAnswers{ScopedStore: memory.NewScopedStore[AnswerRecord]()}
	racer := AnswerRecord{Action: ActionAnswer, By: Attribution{Kind: KindOperator, Principal: PrincipalAdminKey, At: 1}}
	if err := s.ScopedStore.Create(context.Background(), "dev1", "frq1", racer); err != nil {
		t.Fatal(err)
	}
	return s
}

// An answer record that vanished is not the request being gone: the
// operator's Answer must not read as store.ErrNotFound (the admin route's
// 404) while the request exists.
func TestAnswer_VanishedAnswerRecordIsNotTheRequestGone(t *testing.T) {
	frqStore := &stubFRQReader{}
	frqStore.put("dev1", "frq1", conflictRequest())
	frpStore := &stubFRPStore{}
	q := NewQueue(frqStore, frpStore, PermissiveGate{}, Config{Deadline: time.Hour}, nil)
	q.RecordTimers()
	t.Cleanup(q.Close)
	q.RecordAnswers(NewAnswers(newVanishingAnswers(t)))

	_, err := q.Answer(context.Background(), "dev1", "frq1", Decision{By: Attribution{Kind: KindOperator, At: 2}})
	if err == nil || errors.Is(err, store.ErrNotFound) || errors.Is(err, ErrAlreadyAnswered) {
		t.Fatalf("Answer err = %v, want a failure that is neither store.ErrNotFound nor ErrAlreadyAnswered", err)
	}
	if !errors.Is(err, errRecordMoved) {
		t.Errorf("Answer err = %v, want errRecordMoved", err)
	}
	if n := frpStore.count(); n != 0 {
		t.Errorf("responses stored = %d, want 0", n)
	}
}

// The fallback that finds its answer record vanished keeps a retry armed,
// and the retry answers the request once.
func TestFallback_VanishedAnswerRecordKeepsTheTimerAndAnswersOnce(t *testing.T) {
	frqStore := &stubFRQReader{}
	frqStore.put("dev1", "frq1", conflictRequest())
	frpStore := &stubFRPStore{}
	q := NewQueue(frqStore, frpStore, PermissiveGate{}, Config{Deadline: time.Hour, RetryBackoff: time.Second, RetryAttempts: 3}, nil)
	timers := q.RecordTimers()
	t.Cleanup(q.Close)
	answers := newVanishingAnswers(t)
	q.RecordAnswers(NewAnswers(answers))

	q.attemptFallback(context.Background(), "dev1", "frq1", 1)

	live := timers.Live()
	if n := frpStore.count(); n != 0 || len(live) != 1 || live[0].Delay != time.Second || q.GivenUpCount() != 0 {
		t.Fatalf("after the vanished record: responses %d, live timers %d, given up %d; want 0, one retry at 1s, 0", n, len(live), q.GivenUpCount())
	}
	live[0].Fire()
	if n := frpStore.count(); n != 1 {
		t.Fatalf("responses after the retry = %d, want exactly 1", n)
	}
	rec, err := answers.Get(context.Background(), "dev1", "frq1")
	if err != nil || rec.By.Kind != KindDeadlineFallback {
		t.Errorf("answer record = %+v (err %v), want the fallback's answer", rec, err)
	}
}

// notFoundTakeBack answers a take-back Delete with store.ErrNotFound.
type notFoundTakeBack struct {
	*memory.ScopedStore[AnswerRecord]
}

func (notFoundTakeBack) Delete(context.Context, string, string) error { return store.ErrNotFound }

// A take-back that finds no record, after a response Create that failed for
// another reason, is not the request being gone either.
func TestFallback_TakeBackNotFoundAfterAFailedCreateKeepsTheTimer(t *testing.T) {
	frqStore := &stubFRQReader{}
	frqStore.put("dev1", "frq1", conflictRequest())
	frpStore := &stubFRPStore{failCreateTimes: 1}
	q := NewQueue(frqStore, frpStore, PermissiveGate{}, Config{Deadline: time.Hour, RetryBackoff: time.Second, RetryAttempts: 3}, nil)
	timers := q.RecordTimers()
	t.Cleanup(q.Close)
	q.RecordAnswers(NewAnswers(notFoundTakeBack{memory.NewScopedStore[AnswerRecord]()}))

	q.attemptFallback(context.Background(), "dev1", "frq1", 1)

	live := timers.Live()
	if len(live) != 1 || q.PendingTimers() != 1 || q.GivenUpCount() != 0 {
		t.Fatalf("live timers %d, pending %d, given up %d; want one retry armed", len(live), q.PendingTimers(), q.GivenUpCount())
	}
	live[0].Fire()
	if n := frpStore.count(); n != 1 {
		t.Errorf("responses after the retry = %d, want exactly 1", n)
	}
}

func captureLog(t *testing.T) func() string {
	t.Helper()
	var buf bytes.Buffer
	var mu sync.Mutex
	prev := log.Writer()
	log.SetOutput(writerFunc(func(p []byte) (int, error) { mu.Lock(); defer mu.Unlock(); return buf.Write(p) }))
	t.Cleanup(func() { log.SetOutput(prev) })
	return func() string { mu.Lock(); defer mu.Unlock(); return buf.String() }
}

// The fallback that finds the request answered still logs, at ERROR, an
// answer record it could not take back, naming who that record wrongly says
// answered.
func TestFallback_LogsAFailedTakeBackAtError(t *testing.T) {
	logged := captureLog(t)
	frqStore := &stubFRQReader{}
	frqStore.put("dev1", "frq1", conflictRequest())
	q := NewQueue(frqStore, &stubFRPStore{forceAlreadyExists: true}, PermissiveGate{}, Config{Deadline: time.Hour}, nil)
	q.RecordTimers()
	t.Cleanup(q.Close)
	q.RecordAnswers(NewAnswers(&failingTakeBack{ScopedStore: memory.NewScopedStore[AnswerRecord](), err: errors.New("boom: delete")}))

	q.attemptFallback(context.Background(), "dev1", "frq1", 1)

	got := logged()
	for _, want := range []string{"ERROR", "cause=answer_record_take_back", "dev1/frq1", "deadline_fallback", "boom: delete"} {
		if !strings.Contains(got, want) {
			t.Errorf("log = %q, want it to contain %q", got, want)
		}
	}
	if q.PendingTimers() != 0 {
		t.Errorf("pending timers = %d, want 0: a response exists", q.PendingTimers())
	}
}

// A duplicate response whose take-back works is plain ErrAlreadyAnswered:
// the store's own ErrAlreadyExists and the take-back mark stay out of it,
// and the attempt's record is gone.
func TestAnswer_DuplicateWithAWorkingTakeBackIsPlainAlreadyAnswered(t *testing.T) {
	frqStore := &stubFRQReader{}
	frqStore.put("dev1", "frq1", conflictRequest())
	q := NewQueue(frqStore, &stubFRPStore{forceAlreadyExists: true}, PermissiveGate{}, Config{Deadline: time.Hour}, nil)
	q.RecordTimers()
	t.Cleanup(q.Close)
	answers := memory.NewScopedStore[AnswerRecord]()
	q.RecordAnswers(NewAnswers(answers))

	_, err := q.Answer(context.Background(), "dev1", "frq1", Decision{By: Attribution{Kind: KindOperator, At: 2}})
	if !errors.Is(err, ErrAlreadyAnswered) || errors.Is(err, store.ErrAlreadyExists) || errors.Is(err, ErrAnswerRecordTakeBack) {
		t.Errorf("Answer err = %v, want ErrAlreadyAnswered alone", err)
	}
	if _, gerr := answers.Get(context.Background(), "dev1", "frq1"); !errors.Is(gerr, store.ErrNotFound) {
		t.Errorf("answer record after the take-back: err %v, want none stored", gerr)
	}
}

// restoreFailing fails every Update after the first, so the Update undo of a
// replaced stale record cannot put the old one back.
type restoreFailing struct {
	*memory.ScopedStore[AnswerRecord]
	updates int
	err     error
}

func (s *restoreFailing) Update(ctx context.Context, parentID, id string, rec AnswerRecord) error {
	s.updates++
	if s.updates > 1 && s.err != nil {
		return s.err
	}
	return s.ScopedStore.Update(ctx, parentID, id, rec)
}

// A stale record replaced by this attempt is put back when the response
// already exists, and a failure to put it back reaches the caller.
func TestAnswer_DuplicateRestoresAReplacedStaleRecord(t *testing.T) {
	stale := AnswerRecord{Action: ActionAnswer, By: Attribution{Kind: KindRecovery, At: 7}}
	errRestore := errors.New("boom: restore answer record")
	for _, tc := range []struct {
		name    string
		failErr error
	}{
		{"restore works", nil},
		{"restore fails", errRestore},
	} {
		t.Run(tc.name, func(t *testing.T) {
			frqStore := &stubFRQReader{}
			frqStore.put("dev1", "frq1", conflictRequest())
			q := NewQueue(frqStore, &stubFRPStore{forceAlreadyExists: true}, PermissiveGate{}, Config{Deadline: time.Hour}, nil)
			q.RecordTimers()
			t.Cleanup(q.Close)
			answers := &restoreFailing{ScopedStore: memory.NewScopedStore[AnswerRecord](), err: tc.failErr}
			if err := answers.ScopedStore.Create(context.Background(), "dev1", "frq1", stale); err != nil {
				t.Fatal(err)
			}
			q.RecordAnswers(NewAnswers(answers))

			_, err := q.Answer(context.Background(), "dev1", "frq1", Decision{By: Attribution{Kind: KindOperator, At: 2}})
			if !errors.Is(err, ErrAlreadyAnswered) {
				t.Fatalf("Answer err = %v, want ErrAlreadyAnswered", err)
			}
			rec, gerr := answers.Get(context.Background(), "dev1", "frq1")
			if gerr != nil {
				t.Fatal(gerr)
			}
			if tc.failErr == nil {
				if rec != stale || errors.Is(err, ErrAnswerRecordTakeBack) {
					t.Errorf("record %+v err %v, want the stale record put back and no take-back failure", rec, err)
				}
				return
			}
			if !errors.Is(err, ErrAnswerRecordTakeBack) || !errors.Is(err, errRestore) || rec.By.Kind != KindOperator {
				t.Errorf("record %+v err %v, want the take-back failure surfaced and the operator's record left", rec, err)
			}
		})
	}
}
