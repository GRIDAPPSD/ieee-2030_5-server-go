package server

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/flowreservation"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/assembly"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/handlers/sep2time"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

// #670: answer records persist like the responses they describe, and the
// EndDevice delete removes them with the device's responses.
func TestFlowReservationAnswers_SurviveARestartAndGoWithTheirEndDevice(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	fx := newFRPFixture(sep2time.Now().Unix() + 3600)
	rec := flowreservation.AnswerRecord{
		Action:      flowreservation.ActionAnswer,
		By:          flowreservation.Attribution{Kind: flowreservation.KindOperator, Admission: "mtls", Principal: "cert:ab", At: 100},
		CancelledBy: &flowreservation.Attribution{Kind: flowreservation.KindClient, At: 200},
	}

	boot1, _ := bootFRP(t, dir)
	seedFRPDevice(t, boot1)
	fx.seed(t, boot1)
	if err := boot1.FlowReservationAnswers.Create(ctx, frpDevice, "frq-3", rec); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "flowreservation-answers.json")); err != nil {
		t.Fatalf("answer snapshot after the write: %v", err)
	}

	boot2, core2 := bootFRP(t, dir)
	got, err := boot2.FlowReservationAnswers.Get(ctx, frpDevice, "frq-3")
	if err != nil || !reflect.DeepEqual(got, rec) {
		t.Fatalf("answer record after a restart = %+v, %v, want %+v", got, err, rec)
	}
	if h := newAdminFlowReservationHandler(boot2); h == nil || !h.Persisted || h.Attributions == nil {
		t.Errorf("handler over persistent stores: %+v, want Persisted with attributions wired", h)
	}

	if code := deleteDevice(t, core2); code/100 != 2 {
		t.Fatalf("DELETE /edev/%s = %d, want 2xx", frpDevice, code)
	}
	if n, err := boot2.FlowReservationAnswers.Count(ctx, frpDevice); err != nil || n != 0 {
		t.Errorf("answer records under %s after the delete = %d, %v, want 0", frpDevice, n, err)
	}
	boot3, _ := bootFRP(t, dir)
	if n, err := boot3.FlowReservationAnswers.Count(ctx, frpDevice); err != nil || n != 0 {
		t.Errorf("answer records under %s after a restart = %d, %v, want 0", frpDevice, n, err)
	}
}

// The queue the server builds records each answer's author in the answer
// store when one is wired.
func TestFlowReservationQueueRecordsAnswersInTheServersStore(t *testing.T) {
	s := fullyWiredFlowReservationStores()
	s.FlowReservationAnswers = memory.NewScopedStore[flowreservation.AnswerRecord]()
	q := newFlowReservationQueue(s, nil)
	t.Cleanup(q.Close)
	if q.Answers() == nil {
		t.Fatal("queue built with an answer store records nothing")
	}
	s.FlowReservationAnswers = nil
	q2 := newFlowReservationQueue(s, nil)
	t.Cleanup(q2.Close)
	if q2.Answers() != nil {
		t.Error("queue built without an answer store records into one")
	}
}

// Each write is wired only with what it writes through, and is otherwise left
// unset so it answers 503: answer needs the queue, revise and cancel the
// ledger and the DER control issuer too.
func TestNewAdminFlowReservationHandler_WiresWritesOnlyWithTheirDependencies(t *testing.T) {
	s := fullyWiredFlowReservationStores()
	h := newAdminFlowReservationHandler(s)
	if h.Queue != nil || h.Canceller != nil || h.Revise.Ledger != nil {
		t.Errorf("no queue: Queue=%v Canceller=%v Revise.Ledger=%v, want all unset", h.Queue, h.Canceller, h.Revise.Ledger)
	}

	s.FlowReservationQueue = newFlowReservationQueue(s, nil)
	t.Cleanup(s.FlowReservationQueue.Close)
	h = newAdminFlowReservationHandler(s)
	if h.Queue == nil || h.Canceller != nil || h.Revise.Ledger != nil {
		t.Errorf("queue only: Queue=%v Canceller=%v Revise.Ledger=%v, want only Queue", h.Queue, h.Canceller, h.Revise.Ledger)
	}

	s.DERPrograms = memory.NewDERProgramStore()
	s.CommitmentLedger = NewCommitmentLedger(s)
	issuer, err := assembly.NewDERControlIssuer(s.DERPrograms, s.DERControls, s.DERControlLifecycles, nil)
	if err != nil {
		t.Fatal(err)
	}
	s.DERControlIssuer = issuer
	s.FlowReservationAnswers = memory.NewScopedStore[flowreservation.AnswerRecord]()
	s.FlowReservationQueue = newFlowReservationQueue(s, nil)
	t.Cleanup(s.FlowReservationQueue.Close)
	h = newAdminFlowReservationHandler(s)
	if h.Queue == nil || h.Canceller == nil || h.Revise.Ledger == nil || h.Revise.FRP == nil || h.Revise.Replace == nil {
		t.Fatalf("fully wired: Queue=%v Canceller=%v Revise=%+v, want every write wired", h.Queue, h.Canceller, h.Revise)
	}
	if h.Revise.Answers == nil || h.Revise.Answers != s.FlowReservationQueue.Answers() || h.CancelRecorder == nil || h.Attributions == nil {
		t.Errorf("answer records: Revise.Answers=%v queue=%v CancelRecorder=%v Attributions=%v, want one store for all",
			h.Revise.Answers, s.FlowReservationQueue.Answers(), h.CancelRecorder, h.Attributions)
	}
}
