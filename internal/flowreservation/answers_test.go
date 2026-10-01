package flowreservation_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/commitment"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/flowreservation"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

// Tests for GRIDAPPSD/ieee-2030_5-server-go#670: who answered and who
// cancelled each response, recorded beside it.

var operator = flowreservation.Attribution{Kind: flowreservation.KindOperator, Admission: "bearer", Principal: flowreservation.PrincipalAdminKey, At: 1790000000}

func recordingAnswers(f *cancelFixture) *memory.ScopedStore[flowreservation.AnswerRecord] {
	s := memory.NewScopedStore[flowreservation.AnswerRecord]()
	f.queue.RecordAnswers(flowreservation.NewAnswers(s))
	return s
}

func answerRecord(t *testing.T, s *memory.ScopedStore[flowreservation.AnswerRecord], id string) (flowreservation.AnswerRecord, bool) {
	t.Helper()
	rec, err := s.Get(context.Background(), aggID, id)
	if errors.Is(err, store.ErrNotFound) {
		return rec, false
	}
	must(t, err)
	return rec, true
}

// intentCheckingFRP fails the test if a response is created before its
// answer record exists.
type intentCheckingFRP struct {
	*memory.ScopedStore[sep2.FlowReservationResponse]
	t       *testing.T
	answers *memory.ScopedStore[flowreservation.AnswerRecord]
	seen    []flowreservation.AnswerRecord
}

func (s *intentCheckingFRP) Create(ctx context.Context, parentID, id string, frp sep2.FlowReservationResponse) error {
	rec, err := s.answers.Get(ctx, parentID, id)
	if err != nil {
		s.t.Errorf("response %s/%s created with no answer record: %v", parentID, id, err)
	}
	s.seen = append(s.seen, rec)
	return s.ScopedStore.Create(ctx, parentID, id, frp)
}

func TestAnswers_IntentIsWrittenBeforeTheResponse(t *testing.T) {
	t.Parallel()
	f := newCancelFixture(t, flowreservation.Config{Deadline: time.Hour})
	answers := memory.NewScopedStore[flowreservation.AnswerRecord]()
	frp := &intentCheckingFRP{ScopedStore: f.frp, t: t, answers: answers}
	q := f.queueOver(t, frp, flowreservation.Config{Deadline: time.Hour})
	q.RecordAnswers(flowreservation.NewAnswers(answers))
	storeRequest(t, f.frq, aggID, "R1", windowRequest("REQ-1", time.Now().Add(time.Hour).Unix(), 600, 100))

	resp, err := q.Answer(context.Background(), aggID, "R1", flowreservation.Decision{By: operator})
	must(t, err)
	if len(frp.seen) != 1 || frp.seen[0].By != operator || frp.seen[0].Action != flowreservation.ActionAnswer {
		t.Fatalf("record seen at the response's Create = %+v, want one answer by the operator", frp.seen)
	}
	rec, ok := answerRecord(t, answers, "R1")
	if !ok || rec.By != operator || rec.CancelledBy != nil {
		t.Errorf("stored record = %+v (present %v), want the operator's answer", rec, ok)
	}
	if resp.MRID == "" {
		t.Error("no response returned")
	}
}

// A zero At takes the response's creationTime, so a record always says when.
func TestAnswers_ZeroAtTakesTheCreationTime(t *testing.T) {
	t.Parallel()
	f := newCancelFixture(t, flowreservation.Config{Deadline: time.Hour})
	answers := recordingAnswers(f)
	storeRequest(t, f.frq, aggID, "R1", windowRequest("REQ-1", time.Now().Add(time.Hour).Unix(), 600, 100))
	resp, err := f.queue.Answer(context.Background(), aggID, "R1", flowreservation.Decision{Kind: flowreservation.Deny, By: flowreservation.Attribution{Kind: flowreservation.KindOperator}})
	must(t, err)
	if rec, _ := answerRecord(t, answers, "R1"); rec.By.At != resp.CreationTime {
		t.Errorf("record At = %d, want the response's creationTime %d", rec.By.At, resp.CreationTime)
	}
}

func TestAnswers_RefusedGrantLeavesTheAnswerStoreUnchanged(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newCancelFixture(t, flowreservation.Config{Deadline: time.Hour})
	answers := recordingAnswers(f)
	base := time.Now().Add(time.Hour).Unix()
	storeRequest(t, f.frq, aggID, "R1", windowRequest("REQ-1", base, 600, 100))
	storeRequest(t, f.frq, aggID, "R2", windowRequest("REQ-2", base, 600, 100))
	_, err := f.queue.Answer(ctx, aggID, "R1", flowreservation.Decision{By: operator})
	must(t, err)
	// An earlier record left under R2 by a crash must survive the refusal too.
	orphan := flowreservation.AnswerRecord{Action: flowreservation.ActionAnswer, By: flowreservation.Attribution{Kind: flowreservation.KindDeadlineFallback, At: 5}}
	must(t, answers.Create(ctx, aggID, "R2", orphan))

	_, err = f.queue.Answer(ctx, aggID, "R2", flowreservation.Decision{By: operator})
	var conflict *commitment.ConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("second grant err = %v, want a window conflict", err)
	}
	if rec, ok := answerRecord(t, answers, "R2"); !ok || rec != orphan {
		t.Errorf("R2 record after the refusal = %+v (present %v), want the earlier record %+v untouched", rec, ok, orphan)
	}
}

