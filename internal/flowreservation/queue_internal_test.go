package flowreservation

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
)

// stubFRQReader answers Get from a fixed map, for the one internal test
// that needs to force newFRPMRID's failure (frpRandRead is unexported, so
// this test lives in package flowreservation rather than the external
// queue_test.go).
type stubFRQReader map[string]sep2.FlowReservationRequest

func (s stubFRQReader) Get(_ context.Context, parentID, id string) (sep2.FlowReservationRequest, error) {
	frq, ok := s[parentID+"/"+id]
	if !ok {
		return sep2.FlowReservationRequest{}, errors.New("not found")
	}
	return frq, nil
}

type stubFRPCreator struct {
	created []sep2.FlowReservationResponse
}

func (s *stubFRPCreator) Create(_ context.Context, _, _ string, resource sep2.FlowReservationResponse) error {
	s.created = append(s.created, resource)
	return nil
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
	frqStore := stubFRQReader{"dev1/frq1": frq}
	frpStore := &stubFRPCreator{}
	q := NewQueue(frqStore, frpStore, PermissiveCommitmentChecker{}, Config{Deadline: time.Hour}, nil)
	t.Cleanup(q.Close)

	frpRandRead = func(b []byte) (int, error) { return 0, errors.New("boom") }
	if _, err := q.Answer(context.Background(), "dev1", "frq1", Decision{}); err == nil {
		t.Fatal("Answer with a failing mint returned nil error, want the mint failure")
	}
	if len(frpStore.created) != 0 {
		t.Fatalf("responses created after a mint failure = %d, want 0", len(frpStore.created))
	}

	frpRandRead = orig
	frp, err := q.Answer(context.Background(), "dev1", "frq1", Decision{})
	if err != nil {
		t.Fatalf("retry Answer after mint recovers: %v", err)
	}
	if len(frpStore.created) != 1 {
		t.Fatalf("responses created after the retry = %d, want 1", len(frpStore.created))
	}
	if frp.MRID == "" {
		t.Error("retried Answer returned an empty MRID")
	}
}
