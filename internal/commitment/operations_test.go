package commitment

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
)

// fakeWriters applies CancelGrant's and Revise's writes to the fake sources,
// logging every call so a test can assert the write order. failOn names a
// call ("cancel:<mrid>", "relink:<mrid>-><grant>", "mark:<mrid>",
// "create:<mrid>", "delete:<mrid>") that returns errWrite instead;
// applyThenFail names one that is applied and then reports errWrite, as a
// store whose rollback failed leaves it. Every write fails the test when the
// fleet lock is not held. With honorCtx a write refuses a done context, as
// Issuer.Relink does, and cancelOnFail is called when a write fails.
type fakeWriters struct {
	t             *testing.T
	ledger        *Ledger
	grants        *fakeGrants
	controls      *fakeControls
	calls         []string
	failOn        map[string]bool
	applyThenFail map[string]bool
	honorCtx      bool
	cancelOnFail  context.CancelFunc
}

var errWrite = errors.New("fake: write failed")

// record logs call and reports whether to apply it and what to return.
func (f *fakeWriters) record(ctx context.Context, call string) (apply bool, err error) {
	f.calls = append(f.calls, call)
	if m := f.ledger.fleetLock("FLEET1"); m.TryLock() {
		m.Unlock()
		f.t.Errorf("write %s ran without the fleet lock", call)
	}
	if f.honorCtx && ctx.Err() != nil {
		return false, ctx.Err()
	}
	switch {
	case f.failOn[call]:
		apply, err = false, errWrite
	case f.applyThenFail[call]:
		apply, err = true, errWrite
	default:
		return true, nil
	}
	if f.cancelOnFail != nil {
		f.cancelOnFail()
	}
	return apply, err
}

func (f *fakeWriters) control(mrid string) *Control {
	for i := range f.controls.controls {
		if f.controls.controls[i].MRID == mrid {
			return &f.controls.controls[i]
		}
	}
	panic("fake: no control " + mrid)
}

func (f *fakeWriters) CancelExecution(ctx context.Context, c Control, _ string) error {
	apply, err := f.record(ctx, "cancel:"+c.MRID)
	if apply {
		f.control(c.MRID).Cancelled = true
	}
	return err
}

func (f *fakeWriters) RelinkExecution(ctx context.Context, c Control, grantMRID string) error {
	apply, err := f.record(ctx, "relink:"+c.MRID+"->"+grantMRID)
	if apply {
		f.control(c.MRID).GrantMRID = grantMRID
	}
	return err
}

func (f *fakeWriters) MarkCancelled(ctx context.Context, g Grant, _ string, now int64) error {
	apply, err := f.record(ctx, "mark:"+g.MRID)
	if apply {
		for i := range f.grants.grants {
			if f.grants.grants[i].MRID == g.MRID {
				f.grants.grants[i].CancelledAt = &now
			}
		}
	}
	return err
}

func (f *fakeWriters) writers() Writers {
	return Writers{Executions: f, Grants: f}
}

// replacement returns a Replacement whose Create and Delete add and remove
// g in the fake grant source.
func (f *fakeWriters) replacement(g Grant) Replacement {
	return Replacement{
		Grant: g,
		Create: func(ctx context.Context) error {
			apply, err := f.record(ctx, "create:"+g.MRID)
			if apply {
				f.grants.grants = append(f.grants.grants, g)
			}
			return err
		},
		Delete: func(ctx context.Context) error {
			apply, err := f.record(ctx, "delete:"+g.MRID)
			if apply {
				f.grants.grants = slices.DeleteFunc(f.grants.grants, func(x Grant) bool { return x.MRID == g.MRID })
			}
			return err
		},
	}
}