// failingCreateFRP refuses every Create, as a failed snapshot write does.
type failingCreateFRP struct {
	*memory.ScopedStore[sep2.FlowReservationResponse]
}

func (failingCreateFRP) Create(context.Context, string, string, sep2.FlowReservationResponse) error {
	return errors.New("disk full")
}

func TestAnswers_FailedResponseCreateTakesTheRecordBack(t *testing.T) {
	t.Parallel()
	f := newCancelFixture(t, flowreservation.Config{Deadline: time.Hour})
	answers := memory.NewScopedStore[flowreservation.AnswerRecord]()
	q := f.queueOver(t, failingCreateFRP{f.frp}, flowreservation.Config{Deadline: time.Hour})
	q.RecordAnswers(flowreservation.NewAnswers(answers))
	storeRequest(t, f.frq, aggID, "R1", windowRequest("REQ-1", time.Now().Add(time.Hour).Unix(), 600, 100))
	if _, err := q.Answer(context.Background(), aggID, "R1", flowreservation.Decision{Kind: flowreservation.Deny, By: operator}); err == nil {
		t.Fatal("answer with a failing response store succeeded")
	}
	if rec, ok := answerRecord(t, answers, "R1"); ok {
		t.Errorf("record %+v outlived its failed response", rec)
	}
}

func TestAnswers_FallbackRecordsItself(t *testing.T) {
	t.Parallel()
	f := newCancelFixture(t, flowreservation.Config{Deadline: time.Hour})
	answers := recordingAnswers(f)
	timers := f.queue.RecordTimers()
	req := windowRequest("REQ-1", time.Now().Add(time.Hour).Unix(), 600, 100)
	storeRequest(t, f.frq, aggID, "R1", req)
	f.queue.Submit(aggID, "R1", req, time.Now().Unix())
	live := timers.Live()
	if len(live) != 1 {
		t.Fatalf("timers = %d, want 1", len(live))
	}
	live[0].Fire()
	rec, ok := answerRecord(t, answers, "R1")
	if !ok || rec.By.Kind != flowreservation.KindDeadlineFallback || rec.By.Admission != "" || rec.By.Principal != "" || rec.By.At == 0 {
		t.Errorf("fallback record = %+v (present %v), want kind deadline_fallback with a time and no operator", rec, ok)
	}
}

func TestAnswers_ClientCancelRecordsTheClient(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newCancelFixture(t, flowreservation.Config{Deadline: time.Hour})
	answers := recordingAnswers(f)
	base := time.Now().Add(time.Hour).Unix()

	storeRequest(t, f.frq, aggID, "P1", windowRequest("REQ-P", base, 600, 100))
	must(t, f.canceller.Cancel(ctx, aggID, "P1", cancelledStatus(time.Now().Unix())))
	if rec, ok := answerRecord(t, answers, "P1"); !ok || rec.By.Kind != flowreservation.KindClient || rec.CancelledBy != nil {
		t.Errorf("pending cancel record = %+v (present %v), want the denial answered by the client", rec, ok)
	}

	grant := f.answered(t, base+3600)
	f.issueExec(t, grant, base+3600, -2000)
	must(t, f.canceller.Cancel(ctx, aggID, "R1", cancelledStatus(time.Now().Unix())))
	rec, ok := answerRecord(t, answers, "R1")
	if !ok || rec.CancelledBy == nil || rec.CancelledBy.Kind != flowreservation.KindClient || rec.CancelledBy.At == 0 {
		t.Errorf("answered cancel record = %+v (present %v), want cancelled by the client", rec, ok)
	}
}

