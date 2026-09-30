package flowreservation_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/commitment"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/commitment/sources"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/dercontrol"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/flowreservation"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

// Tests for GRIDAPPSD/ieee-2030_5-server-go#714 slice S4: the queue's
// answers and deadline fallback checked against the real commitment ledger
// over real stores.

var (
	aggLFDI        = strings.Repeat("A", 40)
	managedLFDI    = strings.Repeat("B", 40)
	standaloneLFDI = strings.Repeat("C", 40)
)

const (
	aggID        = "agg"
	managedID    = "m1"
	standaloneID = "s1"
)

type ledgerFixture struct {
	devices           *memory.EndDeviceStore
	managers          *memory.EndDeviceManagementStore
	frq               *memory.ScopedStore[sep2.FlowReservationRequest]
	frp               *memory.ScopedStore[sep2.FlowReservationResponse]
	frpLifecycles     *memory.ScopedStore[dercontrol.LifecycleRecord]
	programs          *memory.ScopedStore[sep2.DERProgram]
	controls          *memory.ScopedStore[sep2.DERControl]
	controlLifecycles *memory.ScopedStore[dercontrol.LifecycleRecord]
	ledger            *commitment.Ledger
	queue             *flowreservation.Queue
}

// newLedgerFixture: an aggregator managing m1, and a standalone s1, with a
// queue whose gate is the ledger over the same stores.
func newLedgerFixture(t *testing.T, cfg flowreservation.Config) *ledgerFixture {
	t.Helper()
	ctx := context.Background()
	f := &ledgerFixture{
		devices:           memory.NewEndDeviceStore(),
		managers:          memory.NewEndDeviceManagementStore(),
		frq:               memory.NewScopedStore[sep2.FlowReservationRequest](),
		frp:               memory.NewScopedStore[sep2.FlowReservationResponse](),
		frpLifecycles:     memory.NewScopedStore[dercontrol.LifecycleRecord](),
		programs:          memory.NewScopedStore[sep2.DERProgram](),
		controls:          memory.NewScopedStore[sep2.DERControl](),
		controlLifecycles: memory.NewScopedStore[dercontrol.LifecycleRecord](),
	}
	must(t, f.devices.Create(ctx, aggID, sep2.EndDevice{LFDI: aggLFDI}))
	must(t, f.devices.Create(ctx, managedID, sep2.EndDevice{LFDI: managedLFDI}))
	must(t, f.devices.Create(ctx, standaloneID, sep2.EndDevice{LFDI: standaloneLFDI}))
	must(t, f.managers.Assign(ctx, aggLFDI, managedLFDI))
	f.ledger = sources.NewLedger(f.devices, f.managers, f.frp, f.frpLifecycles, f.controls, f.controlLifecycles)
	gate := flowreservation.NewLedgerGate(f.ledger, commitment.Resolver{Devices: f.devices, Managers: f.managers})
	f.queue = flowreservation.NewQueue(f.frq, f.frp, gate, cfg, nil)
	t.Cleanup(f.queue.Close)
	return f
}