// opsFixture: grant-1 on [1000, 4600) with two executions, listed in the
// source in the reverse of their time order (ctrl-b starts at 1000,
// ctrl-a at 2000), so a test naming "the first" proves the ledger sorts.
func opsFixture(t *testing.T) (*Ledger, *fakeWriters) {
	g := baseGrant()
	g.Subject, g.CreationTime = "SUBJ-1", 100
	a := baseExecution()
	a.MRID, a.Window = "ctrl-a", Window{Start: 2000, Duration: 600}
	b := baseExecution()
	b.MRID, b.Window = "ctrl-b", Window{Start: 1000, Duration: 600}
	grants := &fakeGrants{grants: []Grant{g}}
	controls := &fakeControls{controls: []Control{a, b}}
	l := NewLedger(grants, controls)
	return l, &fakeWriters{t: t, ledger: l, grants: grants, controls: controls}
}

func revisedGrant(mutate func(*Grant)) Grant {
	g := baseGrant()
	g.MRID = "grant-2"
	g.Subject, g.CreationTime = "SUBJ-1", 200
	mutate(&g)
	return g
}

func TestCancelGrant_ExecutionsFirstThenGrant(t *testing.T) {
	t.Parallel()
	l, w := opsFixture(t)
	if err := l.CancelGrant(context.Background(), w.writers(), "grant-1", "client cancel", 900); err != nil {
		t.Fatalf("CancelGrant() error = %v", err)
	}
	want := []string{"cancel:ctrl-b", "cancel:ctrl-a", "mark:grant-1"}
	if !slices.Equal(w.calls, want) {
		t.Fatalf("calls = %v, want %v", w.calls, want)
	}
	if g := w.grants.grants[0]; g.CancelledAt == nil || *g.CancelledAt != 900 {
		t.Fatalf("grant CancelledAt = %v, want 900", g.CancelledAt)
	}
	if err := checkGrant(t, l, "FLEET1", baseGrant().Window, ""); err != nil {
		t.Fatalf("CheckGrant on the cancelled grant's window = %v, want free", err)
	}
}

func TestCancelGrant_SkipsCancelledExecution(t *testing.T) {
	t.Parallel()
	l, w := opsFixture(t)
	w.control("ctrl-a").Cancelled = true
	if err := l.CancelGrant(context.Background(), w.writers(), "grant-1", "", 900); err != nil {
		t.Fatalf("CancelGrant() error = %v", err)
	}
	if want := []string{"cancel:ctrl-b", "mark:grant-1"}; !slices.Equal(w.calls, want) {
		t.Fatalf("calls = %v, want %v", w.calls, want)
	}
}

func TestCancelGrant_ExecutionFailureLeavesGrantLive(t *testing.T) {
	t.Parallel()
	l, w := opsFixture(t)
	w.failOn = map[string]bool{"cancel:ctrl-a": true}
	err := l.CancelGrant(context.Background(), w.writers(), "grant-1", "", 900)
	if !errors.Is(err, errWrite) {
		t.Fatalf("CancelGrant() error = %v, want wrapping errWrite", err)
	}
	if slices.Contains(w.calls, "mark:grant-1") {
		t.Fatalf("calls = %v: the grant was marked after an execution failed to cancel", w.calls)
	}
	if w.grants.grants[0].CancelledAt != nil {
		t.Fatal("grant reads cancelled with a live execution left")
	}
}

func TestCancelGrant_Refusals(t *testing.T) {
	t.Parallel()
	t.Run("absent grant", func(t *testing.T) {
		t.Parallel()
		l, w := opsFixture(t)
		err := l.CancelGrant(context.Background(), w.writers(), "grant-none", "", 900)
		if !errors.Is(err, ErrNoGrant) {
			t.Fatalf("CancelGrant() error = %v, want ErrNoGrant", err)
		}
		if len(w.calls) != 0 {
			t.Fatalf("calls = %v, want none", w.calls)
		}
	})
	t.Run("cancelled grant", func(t *testing.T) {
		t.Parallel()
		l, w := opsFixture(t)
		at := int64(5)
		w.grants.grants[0].CancelledAt = &at
		err := l.CancelGrant(context.Background(), w.writers(), "grant-1", "", 900)
		wantConflict(t, err, ConflictGrantNotLive, "grant-1")
		if len(w.calls) != 0 {
			t.Fatalf("calls = %v, want none", w.calls)
		}
	})
	t.Run("store read fails", func(t *testing.T) {
		t.Parallel()
		l, w := opsFixture(t)
		w.controls.err = errStoreDown
		if err := l.CancelGrant(context.Background(), w.writers(), "grant-1", "", 900); !errors.Is(err, errStoreDown) {
			t.Fatalf("CancelGrant() error = %v, want errStoreDown", err)
		}
		if len(w.calls) != 0 {
			t.Fatalf("calls = %v, want none", w.calls)
		}
	})
	t.Run("no writers", func(t *testing.T) {
		t.Parallel()
		l, _ := opsFixture(t)
		if err := l.CancelGrant(context.Background(), Writers{}, "grant-1", "", 900); err == nil {
			t.Fatal("CancelGrant() with no writers = nil, want an error")
		}
	})
}