func TestAnswers_RecoverRecordsRecovery(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newCancelFixture(t, flowreservation.Config{Deadline: hold})
	answers := recordingAnswers(f)
	f.queue.RecordTimers()
	base := time.Now().Add(time.Hour).Unix()

	f.pendingRequest(t, "P1", time.Now().Unix()-50, windowRequest("REQ-P", base, 600, 100))
	f.cancelRequest(t, "P1", time.Now().Unix())

	grant := f.answered(t, base+3600)
	frq := f.storedRequest(t, "R1")
	frq.Href = "/edev/" + aggID + "/frq/R1"
	must(t, f.frq.Update(ctx, aggID, "R1", frq))
	f.issueExec(t, grant, base+3600, -2000)
	f.cancelRequest(t, "R1", time.Now().Unix())

	now := time.Now()
	_, err := flowreservation.Recover(ctx, f.recoverDeps(), now)
	must(t, err)
	if rec, ok := answerRecord(t, answers, "P1"); !ok || rec.By.Kind != flowreservation.KindRecovery || rec.By.At != now.Unix() {
		t.Errorf("recovered denial record = %+v (present %v), want answered by recovery at %d", rec, ok, now.Unix())
	}
	if rec, ok := answerRecord(t, answers, "R1"); !ok || rec.CancelledBy == nil || rec.CancelledBy.Kind != flowreservation.KindRecovery {
		t.Errorf("recovered cancel record = %+v (present %v), want cancelled by recovery", rec, ok)
	}
}

func TestAnswers_RevisionRecordsItsAuthorAndARefusedOneRecordsNothing(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newCancelFixture(t, flowreservation.Config{Deadline: time.Hour})
	answers := recordingAnswers(f)
	base := time.Now().Add(time.Hour).Unix()
	grant := f.answered(t, base)
	f.issueExec(t, grant, base, -2000)
	deps := f.reviseDeps()
	deps.Answers = f.queue.Answers()

	if _, err := flowreservation.Revise(ctx, deps, aggID, "R1", shorten(base+1800, 1800), "", operator, time.Now()); err == nil {
		t.Fatal("a revision the execution no longer fits succeeded")
	}
	if rec, ok := answerRecord(t, answers, "R1-r1"); ok {
		t.Errorf("refused revision left record %+v", rec)
	}

	_, err := flowreservation.Revise(ctx, deps, aggID, "R1", shorten(base, 1800), "", operator, time.Now())
	must(t, err)
	rec, ok := answerRecord(t, answers, "R1-r1")
	if !ok || rec.Action != flowreservation.ActionRevise || rec.By != operator {
		t.Errorf("revision record = %+v (present %v), want revise by the operator", rec, ok)
	}
}

// The undo of a revision deletes the response and puts its key's record back.
func TestAnswers_RevisionUndoTakesTheRecordBack(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newCancelFixture(t, flowreservation.Config{Deadline: time.Hour})
	answers := recordingAnswers(f)
	base := time.Now().Add(time.Hour).Unix()
	f.answered(t, base)
	deps := f.reviseDeps()
	deps.Answers = f.queue.Answers()
	deps.Writers.Grants = failingGrants{}

	if _, err := flowreservation.Revise(ctx, deps, aggID, "R1", shorten(base, 1800), "", operator, time.Now()); err == nil {
		t.Fatal("revision with a failing grant writer succeeded")
	}
	if _, err := f.frp.Get(ctx, aggID, "R1-r1"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("revision response after the undo: err = %v, want not found", err)
	}
	if rec, ok := answerRecord(t, answers, "R1-r1"); ok {
		t.Errorf("record %+v outlived its undone revision", rec)
	}
}

type failingGrants struct{}

func (failingGrants) MarkCancelled(context.Context, commitment.Grant, string, int64) error {
	return errors.New("lifecycle store down")
}

func TestAnswers_AttributionsOfReadsBothSides(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := memory.NewScopedStore[flowreservation.AnswerRecord]()
	a := flowreservation.NewAnswers(s)
	if by, cancelled, err := a.AttributionsOf(ctx, aggID, "none"); by != nil || cancelled != nil || err != nil {
		t.Errorf("absent record = %v %v %v, want nothing recorded", by, cancelled, err)
	}
	must(t, s.Create(ctx, aggID, "R1", flowreservation.AnswerRecord{Action: flowreservation.ActionAnswer, By: operator}))
	client := flowreservation.Attribution{Kind: flowreservation.KindClient, At: 9}
	must(t, a.RecordCancel(ctx, aggID, "R1", client))
	by, cancelled, err := a.AttributionsOf(ctx, aggID, "R1")
	must(t, err)
	if by == nil || *by != operator || cancelled == nil || *cancelled != client {
		t.Errorf("attributions = %v %v, want %v and %v", by, cancelled, operator, client)
	}
	// A cancel recorded on a response with no record carries the cancel only.
	must(t, a.RecordCancel(ctx, aggID, "R2", client))
	by, cancelled, err = a.AttributionsOf(ctx, aggID, "R2")
	must(t, err)
	if by != nil || cancelled == nil || *cancelled != client {
		t.Errorf("cancel-only record = %v %v, want no author and the client's cancel", by, cancelled)
	}
	var none *flowreservation.Answers
	if by, cancelled, err := none.AttributionsOf(ctx, aggID, "R1"); by != nil || cancelled != nil || err != nil {
		t.Errorf("nil Answers = %v %v %v, want nothing", by, cancelled, err)
	}
}