// queueOver builds a second queue on the fixture's ledger whose response
// writes go through frp, which must write to f.frp for the ledger to see
// them.
func (f *ledgerFixture) queueOver(t *testing.T, frp flowreservation.FRPStore, cfg flowreservation.Config) *flowreservation.Queue {
	t.Helper()
	gate := flowreservation.NewLedgerGate(f.ledger, commitment.Resolver{Devices: f.devices, Managers: f.managers})
	q := flowreservation.NewQueue(f.frq, frp, gate, cfg, nil)
	t.Cleanup(q.Close)
	return q
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func windowRequest(mrid string, start int64, duration uint32, energyWh int64) sep2.FlowReservationRequest {
	return sep2.FlowReservationRequest{
		MRID:              mrid,
		EnergyRequested:   &sep2.SignedRealEnergy{Value: energyWh},
		PowerRequested:    &sep2.ActivePower{Value: 4000},
		IntervalRequested: &sep2.DateTimeInterval{Start: start, Duration: duration},
	}
}

func (f *ledgerFixture) answer(t *testing.T, edevID, frqID string, frq sep2.FlowReservationRequest) (sep2.FlowReservationResponse, error) {
	t.Helper()
	storeRequest(t, f.frq, edevID, frqID, frq)
	return f.queue.Answer(context.Background(), edevID, frqID, flowreservation.Decision{})
}

// addPlainControl stores an issuer-shaped control with a lifecycle record
// under m1, the way the ledger's control source reads one.
func (f *ledgerFixture) addPlainControl(t *testing.T, id string, start int64, duration uint32) {
	t.Helper()
	const scope = managedID + "/fsa1/derp1"
	ctrl := sep2.DERControl{DERControlBase: &sep2.DERControlBase{}}
	ctrl.Href = "/edev/" + managedID + "/fsa/fsa1/derp/derp1/derc/" + id
	ctrl.MRID = "MRID-" + id
	ctrl.Interval = &sep2.DateTimeInterval{Start: start, Duration: duration}
	must(t, f.controls.Create(context.Background(), scope, id, ctrl))
	must(t, f.controlLifecycles.Create(context.Background(), scope, id, dercontrol.LifecycleRecord{FleetKey: aggLFDI, Reach: 1}))
}

func (f *ledgerFixture) responses(t *testing.T, edevID string) []sep2.FlowReservationResponse {
	t.Helper()
	got, err := f.frp.List(context.Background(), edevID, store.ListOptions{Unbounded: true})
	must(t, err)
	return got.Items
}

func wantConflict(t *testing.T, err error, code commitment.ConflictCode, mrid string) {
	t.Helper()
	ce, ok := err.(*commitment.ConflictError)
	if !ok {
		t.Fatalf("err = %v (%T), want an unwrapped *commitment.ConflictError", err, err)
	}
	if ce.Code != code || ce.MRID != mrid {
		t.Fatalf("conflict = %+v, want %s naming %s", *ce, code, mrid)
	}
}

// TestLedgerGate_AnswerRefusesAnOverlappingGrantOfTheFleet: an operator
// grant overlapping a live grant of the same fleet returns the conflict
// naming that grant's mRID and stores no response. The fleet is the
// aggregator's, whether the second request sits on the aggregator or on
// its managed device; a standalone device is its own fleet.
func TestLedgerGate_AnswerRefusesAnOverlappingGrantOfTheFleet(t *testing.T) {
	t.Parallel()
	f := newLedgerFixture(t, flowreservation.Config{Deadline: time.Hour})
	const start = 1_000_000

	first, err := f.answer(t, aggID, "R1", windowRequest("REQ1", start, 600, 10000))
	must(t, err)
	if first.Interval == nil || first.Interval.Duration != 600 {
		t.Fatalf("first grant interval = %+v, want 600 s", first.Interval)
	}

	_, err = f.answer(t, aggID, "R2", windowRequest("REQ2", start+599, 600, 10000))
	wantConflict(t, err, commitment.ConflictFleetWindow, first.MRID)
	if _, err := f.frp.Get(context.Background(), aggID, "R2"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("response R2 after the conflict: err = %v, want ErrNotFound", err)
	}

	_, err = f.answer(t, managedID, "R3", windowRequest("REQ3", start+300, 60, 10000))
	wantConflict(t, err, commitment.ConflictFleetWindow, first.MRID)
	if got := f.responses(t, managedID); len(got) != 0 {
		t.Fatalf("responses under %s = %d, want 0", managedID, len(got))
	}

	// Touching at an instant is not overlap, and another fleet is free.
	if _, err := f.answer(t, aggID, "R4", windowRequest("REQ4", start+600, 600, 10000)); err != nil {
		t.Fatalf("grant touching the first at its end: err = %v, want nil", err)
	}
	if _, err := f.answer(t, standaloneID, "R5", windowRequest("REQ5", start, 600, 10000)); err != nil {
		t.Fatalf("grant on another fleet's same window: err = %v, want nil", err)
	}
	if got := f.responses(t, aggID); len(got) != 2 {
		t.Fatalf("responses under %s = %d, want 2 (R1 and R4)", aggID, len(got))
	}
}

// TestLedgerGate_AnswerRefusesAGrantOverAPlainControl: a live plain
// control of the fleet blocks a grant on its window, named in the conflict.
func TestLedgerGate_AnswerRefusesAGrantOverAPlainControl(t *testing.T) {
	t.Parallel()
	f := newLedgerFixture(t, flowreservation.Config{Deadline: time.Hour})
	f.addPlainControl(t, "C1", 2000, 300)

	_, err := f.answer(t, aggID, "R1", windowRequest("REQ1", 2299, 100, 10000))
	wantConflict(t, err, commitment.ConflictFleetWindow, "MRID-C1")
	if got := f.responses(t, aggID); len(got) != 0 {
		t.Fatalf("responses = %d, want 0", len(got))
	}
}

// TestLedgerGate_CheckFailuresAreInternal: a fleet that cannot be resolved,
// a store read that fails inside the ledger, and an unwired ledger each
// refuse the grant as an internal error, never a conflict, and store
// nothing.
func TestLedgerGate_CheckFailuresAreInternal(t *testing.T) {
	t.Parallel()
	req := windowRequest("REQ1", 3000, 600, 10000)
	var conflict *commitment.ConflictError

	t.Run("unknown EndDevice", func(t *testing.T) {
		t.Parallel()
		f := newLedgerFixture(t, flowreservation.Config{Deadline: time.Hour})
		_, err := f.answer(t, "ghost", "R1", req)
		if !errors.Is(err, flowreservation.ErrCommitmentCheck) || !errors.Is(err, store.ErrNotFound) || errors.As(err, &conflict) {
			t.Fatalf("err = %v, want ErrCommitmentCheck wrapping ErrNotFound and no conflict", err)
		}
		if got := f.responses(t, "ghost"); len(got) != 0 {
			t.Fatalf("responses = %d, want 0", len(got))
		}
	})

	t.Run("store read fails inside the ledger", func(t *testing.T) {
		t.Parallel()
		f := newLedgerFixture(t, flowreservation.Config{Deadline: time.Hour})
		// A response under an EndDevice with no LFDI makes every grant scan
		// fail: the resolver refuses a fleet of "".
		must(t, f.devices.Create(context.Background(), "nolfdi", sep2.EndDevice{}))
		bad := sep2.FlowReservationResponse{}
		bad.Href = "/edev/nolfdi/frp/X"
		bad.MRID = "MRID-X"
		bad.Interval = &sep2.DateTimeInterval{Start: 1, Duration: 1}
		must(t, f.frp.Create(context.Background(), "nolfdi", "X", bad))

		_, err := f.answer(t, aggID, "R1", req)
		if !errors.Is(err, flowreservation.ErrCommitmentCheck) || errors.As(err, &conflict) {
			t.Fatalf("err = %v, want ErrCommitmentCheck and no conflict", err)
		}
		if got := f.responses(t, aggID); len(got) != 0 {
			t.Fatalf("responses = %d, want 0", len(got))
		}
	})

	t.Run("nil ledger", func(t *testing.T) {
		t.Parallel()
		frq := memory.NewScopedStore[sep2.FlowReservationRequest]()
		frp := memory.NewScopedStore[sep2.FlowReservationResponse]()
		gate := flowreservation.NewLedgerGate(nil, commitment.Resolver{})
		q := flowreservation.NewQueue(frq, frp, gate, flowreservation.Config{Deadline: time.Hour}, nil)
		t.Cleanup(q.Close)
		storeRequest(t, frq, aggID, "R1", req)
		_, err := q.Answer(context.Background(), aggID, "R1", flowreservation.Decision{})
		if !errors.Is(err, flowreservation.ErrCommitmentCheck) || !errors.Is(err, commitment.ErrNoLedger) {
			t.Fatalf("err = %v, want ErrCommitmentCheck wrapping ErrNoLedger", err)
		}
		assertNoResponse(t, frp, aggID)
	})
}

// TestLedgerGate_FallbackDeniesOnConflictAndGrantsWhenFree: with no
// operator answer, the deadline grants a free window as asked and denies
// one the fleet already holds.
func TestLedgerGate_FallbackDeniesOnConflictAndGrantsWhenFree(t *testing.T) {
	t.Parallel()
	f := newLedgerFixture(t, flowreservation.Config{Deadline: time.Millisecond})
	start := time.Now().Add(time.Hour).Unix()

	held, err := f.answer(t, aggID, "R1", windowRequest("REQ1", start, 600, 10000))
	must(t, err)

	contested := windowRequest("REQ2", start+100, 600, 10000)
	storeRequest(t, f.frq, managedID, "R2", contested)
	f.queue.Submit(managedID, "R2", contested, time.Now().Unix())
	denied := waitForResponse(t, f.frp, managedID)[0]
	if denied.Interval == nil || denied.Interval.Duration != 0 || denied.EnergyAvailable == nil || denied.EnergyAvailable.Value != 0 {
		t.Fatalf("contested fallback = interval %+v energy %+v, want a denial", denied.Interval, denied.EnergyAvailable)
	}

	free := windowRequest("REQ3", start+600, 600, 10000)
	storeRequest(t, f.frq, standaloneID, "R3", free)
	f.queue.Submit(standaloneID, "R3", free, time.Now().Unix())
	granted := waitForResponse(t, f.frp, standaloneID)[0]
	if granted.Interval == nil || *granted.Interval != *free.IntervalRequested || *granted.EnergyAvailable != *free.EnergyRequested {
		t.Fatalf("free fallback = interval %+v energy %+v, want the request granted as asked", granted.Interval, granted.EnergyAvailable)
	}

	if got := f.responses(t, aggID); len(got) != 1 || got[0].MRID != held.MRID {
		t.Fatalf("aggregator responses = %+v, want only the held grant", got)
	}
}

// TestLedgerGate_FallbackDeniesWhenTheCheckFails: the fallback fails
// closed when the ledger cannot answer, here an EndDevice the resolver
// cannot find.
func TestLedgerGate_FallbackDeniesWhenTheCheckFails(t *testing.T) {
	t.Parallel()
	f := newLedgerFixture(t, flowreservation.Config{Deadline: time.Millisecond})
	req := windowRequest("REQ1", time.Now().Add(time.Hour).Unix(), 600, 10000)
	storeRequest(t, f.frq, "ghost", "R1", req)
	f.queue.Submit("ghost", "R1", req, time.Now().Unix())

	got := waitForResponse(t, f.frp, "ghost")[0]
	if got.Interval == nil || got.Interval.Duration != 0 {
		t.Fatalf("fallback interval = %+v, want a denial", got.Interval)
	}
}

// TestLedgerGate_GrantSignReachesTheExecutionCheck: the response the queue
// stores keeps the request's sign, charging positive, and the ledger reads
// it so that a charge grant executes only as a negative opModTargetW and a
// discharge grant only as a positive one.
func TestLedgerGate_GrantSignReachesTheExecutionCheck(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name          string
		energyWh      int64
		accept, refus int16
	}{
		{"charge", 10000, -2000, 2000},
		{"discharge", -10000, 2000, -2000},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newLedgerFixture(t, flowreservation.Config{Deadline: time.Hour})
			grant, err := f.answer(t, aggID, "R1", windowRequest("REQ1", 5000, 3600, tc.energyWh))
			must(t, err)
			stored, err := f.frp.Get(context.Background(), aggID, "R1")
			must(t, err)
			if stored.EnergyAvailable == nil || stored.EnergyAvailable.Value != tc.energyWh {
				t.Fatalf("stored energyAvailable = %+v, want %d", stored.EnergyAvailable, tc.energyWh)
			}

			check := func(target int16) error {
				p := commitment.Proposal{
					FleetKey:  aggLFDI,
					Window:    commitment.Window{Start: 5000, Duration: 600},
					GrantMRID: grant.MRID,
					TargetW:   &sep2.ActivePower{Value: target},
					Reach:     1,
				}
				return f.ledger.Within(context.Background(), []string{aggLFDI}, func(v commitment.View) error {
					return v.CheckControl(context.Background(), p)
				})
			}
			if err := check(tc.accept); err != nil {
				t.Fatalf("execution at %d W: err = %v, want accepted", tc.accept, err)
			}
			wantConflict(t, check(tc.refus), commitment.ConflictDirection, grant.MRID)
		})
	}
}