func TestRevise_FitsRelinksEveryExecution(t *testing.T) {
	t.Parallel()
	l, w := opsFixture(t)
	next := revisedGrant(func(g *Grant) { g.Window = &Window{Start: 1000, Duration: 1800} })
	err := l.Revise(context.Background(), w.writers(), "grant-1", "revised", 900, func(old Grant) (Replacement, error) {
		if old.MRID != "grant-1" {
			t.Errorf("build got old %s, want grant-1", old.MRID)
		}
		return w.replacement(next), nil
	})
	if err != nil {
		t.Fatalf("Revise() error = %v", err)
	}
	want := []string{"create:grant-2", "relink:ctrl-b->grant-2", "relink:ctrl-a->grant-2", "mark:grant-1"}
	if !slices.Equal(w.calls, want) {
		t.Fatalf("calls = %v, want %v", w.calls, want)
	}
	for _, c := range w.controls.controls {
		if c.GrantMRID != "grant-2" || c.Cancelled {
			t.Errorf("%s = linked %q cancelled %v, want linked grant-2 and live", c.MRID, c.GrantMRID, c.Cancelled)
		}
	}
	if old := w.grants.grants[0]; old.CancelledAt == nil || *old.CancelledAt != 900 {
		t.Errorf("old grant CancelledAt = %v, want 900", old.CancelledAt)
	}
}

func TestRevise_RefusedNamesFirstExecutionThatDoesNotFit(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		mutate   func(*Grant)
		wantCode ConflictCode
		wantMRID string
	}{
		{
			// Both executions fall outside; ctrl-b starts first.
			name:     "window moved past both",
			mutate:   func(g *Grant) { g.Window = &Window{Start: 3000, Duration: 600} },
			wantCode: ConflictOutsideInterval, wantMRID: "ctrl-b",
		},
		{
			name:     "window shrunk below the later one",
			mutate:   func(g *Grant) { g.Window = &Window{Start: 1000, Duration: 1599} },
			wantCode: ConflictOutsideInterval, wantMRID: "ctrl-a",
		},
		{
			// 1000 W x 600 s each is 600000 Ws; 200 Wh is 720000 Ws.
			name:     "energy holds only the first",
			mutate:   func(g *Grant) { g.Energy = &sep2.SignedRealEnergy{Value: 200} },
			wantCode: ConflictEnergy, wantMRID: "ctrl-a",
		},
		{
			name:     "direction reversed names the first",
			mutate:   func(g *Grant) { g.Energy = &sep2.SignedRealEnergy{Value: -10000} },
			wantCode: ConflictDirection, wantMRID: "ctrl-b",
		},
		{
			name:     "grant no longer executable names the first",
			mutate:   func(g *Grant) { g.Power = nil },
			wantCode: ConflictNotExecutable, wantMRID: "ctrl-b",
		},
		{
			name:     "power below one execution names the first",
			mutate:   func(g *Grant) { g.Power = &sep2.ActivePower{Value: 999} },
			wantCode: ConflictPower, wantMRID: "ctrl-b",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			l, w := opsFixture(t)
			before := slices.Clone(w.controls.controls)
			err := l.Revise(context.Background(), w.writers(), "grant-1", "", 900, func(Grant) (Replacement, error) {
				return w.replacement(revisedGrant(tc.mutate)), nil
			})
			wantConflict(t, err, tc.wantCode, tc.wantMRID)
			if len(w.calls) != 0 {
				t.Fatalf("calls = %v, want none: a refused revision changes nothing", w.calls)
			}
			if !slices.Equal(w.controls.controls, before) || len(w.grants.grants) != 1 || w.grants.grants[0].CancelledAt != nil {
				t.Fatal("a refused revision changed a stored commitment")
			}
		})
	}
}

