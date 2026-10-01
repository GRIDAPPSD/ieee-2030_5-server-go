package commitment

import (
	"context"
	"errors"
	"slices"
	"testing"
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

	for _, except := range []string{"", "g-old"} {
		for start := int64(0); start <= 5000; start += 250 {
			w := Window{Start: start, Duration: 300}
			live, _ := Live(grants, nil)
			_, plain := Live(grants, controls)
			want := ""
			for _, g := range live {
				if g.MRID != except && g.Window.Overlaps(w) {
					want = g.MRID
					break
				}
			}
			if want == "" {
				for _, c := range plain {
					if c.Window.Overlaps(w) {
						want = c.MRID
						break
					}
				}
			}

			err := checkGrant(t, l, "FLEET1", &w, except)
			if want == "" {
				if err != nil {
					t.Errorf("except %q, window %+v: CheckGrant = %v, want free", except, w, err)
				}
				continue
			}
			var ce *ConflictError
			if !errors.As(err, &ce) || ce.Code != ConflictFleetWindow || ce.MRID != want {
				t.Errorf("except %q, window %+v: CheckGrant = %v, want a conflict naming %s", except, w, err, want)
			}
		}
	}
}

func TestLedgerCommitments(t *testing.T) {
	t.Parallel()
	const now = 2000
	other := plainControl("c-other-fleet", Window{Start: 2500, Duration: 600})
	other.FleetKey = "FLEET2"
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
	if want := []string{"c-running-1"}; !slices.Equal(execs["g-running"], want) {
		t.Errorf("executions of g-running = %v, want %v (the cancelled one is not counted)", execs["g-running"], want)
	}
	if len(execs["g-future"]) != 0 {
		t.Errorf("executions of g-future = %v, want none", execs["g-future"])
	}
	if want := []string{"c-of-cancelled", "c-plain-future"}; !slices.Equal(mridsOfControls(got.Plain), want) {
		t.Errorf("plain = %v, want %v", mridsOfControls(got.Plain), want)
	}
}

// executionsDown fails ExecutionsOf only, after both fleet reads succeed.
type executionsDown struct{ fakeControls }

func (executionsDown) ExecutionsOf(context.Context, string) ([]Control, error) {
	return nil, errStoreDown
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
