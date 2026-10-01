package commitment

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"
)

func mridsOfGrants(gs []Grant) []string {
	out := []string{}
	for _, g := range gs {
		out = append(out, g.MRID)
	}
	return out
}

func mridsOfControls(cs []Control) []string {
	out := []string{}
	for _, c := range cs {
		out = append(out, c.MRID)
	}
	return out
}

func cancelledGrant(mrid string, w Window) Grant {
	g := grantOn(mrid, w)
	at := int64(1)
	g.CancelledAt = &at
	return g
}

func executionOf(mrid, grant string, w Window) Control {
	c := baseExecution()
	c.MRID, c.GrantMRID, c.Window = mrid, grant, w
	return c
}

func cancelledControl(c Control) Control {
	c.Cancelled = true
	return c
}

func TestLive(t *testing.T) {
	t.Parallel()
	w := Window{Start: 1000, Duration: 600}
	noWindow := baseGrant()
	noWindow.MRID, noWindow.Window = "g-nowindow", nil

	grants := []Grant{grantOn("g-live", w), cancelledGrant("g-cancelled", w), noWindow, grantOn("g-live2", w)}
	controls := []Control{
		plainControl("c-plain", w),
		cancelledControl(plainControl("c-plain-cancelled", w)),
		executionOf("c-exec-live", "g-live", w),
		cancelledControl(executionOf("c-exec-live-cancelled", "g-live", w)),
		executionOf("c-exec-cancelled-grant", "g-cancelled", w),
		executionOf("c-exec-nowindow-grant", "g-nowindow", w),
		executionOf("c-exec-gone-grant", "g-gone", w),
	}

	live, plain := Live(grants, controls)
	if got, want := mridsOfGrants(live), []string{"g-live", "g-live2"}; !slices.Equal(got, want) {
		t.Errorf("live grants = %v, want %v", got, want)
	}
	wantPlain := []string{"c-plain", "c-exec-cancelled-grant", "c-exec-nowindow-grant", "c-exec-gone-grant"}
	if got := mridsOfControls(plain); !slices.Equal(got, wantPlain) {
		t.Errorf("plain controls = %v, want %v", got, wantPlain)
	}

	// Without controls only the grants are sorted, as CheckGrant asks first.
	live, plain = Live(grants, nil)
	if got := mridsOfGrants(live); !slices.Equal(got, []string{"g-live", "g-live2"}) || plain != nil {
		t.Errorf("Live(grants, nil) = %v, %v; want the two live grants and no controls", got, plain)
	}
}

// CheckGrant refuses a window exactly when Live counts a commitment that
// overlaps it, naming the first one: grants first, then plain controls.
func TestCheckGrantRefusesWhatLiveCounts(t *testing.T) {
	t.Parallel()
	grants := []Grant{
		grantOn("g-live", Window{Start: 1000, Duration: 600}),
		cancelledGrant("g-cancelled", Window{Start: 2000, Duration: 600}),
		grantOn("g-old", Window{Start: 4000, Duration: 600}),
	}
	controls := []Control{
		executionOf("c-exec-live", "g-live", Window{Start: 1000, Duration: 600}),
		executionOf("c-exec-cancelled-grant", "g-cancelled", Window{Start: 2000, Duration: 600}),
		cancelledControl(plainControl("c-cancelled", Window{Start: 3000, Duration: 600})),
		plainControl("c-plain", Window{Start: 3500, Duration: 600}),
		executionOf("c-exec-old", "g-old", Window{Start: 4000, Duration: 600}),
	}
	l := NewLedger(&fakeGrants{grants: grants}, &fakeControls{controls: controls})

	// Each probe is [start, start+300). Commitments: g-live [1000,1600),
	// c-exec-cancelled-grant [2000,2600) as plain, c-plain [3500,4100) and
	// g-old [4000,4600) with its execution under it.
	cases := []struct {
		start  int64
		except string
		want   string // "" for free
	}{
		{700, "", ""},
		{800, "", "g-live"},
		{1550, "", "g-live"},
		{1600, "", ""},
		{1900, "", "c-exec-cancelled-grant"},
		{2600, "", ""},
		{3000, "", ""},
		{3400, "", "c-plain"},
		{3900, "", "g-old"},
		{3900, "g-old", "c-plain"},
		{4200, "", "g-old"},
		{4200, "g-old", ""},
		{4600, "", ""},
	}
	for _, tc := range cases {
		w := Window{Start: tc.start, Duration: 300}
		err := checkGrant(t, l, "FLEET1", &w, tc.except)
		if tc.want == "" {
			if err != nil {
				t.Errorf("except %q, window %+v: CheckGrant = %v, want free", tc.except, w, err)
			}
			continue
		}
		var ce *ConflictError
		if !errors.As(err, &ce) || ce.Code != ConflictFleetWindow || ce.MRID != tc.want {
			t.Errorf("except %q, window %+v: CheckGrant = %v, want a conflict naming %s", tc.except, w, err, tc.want)
		}
	}
}