func TestRevise_RefusedByAnotherGrantOfTheFleet(t *testing.T) {
	t.Parallel()
	l, w := opsFixture(t)
	other := grantOn("grant-other", Window{Start: 4600, Duration: 600})
	w.grants.grants = append(w.grants.grants, other)
	err := l.Revise(context.Background(), w.writers(), "grant-1", "", 900, func(Grant) (Replacement, error) {
		return w.replacement(revisedGrant(func(g *Grant) { g.Window = &Window{Start: 1000, Duration: 3601} })), nil
	})
	wantConflict(t, err, ConflictFleetWindow, "grant-other")
	if len(w.calls) != 0 {
		t.Fatalf("calls = %v, want none", w.calls)
	}
}

func TestRevise_UndoOnFailedStep(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		failOn    string
		wantCalls []string
	}{
		{
			name:      "create",
			failOn:    "create:grant-2",
			wantCalls: []string{"create:grant-2"},
		},
		{
			name:      "second relink",
			failOn:    "relink:ctrl-a->grant-2",
			wantCalls: []string{"create:grant-2", "relink:ctrl-b->grant-2", "relink:ctrl-a->grant-2", "relink:ctrl-a->grant-1", "relink:ctrl-b->grant-1", "delete:grant-2"},
		},
		{
			name:   "mark old",
			failOn: "mark:grant-1",
			wantCalls: []string{"create:grant-2", "relink:ctrl-b->grant-2", "relink:ctrl-a->grant-2", "mark:grant-1",
				"relink:ctrl-a->grant-1", "relink:ctrl-b->grant-1", "delete:grant-2"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			l, w := opsFixture(t)
			before := slices.Clone(w.controls.controls)
			w.failOn = map[string]bool{tc.failOn: true}
			err := l.Revise(context.Background(), w.writers(), "grant-1", "", 900, func(Grant) (Replacement, error) {
				return w.replacement(revisedGrant(func(*Grant) {})), nil
			})
			if !errors.Is(err, errWrite) || errors.Is(err, ErrUndo) {
				t.Fatalf("Revise() error = %v, want errWrite and no ErrUndo", err)
			}
			if !slices.Equal(w.calls, tc.wantCalls) {
				t.Fatalf("calls = %v, want %v", w.calls, tc.wantCalls)
			}
			if !slices.Equal(w.controls.controls, before) || len(w.grants.grants) != 1 || w.grants.grants[0].CancelledAt != nil {
				t.Fatalf("after undo controls = %+v grants = %+v, want the state before", w.controls.controls, w.grants.grants)
			}
		})
	}
}

func TestRevise_FailedUndoIsErrUndo(t *testing.T) {
	t.Parallel()
	l, w := opsFixture(t)
	w.failOn = map[string]bool{"mark:grant-1": true, "delete:grant-2": true}
	err := l.Revise(context.Background(), w.writers(), "grant-1", "", 900, func(Grant) (Replacement, error) {
		return w.replacement(revisedGrant(func(*Grant) {})), nil
	})
	if !errors.Is(err, ErrUndo) || !errors.Is(err, errWrite) {
		t.Fatalf("Revise() error = %v, want ErrUndo wrapping errWrite", err)
	}
}

