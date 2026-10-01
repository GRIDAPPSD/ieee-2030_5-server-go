package flowreservation_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/commitment"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/commitment/sources"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/dercontrol"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/flowreservation"
)

// Tests for GRIDAPPSD/ieee-2030_5-server-go#667: a client's cancel against
// the real queue, commitment ledger and DER control issuer.

type cancelFixture struct {
	*ledgerFixture
	issuer    *dercontrol.Issuer
	canceller *flowreservation.Canceller
}

func newCancelFixture(t *testing.T, cfg flowreservation.Config) *cancelFixture {
	t.Helper()
	f := newLedgerFixture(t, cfg)
	must(t, f.programs.Create(context.Background(), managedID, "derp1", sep2.DERProgram{
		DERControlListLink: &sep2.ListLink{Href: "/edev/" + managedID + "/fsa/fsa1/derp/derp1/derc"},
	}))
	pen := uint32(0x40732001)
	issuer, err := dercontrol.NewIssuer(f.programs, f.controls, f.controlLifecycles, dercontrol.Config{PEN: &pen})
	must(t, err)
	return &cancelFixture{
		ledgerFixture: f,
		issuer:        issuer,
		canceller:     flowreservation.NewCanceller(f.frq, f.frp, f.queue, f.ledger, sources.NewWriters(issuer, f.frpLifecycles)),
	}
}

func cancelledStatus(at int64) sep2.RequestStatus {
	return sep2.RequestStatus{DateTime: at, RequestStatus: sep2.RequestStatusCancelled}
}

func (f *cancelFixture) storedRequest(t *testing.T, id string) sep2.FlowReservationRequest {
	t.Helper()
	frq, err := f.frq.Get(context.Background(), aggID, id)
	must(t, err)
	return frq
}

func TestCancel_PendingWritesOneZeroDurationResponseAndLeavesTheQueue(t *testing.T) {
	t.Parallel()
	f := newCancelFixture(t, flowreservation.Config{Deadline: 40 * time.Millisecond})
	start := time.Now().Add(time.Hour).Unix()
	req := windowRequest("REQ-PENDING", start, 600, 10000)
	storeRequest(t, f.frq, aggID, "R1", req)
	f.queue.Submit(aggID, "R1", req, time.Now().Unix())
	if got := f.queue.PendingTimers(); got != 1 {
		t.Fatalf("PendingTimers before cancel = %d, want 1", got)
	}

	at := time.Now().Unix()
	must(t, f.canceller.Cancel(context.Background(), aggID, "R1", cancelledStatus(at)))

	if got := f.queue.PendingTimers(); got != 0 {
		t.Errorf("PendingTimers after cancel = %d, want 0: the request is still held for the deadline", got)
	}
	if frq := f.storedRequest(t, "R1"); frq.RequestStatus != cancelledStatus(at) {
		t.Errorf("stored RequestStatus = %+v, want %+v", frq.RequestStatus, cancelledStatus(at))
	}

	// The hold's own deadline passes: it must not add a second response.
	time.Sleep(150 * time.Millisecond)
	resps := f.responses(t, aggID)
	if len(resps) != 1 {
		t.Fatalf("responses = %d, want exactly 1", len(resps))
	}
	frp := resps[0]
	if frp.Interval == nil || frp.Interval.Duration != 0 || frp.Interval.Start != start {
		t.Errorf("interval = %+v, want duration 0 at the requested start %d", frp.Interval, start)
	}
	if frp.EnergyAvailable == nil || frp.EnergyAvailable.Value != 0 || frp.PowerAvailable == nil || frp.PowerAvailable.Value != 0 {
		t.Errorf("available energy %+v power %+v, want both zero", frp.EnergyAvailable, frp.PowerAvailable)
	}
	if frp.Subject != "REQ-PENDING" {
		t.Errorf("subject = %q, want the request's mRID", frp.Subject)
	}

	// Repeating the cancel changes nothing.
	must(t, f.canceller.Cancel(context.Background(), aggID, "R1", cancelledStatus(at+5)))
	if n := len(f.responses(t, aggID)); n != 1 {
		t.Errorf("responses after a repeated cancel = %d, want 1", n)
	}
	if frq := f.storedRequest(t, "R1"); frq.RequestStatus.DateTime != at {
		t.Errorf("repeated cancel moved the status dateTime to %d, want %d", frq.RequestStatus.DateTime, at)
	}
}