// A grant conflict is refused even when the control read fails, as it was
// when grants were checked before controls were read.
func TestCheckGrantRefusesAGrantConflictWhenControlsFail(t *testing.T) {
	t.Parallel()
	w := Window{Start: 1000, Duration: 600}
	l := NewLedger(&fakeGrants{grants: []Grant{grantOn("g-live", w)}}, &fakeControls{err: errStoreDown})
	wantConflict(t, checkGrant(t, l, "FLEET1", &w, ""), ConflictFleetWindow, "g-live")

	// With no grant conflict the control failure still refuses.
	free := Window{Start: 5000, Duration: 600}
	if err := checkGrant(t, l, "FLEET1", &free, ""); !errors.Is(err, errStoreDown) {
		t.Errorf("CheckGrant on a free window with controls down = %v, want errStoreDown", err)
	}
}

// blockingControls holds its one-walk read open until released, so a test
// can see whether the fleet lock is held meanwhile.
type blockingControls struct {
	fakeControls
	entered, release chan struct{}
}

func (b *blockingControls) ControlsAndExecutions(ctx context.Context, fleetKey string, mrids []string) ([]Control, map[string][]Control, error) {
	close(b.entered)
	<-b.release
	return b.fakeControls.ControlsAndExecutions(ctx, fleetKey, mrids)
}

// Commitments reads under the fleet lock: a Within on the same fleet waits
// until the read returns.
func TestLedgerCommitmentsHoldsTheFleetLock(t *testing.T) {
	t.Parallel()
	src := &blockingControls{entered: make(chan struct{}), release: make(chan struct{})}
	l := NewLedger(&fakeGrants{}, src)
	done := make(chan error, 1)
	go func() {
		_, err := l.Commitments(context.Background(), "FLEET1", 0)
		done <- err
	}()
	<-src.entered

	acquired := make(chan struct{})
	go func() {
		_ = l.Within(context.Background(), []string{"FLEET1"}, func(View) error { return nil })
		close(acquired)
	}()
	select {
	case <-acquired:
		t.Error("Within on the same fleet ran while Commitments was reading")
	case <-time.After(100 * time.Millisecond):
	}
	close(src.release)
	if err := <-done; err != nil {
		t.Fatalf("Commitments = %v", err)
	}
	select {
	case <-acquired:
	case <-time.After(5 * time.Second):
		t.Fatal("Within never ran after Commitments returned")
	}
}