func TestRevise_ToZeroDurationIsADenial(t *testing.T) {
	t.Parallel()
	l, w := opsFixture(t)
	next := revisedGrant(func(g *Grant) { g.Window = &Window{Start: 1000} })
	err := l.Revise(context.Background(), w.writers(), "grant-1", "withdrawn", 900, func(Grant) (Replacement, error) {
		return w.replacement(next), nil
	})
	if err != nil {
		t.Fatalf("Revise() error = %v", err)
	}
	want := []string{"cancel:ctrl-b", "cancel:ctrl-a", "create:grant-2", "mark:grant-1"}
	if !slices.Equal(w.calls, want) {
		t.Fatalf("calls = %v, want %v", w.calls, want)
	}
	for _, c := range w.controls.controls {
		if !c.Cancelled || c.GrantMRID != "grant-1" {
			t.Errorf("%s = linked %q cancelled %v, want cancelled and still linked to grant-1", c.MRID, c.GrantMRID, c.Cancelled)
		}
	}
}

func TestRevise_DenialUndoDeletesTheNewResponse(t *testing.T) {
	t.Parallel()
	l, w := opsFixture(t)
	w.failOn = map[string]bool{"mark:grant-1": true}
	err := l.Revise(context.Background(), w.writers(), "grant-1", "", 900, func(Grant) (Replacement, error) {
		return w.replacement(revisedGrant(func(g *Grant) { g.Window = &Window{Start: 1000} })), nil
	})
	if !errors.Is(err, errWrite) {
		t.Fatalf("Revise() error = %v, want errWrite", err)
	}
	if want := []string{"cancel:ctrl-b", "cancel:ctrl-a", "create:grant-2", "mark:grant-1", "delete:grant-2"}; !slices.Equal(w.calls, want) {
		t.Fatalf("calls = %v, want %v", w.calls, want)
	}
	if len(w.grants.grants) != 1 || w.grants.grants[0].CancelledAt != nil {
		t.Fatalf("grants = %+v, want only the old one, live", w.grants.grants)
	}
}

func TestRevise_Refusals(t *testing.T) {
	t.Parallel()
	errBuild := errors.New("build failed")
	cases := []struct {
		name  string
		setup func(*fakeWriters)
		build func(*fakeWriters) func(Grant) (Replacement, error)
		check func(*testing.T, error)
	}{
		{
			name:  "cancelled grant",
			setup: func(w *fakeWriters) { at := int64(5); w.grants.grants[0].CancelledAt = &at },
			build: func(w *fakeWriters) func(Grant) (Replacement, error) {
				return func(Grant) (Replacement, error) { return w.replacement(revisedGrant(func(*Grant) {})), nil }
			},
			check: func(t *testing.T, err error) { wantConflict(t, err, ConflictGrantNotLive, "grant-1") },
		},
		{
			name: "build fails",
			build: func(*fakeWriters) func(Grant) (Replacement, error) {
				return func(Grant) (Replacement, error) { return Replacement{}, errBuild }
			},
			check: func(t *testing.T, err error) {
				if !errors.Is(err, errBuild) {
					t.Fatalf("error = %v, want errBuild", err)
				}
			},
		},
		{
			name: "same mRID",
			build: func(w *fakeWriters) func(Grant) (Replacement, error) {
				return func(Grant) (Replacement, error) {
					return w.replacement(revisedGrant(func(g *Grant) { g.MRID = "grant-1" })), nil
				}
			},
			check: wantBadReplacement,
		},
		{
			name: "empty mRID",
			build: func(w *fakeWriters) func(Grant) (Replacement, error) {
				return func(Grant) (Replacement, error) {
					return w.replacement(revisedGrant(func(g *Grant) { g.MRID = "" })), nil
				}
			},
			check: wantBadReplacement,
		},
		{
			name: "another EndDevice",
			build: func(w *fakeWriters) func(Grant) (Replacement, error) {
				return func(Grant) (Replacement, error) {
					return w.replacement(revisedGrant(func(g *Grant) { g.EndDeviceID = "edev-other" })), nil
				}
			},
			check: wantBadReplacement,
		},
		{
			name: "another subject",
			build: func(w *fakeWriters) func(Grant) (Replacement, error) {
				return func(Grant) (Replacement, error) {
					return w.replacement(revisedGrant(func(g *Grant) { g.Subject = "SUBJ-2" })), nil
				}
			},
			check: wantBadReplacement,
		},
		{
			name:  "empty subject on both",
			setup: func(w *fakeWriters) { w.grants.grants[0].Subject = "" },
			build: func(w *fakeWriters) func(Grant) (Replacement, error) {
				return func(Grant) (Replacement, error) {
					return w.replacement(revisedGrant(func(g *Grant) { g.Subject = "" })), nil
				}
			},
			check: wantBadReplacement,
		},
		{
			name: "same creationTime",
			build: func(w *fakeWriters) func(Grant) (Replacement, error) {
				return func(Grant) (Replacement, error) {
					return w.replacement(revisedGrant(func(g *Grant) { g.CreationTime = 100 })), nil
				}
			},
			check: wantBadReplacement,
		},
		{
			name: "earlier creationTime",
			build: func(w *fakeWriters) func(Grant) (Replacement, error) {
				return func(Grant) (Replacement, error) {
					return w.replacement(revisedGrant(func(g *Grant) { g.CreationTime = 99 })), nil
				}
			},
			check: wantBadReplacement,
		},
		{
			name: "no delete",
			build: func(w *fakeWriters) func(Grant) (Replacement, error) {
				return func(Grant) (Replacement, error) {
					r := w.replacement(revisedGrant(func(*Grant) {}))
					r.Delete = nil
					return r, nil
				}
			},
			check: wantBadReplacement,
		},
		{
			name: "no create",
			build: func(w *fakeWriters) func(Grant) (Replacement, error) {
				return func(Grant) (Replacement, error) {
					r := w.replacement(revisedGrant(func(*Grant) {}))
					r.Create = nil
					return r, nil
				}
			},
			check: wantBadReplacement,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			l, w := opsFixture(t)
			if tc.setup != nil {
				tc.setup(w)
			}
			err := l.Revise(context.Background(), w.writers(), "grant-1", "", 900, tc.build(w))
			tc.check(t, err)
			if len(w.calls) != 0 {
				t.Fatalf("calls = %v, want none", w.calls)
			}
		})
	}
}