// TestLedgerGate_FallbackRacesAPlainControl runs the deadline fallback and
// a plain control create on one fleet window at the same time, many
// times: exactly one of them commits the window, never both.
func TestLedgerGate_FallbackRacesAPlainControl(t *testing.T) {
	t.Parallel()
	const rounds = 80
	grants, controls := 0, 0
	for i := range rounds {
		grantWon, controlWon := raceFallbackAgainstControl(t, i)
		if grantWon == controlWon {
			t.Fatalf("round %d: grant stored %v, control stored %v; want exactly one", i, grantWon, controlWon)
		}
		if grantWon {
			grants++
		} else {
			controls++
		}
	}
	t.Logf("%d rounds: grant won %d, control won %d", rounds, grants, controls)
	// A ledger that refused every call, or never contended, would let one
	// side win every round; the race has only been exercised if both won.
	if grants == 0 || controls == 0 {
		t.Fatalf("%d rounds: grant won %d, control won %d; want each side to win at least once", rounds, grants, controls)
	}
}

func raceFallbackAgainstControl(t *testing.T, round int) (grantStored, controlStored bool) {
	t.Helper()
	ctx := context.Background()
	f := newLedgerFixture(t, flowreservation.Config{Deadline: time.Hour})
	pen := uint32(0x40732001)
	issuer, err := dercontrol.NewIssuer(f.programs, f.controls, f.controlLifecycles, dercontrol.Config{PEN: &pen})
	must(t, err)
	must(t, f.programs.Create(ctx, managedID, "derp1", sep2.DERProgram{
		DERControlListLink: &sep2.ListLink{Href: "/edev/" + managedID + "/fsa/fsa1/derp/derp1/derc"},
	}))

	start := time.Now().Add(time.Hour).Unix()
	req := windowRequest("REQ", start, 600, 10000)
	storeRequest(t, f.frq, aggID, "R1", req)

	limit := uint16(5000)
	create := dercontrol.CreateRequest{
		DERProgramHref:  "/edev/" + managedID + "/fsa/fsa1/derp/derp1",
		Type:            dercontrol.MaxLimW,
		MaxLimW:         &limit,
		Start:           &start,
		DurationSeconds: 600,
	}

	var wg sync.WaitGroup
	var issueErr error
	wg.Add(1)
	go func() {
		defer wg.Done()
		issueErr = f.ledger.Within(ctx, []string{aggLFDI}, func(v commitment.View) error {
			_, err := issuer.IssueInFleet(ctx, create, dercontrol.Fleet{Key: aggLFDI, Reach: 1, Check: viewCheck(v)})
			return err
		})
	}()
	// createdAt at the requested start caps the deadline at zero, so the
	// fallback fires at once, alongside the create.
	f.queue.Submit(aggID, "R1", req, start)
	frp := waitForResponse(t, f.frp, aggID)[0]
	wg.Wait()

	var ce *commitment.ConflictError
	if issueErr != nil && !errors.As(issueErr, &ce) {
		t.Fatalf("round %d: IssueInFleet err = %v, want nil or a conflict", round, issueErr)
	}
	count, err := f.controls.Count(ctx, managedID+"/fsa1/derp1")
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		t.Fatal(err)
	}
	return frp.Interval != nil && frp.Interval.Duration > 0, count > 0
}