func TestLedgerCommitments(t *testing.T) {
	t.Parallel()
	const now = 2000
	other := plainControl("c-other-fleet", Window{Start: 2500, Duration: 600})
	other.FleetKey = "FLEET2"
	// An execution stored in another fleet still counts against its grant,
	// as an execution check counts it.
	otherExec := executionOf("c-running-fleet2", "g-running", Window{Start: 1900, Duration: 200})
	otherExec.FleetKey = "FLEET2"
	grants := []Grant{
		grantOn("g-future", Window{Start: 5000, Duration: 600}),  // not started: still committed
		grantOn("g-running", Window{Start: 1500, Duration: 600}), // ends 2100
		grantOn("g-ended", Window{Start: 1000, Duration: 1000}),  // ends exactly at now
		cancelledGrant("g-cancelled", Window{Start: 3000, Duration: 600}),
	}
	controls := []Control{
		executionOf("c-running-1", "g-running", Window{Start: 1500, Duration: 300}),
		cancelledControl(executionOf("c-running-cancelled", "g-running", Window{Start: 1800, Duration: 300})),
		executionOf("c-of-cancelled", "g-cancelled", Window{Start: 3000, Duration: 600}),
		plainControl("c-plain-future", Window{Start: 2500, Duration: 600}),
		plainControl("c-plain-ended", Window{Start: 1000, Duration: 1000}),
		plainControl("c-superseded-before-start", Window{Start: 2600}),
		cancelledControl(plainControl("c-plain-cancelled", Window{Start: 2500, Duration: 600})),
		other,
		otherExec,
	}
	l := NewLedger(&fakeGrants{grants: grants}, &fakeControls{controls: controls})

	got, err := l.Commitments(context.Background(), "FLEET1", now)
	if err != nil {
		t.Fatalf("Commitments = %v", err)
	}
	var gotGrants []string
	execs := map[string][]string{}
	for _, g := range got.Grants {
		gotGrants = append(gotGrants, g.MRID)
		execs[g.MRID] = mridsOfControls(g.Executions)
	}
	if want := []string{"g-future", "g-running"}; !slices.Equal(gotGrants, want) {
		t.Errorf("grants = %v, want %v", gotGrants, want)
	}
	if want := []string{"c-running-1", "c-running-fleet2"}; !slices.Equal(execs["g-running"], want) {
		t.Errorf("executions of g-running = %v, want %v (the cancelled one is not counted, the other fleet's is)", execs["g-running"], want)
	}
	if len(execs["g-future"]) != 0 {
		t.Errorf("executions of g-future = %v, want none", execs["g-future"])
	}
	if want := []string{"c-of-cancelled", "c-plain-future"}; !slices.Equal(mridsOfControls(got.Plain), want) {
		t.Errorf("plain = %v, want %v", mridsOfControls(got.Plain), want)
	}
}

// executionsDown fails the one-walk control read only; ControlsInFleet
// alone still succeeds.
type executionsDown struct{ fakeControls }

func (executionsDown) ControlsAndExecutions(context.Context, string, []string) ([]Control, map[string][]Control, error) {
	return nil, nil, errStoreDown
}

// A failed read fails the whole call: an empty answer would read as a free
// fleet.
func TestLedgerCommitmentsRefusesOnAStoreError(t *testing.T) {
	t.Parallel()
	g := []Grant{grantOn("g-live", Window{Start: 5000, Duration: 600})}
	cases := map[string]*Ledger{
		"grants":     NewLedger(&fakeGrants{err: errStoreDown}, &fakeControls{}),
		"controls":   NewLedger(&fakeGrants{grants: g}, &fakeControls{err: errStoreDown}),
		"executions": NewLedger(&fakeGrants{grants: g}, &executionsDown{}),
	}
	for name, l := range cases {
		got, err := l.Commitments(context.Background(), "FLEET1", 0)
		if !errors.Is(err, errStoreDown) {
			t.Errorf("%s down: Commitments error = %v, want errStoreDown", name, err)
		}
		if got.Grants != nil || got.Plain != nil {
			t.Errorf("%s down: Commitments returned %+v with its error, want nothing", name, got)
		}
	}

	var nilLedger *Ledger
	if _, err := nilLedger.Commitments(context.Background(), "FLEET1", 0); !errors.Is(err, ErrNoLedger) {
		t.Errorf("nil ledger: Commitments error = %v, want ErrNoLedger", err)
	}
}