// wantPlainError asserts a refusal that is neither a conflict nor nil: a
// malformed replacement is the caller's bug, not a commitment conflict.
func wantPlainError(t *testing.T, err error) {
	t.Helper()
	var ce *ConflictError
	if err == nil || errors.As(err, &ce) {
		t.Fatalf("error = %v, want a plain error", err)
	}
}

// wantBadReplacement asserts a refusal of a malformed replacement: a plain
// error wrapping ErrBadReplacement, never a conflict.
func wantBadReplacement(t *testing.T, err error) {
	t.Helper()
	wantPlainError(t, err)
	if !errors.Is(err, ErrBadReplacement) {
		t.Fatalf("error = %v, want ErrBadReplacement", err)
	}
}

func TestRevise_UndoCoversARelinkThatLandedBeforeFailing(t *testing.T) {
	t.Parallel()
	l, w := opsFixture(t)
	w.applyThenFail = map[string]bool{"relink:ctrl-a->grant-2": true}
	err := l.Revise(context.Background(), w.writers(), "grant-1", "", 900, func(Grant) (Replacement, error) {
		return w.replacement(revisedGrant(func(*Grant) {})), nil
	})
	if !errors.Is(err, errWrite) || errors.Is(err, ErrUndo) {
		t.Fatalf("Revise() error = %v, want errWrite and no ErrUndo", err)
	}
	for _, c := range w.controls.controls {
		if c.GrantMRID != "grant-1" {
			t.Errorf("%s linked to %q after the revision was undone, want grant-1", c.MRID, c.GrantMRID)
		}
	}
	if len(w.grants.grants) != 1 {
		t.Fatalf("grants = %+v, want only grant-1", w.grants.grants)
	}

	w.calls, w.applyThenFail = nil, nil
	if err := l.CancelGrant(context.Background(), w.writers(), "grant-1", "", 950); err != nil {
		t.Fatalf("CancelGrant() error = %v", err)
	}
	for _, c := range w.controls.controls {
		if !c.Cancelled {
			t.Errorf("%s still live after its grant was cancelled", c.MRID)
		}
	}
}