func viewCheck(v commitment.View) dercontrol.Check {
	return func(ctx context.Context, p dercontrol.Proposal) error {
		return v.CheckControl(ctx, commitment.Proposal{
			FleetKey:   p.FleetKey,
			Window:     p.Window,
			GrantMRID:  p.GrantMRID,
			TargetW:    p.TargetW,
			Reach:      p.Reach,
			Supersedes: p.Supersedes,
		})
	}
}

// blockingFRP holds every Create until release is closed, reporting entry
// on entered.
type blockingFRP struct {
	*memory.ScopedStore[sep2.FlowReservationResponse]
	entered chan struct{}
	release chan struct{}
}

func (b *blockingFRP) Create(ctx context.Context, parentID, id string, r sep2.FlowReservationResponse) error {
	close(b.entered)
	<-b.release
	return b.ScopedStore.Create(ctx, parentID, id, r)
}

// TestLedgerGate_WriteHoldsTheFleetLock: the response Create runs inside
// the ledger's Within, so another check on the same fleet waits until the
// write returns. A gate that checked under the lock and wrote after it
// would let that check in while the grant is still unwritten.
func TestLedgerGate_WriteHoldsTheFleetLock(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newLedgerFixture(t, flowreservation.Config{Deadline: time.Hour})
	frp := &blockingFRP{ScopedStore: f.frp, entered: make(chan struct{}), release: make(chan struct{})}
	q := f.queueOver(t, frp, flowreservation.Config{Deadline: time.Hour})
	storeRequest(t, f.frq, aggID, "R1", windowRequest("REQ1", 7000, 600, 10000))

	answered := make(chan error, 1)
	go func() {
		_, err := q.Answer(ctx, aggID, "R1", flowreservation.Decision{})
		answered <- err
	}()
	<-frp.entered

	checked := make(chan error, 1)
	go func() {
		w := commitment.Window{Start: 7100, Duration: 10}
		checked <- f.ledger.Within(ctx, []string{aggLFDI}, func(v commitment.View) error {
			return v.CheckGrant(ctx, aggLFDI, &w, "")
		})
	}()
	select {
	case err := <-checked:
		close(frp.release)
		t.Fatalf("a second check on the fleet ran while the grant's Create was held (err = %v); the write must be inside Within", err)
	case <-time.After(100 * time.Millisecond):
	}

	close(frp.release)
	must(t, <-answered)
	grant, err := f.frp.Get(ctx, aggID, "R1")
	must(t, err)
	wantConflict(t, <-checked, commitment.ConflictFleetWindow, grant.MRID)
}