// After a crash between an intent and its response, the pending request is
// re-armed at startup and its fallback answer replaces the orphan record, so
// the answer reads as the fallback's and not as the crashed writer's.
func TestAnswers_OrphanRecordIsReplacedByTheAnswerAfterRecover(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newCancelFixture(t, flowreservation.Config{Deadline: hold})
	answers := recordingAnswers(f)
	timers := f.queue.RecordTimers()
	f.pendingRequest(t, "R1", time.Now().Unix()-50, windowRequest("REQ-1", time.Now().Add(time.Hour).Unix(), 600, 100))
	must(t, answers.Create(ctx, aggID, "R1", flowreservation.AnswerRecord{Action: flowreservation.ActionAnswer, By: operator}))

	counts, err := flowreservation.Recover(ctx, f.recoverDeps(), time.Now())
	must(t, err)
	live := timers.Live()
	if counts.Rearmed != 1 || len(live) != 1 {
		t.Fatalf("counts %+v timers %d, want the request re-armed", counts, len(live))
	}
	live[0].Fire()
	by, cancelled, err := f.queue.Answers().AttributionsOf(ctx, aggID, "R1")
	must(t, err)
	if by == nil || by.Kind != flowreservation.KindDeadlineFallback || cancelled != nil {
		t.Errorf("after recover and the fallback: answered %+v cancelled %+v, want the fallback and no cancel", by, cancelled)
	}
}

// fixedGrants is a GrantSource that knows every grant by mRID whatever its
// href, the way a ledger over another store would.
type fixedGrants map[string]commitment.Grant

func (g fixedGrants) GrantsInFleet(context.Context, string) ([]commitment.Grant, error) {
	var out []commitment.Grant
	for _, gr := range g {
		out = append(out, gr)
	}
	return out, nil
}

func (g fixedGrants) Grant(_ context.Context, mrid string) (commitment.Grant, error) {
	if gr, ok := g[mrid]; ok {
		return gr, nil
	}
	return commitment.Grant{}, commitment.ErrNoGrant
}

type noControls struct{}

func (noControls) ControlsInFleet(context.Context, string) ([]commitment.Control, error) {
	return nil, nil
}
func (noControls) ExecutionsOf(context.Context, string) ([]commitment.Control, error) {
	return nil, nil
}
func (noControls) ControlsAndExecutions(context.Context, string, []string) ([]commitment.Control, map[string][]commitment.Control, error) {
	return nil, nil, nil
}

type markingGrants struct{ marked []string }

func (m *markingGrants) MarkCancelled(_ context.Context, g commitment.Grant, _ string, _ int64) error {
	m.marked = append(m.marked, g.MRID)
	return nil
}

type noExecutions struct{}

func (noExecutions) CancelExecution(context.Context, commitment.Control, string) error { return nil }
func (noExecutions) RelinkExecution(context.Context, commitment.Control, string) error { return nil }

// A chain member's store id comes from the walk that read it, not from its
// href, so one whose href does not parse is still cancelled and reported
// with the id its record is kept under.
func TestCancelGrants_IdComesFromTheChainWalkNotTheHref(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newCancelFixture(t, flowreservation.Config{Deadline: time.Hour})
	w := &commitment.Window{Start: time.Now().Add(time.Hour).Unix(), Duration: 600}
	frp := sep2.FlowReservationResponse{Subject: "REQ-1"}
	frp.Interval = &sep2.DateTimeInterval{Start: w.Start, Duration: w.Duration}
	frp.MRID, frp.Href = "GOOD", "/edev/"+aggID+"/frp/R1"
	must(t, f.frp.Create(ctx, aggID, "R1", frp))
	frp.MRID, frp.Href = "BAD-HREF", "not an href"
	must(t, f.frp.Create(ctx, aggID, "R1-r1", frp))
	grants := &markingGrants{}
	ledger := commitment.NewLedger(fixedGrants{
		"GOOD":     {MRID: "GOOD", FleetKey: aggLFDI, Window: w},
		"BAD-HREF": {MRID: "BAD-HREF", FleetKey: aggLFDI, Window: w},
	}, noControls{})
	c := flowreservation.NewCanceller(f.frq, f.frp, f.queue, ledger, commitment.Writers{Executions: noExecutions{}, Grants: grants})

	got, err := c.CancelGrants(ctx, aggID, "R1", "operator stop")
	must(t, err)
	want := []flowreservation.CancelledGrant{{ID: "R1", MRID: "GOOD"}, {ID: "R1-r1", MRID: "BAD-HREF"}}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("cancelled = %+v, want %+v", got, want)
	}
	if len(grants.marked) != 2 {
		t.Errorf("grants marked = %v, want both", grants.marked)
	}
}