func TestRevise_RefusesAGrantThatCannotBeCarriedOut(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		mutate func(*Grant)
	}{
		{"no power", func(g *Grant) { g.Power = nil }},
		{"no energy", func(g *Grant) { g.Energy = nil }},
		{"no interval", func(g *Grant) { g.Window = nil }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			l, w := opsFixture(t)
			w.controls.controls = nil
			err := l.Revise(context.Background(), w.writers(), "grant-1", "", 900, func(Grant) (Replacement, error) {
				return w.replacement(revisedGrant(tc.mutate)), nil
			})
			wantConflict(t, err, ConflictNotExecutable, "grant-2")
			if len(w.calls) != 0 {
				t.Fatalf("calls = %v, want none", w.calls)
			}
		})
	}
}

func TestRevise_RefusesAnOldResponseThatIsNotALiveGrant(t *testing.T) {
	t.Parallel()
	cancelledAt := int64(5)
	cases := []struct {
		name   string
		mutate func(*Grant)
	}{
		{"denial", func(g *Grant) { g.Window = &Window{Start: 1000} }},
		{"no interval", func(g *Grant) { g.Window = nil }},
		{"cancelled", func(g *Grant) { g.CancelledAt = &cancelledAt }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			l, w := opsFixture(t)
			tc.mutate(&w.grants.grants[0])
			built := false
			err := l.Revise(context.Background(), w.writers(), "grant-1", "", 900, func(Grant) (Replacement, error) {
				built = true
				return w.replacement(revisedGrant(func(*Grant) {})), nil
			})
			wantConflict(t, err, ConflictGrantNotLive, "grant-1")
			if built || len(w.calls) != 0 {
				t.Fatalf("built = %v, calls = %v, want neither", built, w.calls)
			}
		})
	}
}

// The undo must not inherit a caller context that is already done: the
// fake refuses a done context as Issuer.Relink does, and the failing step
// cancels the caller's context.
func TestRevise_UndoSurvivesACancelledCallerContext(t *testing.T) {
	t.Parallel()
	for _, failOn := range []string{"mark:grant-1", "relink:ctrl-a->grant-2"} {
		t.Run(failOn, func(t *testing.T) {
			t.Parallel()
			l, w := opsFixture(t)
			before := slices.Clone(w.controls.controls)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			w.honorCtx, w.cancelOnFail, w.failOn = true, cancel, map[string]bool{failOn: true}
			err := l.Revise(ctx, w.writers(), "grant-1", "", 900, func(Grant) (Replacement, error) {
				return w.replacement(revisedGrant(func(*Grant) {})), nil
			})
			if !errors.Is(err, errWrite) || errors.Is(err, ErrUndo) {
				t.Fatalf("Revise() error = %v, want errWrite and a completed undo", err)
			}
			if !slices.Equal(w.controls.controls, before) || len(w.grants.grants) != 1 {
				t.Fatalf("after undo controls = %+v grants = %+v, want the state before", w.controls.controls, w.grants.grants)
			}
		})
	}
	t.Run("denial", func(t *testing.T) {
		t.Parallel()
		l, w := opsFixture(t)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		w.honorCtx, w.cancelOnFail, w.failOn = true, cancel, map[string]bool{"mark:grant-1": true}
		err := l.Revise(ctx, w.writers(), "grant-1", "", 900, func(Grant) (Replacement, error) {
			return w.replacement(revisedGrant(func(g *Grant) { g.Window = &Window{Start: 1000} })), nil
		})
		if !errors.Is(err, errWrite) || errors.Is(err, ErrUndo) {
			t.Fatalf("Revise() error = %v, want errWrite and a completed undo", err)
		}
		if len(w.grants.grants) != 1 {
			t.Fatalf("grants = %+v, want the denial deleted", w.grants.grants)
		}
	})
}