// flakyFRP fails the first failures Create calls with a transient error.
type flakyFRP struct {
	*memory.ScopedStore[sep2.FlowReservationResponse]
	mu       sync.Mutex
	failures int
}

func (s *flakyFRP) Create(ctx context.Context, parentID, id string, r sep2.FlowReservationResponse) error {
	s.mu.Lock()
	fail := s.failures > 0
	if fail {
		s.failures--
	}
	s.mu.Unlock()
	if fail {
		return errors.New("transient create failure")
	}
	return s.ScopedStore.Create(ctx, parentID, id, r)
}

// TestLedgerGate_FallbackRetriesAFailedGatedCreate: a Create that fails
// inside the gate is an infrastructure failure, retried to a grant, not a
// refused check turned into a permanent denial.
func TestLedgerGate_FallbackRetriesAFailedGatedCreate(t *testing.T) {
	t.Parallel()
	f := newLedgerFixture(t, flowreservation.Config{Deadline: time.Hour})
	cfg := flowreservation.Config{Deadline: time.Millisecond, RetryBackoff: time.Millisecond, RetryAttempts: 3}
	q := f.queueOver(t, &flakyFRP{ScopedStore: f.frp, failures: 1}, cfg)

	req := windowRequest("REQ1", time.Now().Add(time.Hour).Unix(), 600, 10000)
	storeRequest(t, f.frq, aggID, "R1", req)
	q.Submit(aggID, "R1", req, time.Now().Unix())

	got := waitForResponse(t, f.frp, aggID)
	if len(got) != 1 || got[0].Interval == nil || *got[0].Interval != *req.IntervalRequested {
		t.Fatalf("responses after one failed Create = %+v, want one grant as asked", got)
	}
	if _, err := q.Answer(context.Background(), aggID, "R1", flowreservation.Decision{Kind: flowreservation.Deny}); !errors.Is(err, flowreservation.ErrAlreadyAnswered) {
		t.Fatalf("later Answer err = %v, want ErrAlreadyAnswered", err)
	}
}