func TestCancel_AnsweredCancelsTheResponseAndItsExecutions(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newCancelFixture(t, flowreservation.Config{Deadline: time.Hour})
	base := time.Now().Add(time.Hour).Unix()
	storeRequest(t, f.frq, aggID, "R1", windowRequest("REQ-ANSWERED", base, 3600, 10000))
	grant, err := f.queue.Answer(ctx, aggID, "R1", flowreservation.Decision{})
	must(t, err)
	if grant.Interval == nil || grant.Interval.Duration != 3600 {
		t.Fatalf("setup: grant interval = %+v, want duration 3600", grant.Interval)
	}

	var execs []dercontrol.Result
	for i, start := range []int64{base, base + 600} {
		target := int16(-2000 - 1000*i)
		req := dercontrol.CreateRequest{
			DERProgramHref:  "/edev/" + managedID + "/fsa/fsa1/derp/derp1",
			Type:            dercontrol.TargetW,
			TargetW:         &sep2.ActivePower{Value: target},
			Start:           &start,
			DurationSeconds: 600,
			ExecutesGrant:   grant.MRID,
		}
		var res dercontrol.Result
		must(t, f.ledger.Within(ctx, []string{aggLFDI}, func(v commitment.View) error {
			var err error
			res, err = f.issuer.IssueInFleet(ctx, req, dercontrol.Fleet{Key: aggLFDI, Reach: 1, Check: viewCheck(v)})
			return err
		}))
		execs = append(execs, res)
	}

	at := time.Now().Unix()
	must(t, f.canceller.Cancel(ctx, aggID, "R1", cancelledStatus(at)))

	if frq := f.storedRequest(t, "R1"); frq.RequestStatus != cancelledStatus(at) {
		t.Errorf("stored RequestStatus = %+v, want %+v", frq.RequestStatus, cancelledStatus(at))
	}
	rlc, err := f.frpLifecycles.Get(ctx, aggID, "R1")
	must(t, err)
	if rlc.CancelledAt == nil || rlc.CancelReason != "client cancel" {
		t.Errorf("response lifecycle = %+v, want cancelled with reason client cancel", rlc)
	}
	for _, res := range execs {
		lc, err := f.controlLifecycles.Get(ctx, res.Scope.Key(), res.ID)
		must(t, err)
		if lc.CancelledAt == nil || lc.GrantMRID != grant.MRID {
			t.Errorf("execution %s lifecycle = %+v, want cancelled and still linked to %s", res.Control.MRID, lc, grant.MRID)
		}
	}
	if n := len(f.responses(t, aggID)); n != 1 {
		t.Errorf("responses = %d, want the one answered response kept", n)
	}

	// The cancelled grant no longer holds its window.
	frp := f.responseFor(base, 3600, "MRID-NEW")
	gate := flowreservation.NewLedgerGate(f.ledger, commitment.Resolver{Devices: f.devices, Managers: f.managers})
	if err := gate.Grant(ctx, aggID, frp.Interval, "", func(ctx context.Context) error {
		return f.frp.Create(ctx, aggID, "frq-new", frp)
	}); err != nil {
		t.Errorf("a new grant on the cancelled window was refused: %v", err)
	}

	// Cancelling again is a no-op, not a conflict.
	must(t, f.canceller.Cancel(ctx, aggID, "R1", cancelledStatus(at)))
}

func (f *cancelFixture) responseFor(start int64, dur uint32, mrid string) sep2.FlowReservationResponse {
	frp := sep2.FlowReservationResponse{
		EnergyAvailable: &sep2.SignedRealEnergy{Value: 1},
		PowerAvailable:  &sep2.ActivePower{Value: 1},
	}
	frp.Href = "/edev/" + aggID + "/frp/frq-new"
	frp.MRID = mrid
	frp.Interval = &sep2.DateTimeInterval{Start: start, Duration: dur}
	return frp
}

func TestCancel_DenialAnsweredEarlierStaysTheOneResponse(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newCancelFixture(t, flowreservation.Config{Deadline: time.Hour})
	storeRequest(t, f.frq, aggID, "R1", windowRequest("REQ-DENIED", time.Now().Add(time.Hour).Unix(), 600, 10000))
	_, err := f.queue.Answer(ctx, aggID, "R1", flowreservation.Decision{Kind: flowreservation.Deny})
	must(t, err)

	must(t, f.canceller.Cancel(ctx, aggID, "R1", cancelledStatus(time.Now().Unix())))

	resps := f.responses(t, aggID)
	if len(resps) != 1 || resps[0].Interval.Duration != 0 {
		t.Fatalf("responses = %+v, want the one earlier denial", resps)
	}
	if _, err := f.frpLifecycles.Get(ctx, aggID, "R1"); err == nil {
		t.Error("a denial gained a cancel mark; it commits nothing to cancel")
	}
}

// TestCancel_RacesTheHoldDeadline fires the deadline fallback and a client
// cancel together, many times: each round ends with exactly one response,
// and a grant that won the race is cancelled by the cancel that lost it.
func TestCancel_RacesTheHoldDeadline(t *testing.T) {
	t.Parallel()
	const rounds = 80
	var denied, grantedThenCancelled int
	for i := range rounds {
		ctx := context.Background()
		f := newCancelFixture(t, flowreservation.Config{Deadline: time.Hour})
		start := time.Now().Add(time.Hour).Unix()
		req := windowRequest("REQ-RACE", start, 600, 10000)
		storeRequest(t, f.frq, aggID, "R1", req)

		var wg sync.WaitGroup
		var cancelErr error
		wg.Add(1)
		go func() {
			defer wg.Done()
			cancelErr = f.canceller.Cancel(ctx, aggID, "R1", cancelledStatus(time.Now().Unix()))
		}()
		// createdAt at the requested start caps the deadline at zero, so the
		// fallback fires at once, alongside the cancel.
		f.queue.Submit(aggID, "R1", req, start)
		wg.Wait()
		must(t, cancelErr)

		// A fallback already past its Get may still be retrying; let it run.
		time.Sleep(5 * time.Millisecond)
		resps := f.responses(t, aggID)
		if len(resps) != 1 {
			t.Fatalf("round %d: responses = %d, want exactly 1", i, len(resps))
		}
		if resps[0].Interval.Duration == 0 {
			denied++
			continue
		}
		lc, err := f.frpLifecycles.Get(ctx, aggID, "R1")
		if err != nil || lc.CancelledAt == nil {
			t.Fatalf("round %d: grant stored but not cancelled (lifecycle %+v, err %v)", i, lc, err)
		}
		grantedThenCancelled++
	}
	t.Logf("%d rounds: denied %d, granted then cancelled %d", rounds, denied, grantedThenCancelled)
}