func TestRevise_FailedUndoStepsAreErrUndo(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		failOn []string
		window *Window
	}{
		{"relink back fails", []string{"mark:grant-1", "relink:ctrl-a->grant-1"}, nil},
		{"denial delete fails", []string{"mark:grant-1", "delete:grant-2"}, &Window{Start: 1000}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			l, w := opsFixture(t)
			w.failOn = map[string]bool{}
			for _, c := range tc.failOn {
				w.failOn[c] = true
			}
			err := l.Revise(context.Background(), w.writers(), "grant-1", "", 900, func(Grant) (Replacement, error) {
				return w.replacement(revisedGrant(func(g *Grant) {
					if tc.window != nil {
						g.Window = tc.window
					}
				})), nil
			})
			if !errors.Is(err, ErrUndo) || !errors.Is(err, errWrite) {
				t.Fatalf("Revise() error = %v, want ErrUndo wrapping errWrite", err)
			}
		})
	}
}

// Executions with one start are ordered by mRID, whatever order the source
// lists them in.
func TestRevise_EqualStartsNameTheLowerMRID(t *testing.T) {
	t.Parallel()
	l, w := opsFixture(t)
	z := baseExecution()
	z.MRID, z.Window = "ctrl-z", Window{Start: 1000, Duration: 600}
	y := baseExecution()
	y.MRID, y.Window = "ctrl-y", Window{Start: 1000, Duration: 600}
	w.controls.controls = []Control{z, y}
	err := l.Revise(context.Background(), w.writers(), "grant-1", "", 900, func(Grant) (Replacement, error) {
		return w.replacement(revisedGrant(func(g *Grant) { g.Window = &Window{Start: 3000, Duration: 600} })), nil
	})
	wantConflict(t, err, ConflictOutsideInterval, "ctrl-y")
}

// movingGrants answers the ledger's first read of a grant with fleet
// FLEET1 and every later read with FLEET2, as a management change between
// the unlocked read and the locked one would.
type movingGrants struct {
	*fakeGrants
	reads int
}

func (m *movingGrants) Grant(ctx context.Context, mrid string) (Grant, error) {
	g, err := m.fakeGrants.Grant(ctx, mrid)
	m.reads++
	if m.reads > 1 {
		g.FleetKey = "FLEET2"
	}
	return g, err
}

func TestOperations_RefuseAGrantThatMovedFleet(t *testing.T) {
	t.Parallel()
	ops := map[string]func(*Ledger, *fakeWriters) error{
		"CancelGrant": func(l *Ledger, w *fakeWriters) error {
			return l.CancelGrant(context.Background(), w.writers(), "grant-1", "", 900)
		},
		"Revise": func(l *Ledger, w *fakeWriters) error {
			return l.Revise(context.Background(), w.writers(), "grant-1", "", 900, func(Grant) (Replacement, error) {
				return w.replacement(revisedGrant(func(*Grant) {})), nil
			})
		},
	}
	for name, op := range ops {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			_, w := opsFixture(t)
			l := NewLedger(&movingGrants{fakeGrants: w.grants}, w.controls)
			w.ledger = l
			err := op(l, w)
			var ce *ConflictError
			if err == nil || errors.As(err, &ce) {
				t.Fatalf("error = %v, want a plain error for a grant that moved fleet", err)
			}
			if len(w.calls) != 0 {
				t.Fatalf("calls = %v, want none", w.calls)
			}
		})
	}
}

// FitsGrant with no proposal names the first active execution in slice
// order on a power conflict; Revise depends on that being deterministic.
func TestFitsGrant_NoProposalPowerNamesFirstActive(t *testing.T) {
	t.Parallel()
	g := baseGrant()
	g.Power = &sep2.ActivePower{Value: 1999}
	a := baseExecution()
	a.MRID = "ctrl-a"
	b := baseExecution()
	b.MRID = "ctrl-b"
	wantConflict(t, FitsGrant(g, []Control{a, b}, nil), ConflictPower, "ctrl-a")
	wantConflict(t, FitsGrant(g, []Control{b, a}, nil), ConflictPower, "ctrl-b")
}
