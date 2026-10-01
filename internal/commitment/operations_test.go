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
// "create:<mrid>", "delete:<mrid>") that returns errWrite instead.
type fakeWriters struct {
	grants   *fakeGrants
	controls *fakeControls
	calls    []string
	failOn   map[string]bool
}

var errWrite = errors.New("fake: write failed")

func (f *fakeWriters) record(call string) error {
	f.calls = append(f.calls, call)
	if f.failOn[call] {
		return errWrite
	}
	return nil
}

func (f *fakeWriters) control(mrid string) *Control {
	for i := range f.controls.controls {
		if f.controls.controls[i].MRID == mrid {
			return &f.controls.controls[i]
		}
	}
	panic("fake: no control " + mrid)
}

func (f *fakeWriters) CancelExecution(_ context.Context, c Control, _ string) error {
	if err := f.record("cancel:" + c.MRID); err != nil {
		return err
	}
	f.control(c.MRID).Cancelled = true
	return nil
}

func (f *fakeWriters) RelinkExecution(_ context.Context, c Control, grantMRID string) error {
	if err := f.record("relink:" + c.MRID + "->" + grantMRID); err != nil {
		return err
	}
	f.control(c.MRID).GrantMRID = grantMRID
	return nil
}

func (f *fakeWriters) MarkCancelled(_ context.Context, g Grant, _ string, now int64) error {
	if err := f.record("mark:" + g.MRID); err != nil {
		return err
	}
	for i := range f.grants.grants {
		if f.grants.grants[i].MRID == g.MRID {
			f.grants.grants[i].CancelledAt = &now
		}
	}
	return nil
}

func (f *fakeWriters) writers() Writers {
	return Writers{Executions: f, Grants: f}
}

// replacement returns a Replacement whose Create and Delete add and remove
// g in the fake grant source.
func (f *fakeWriters) replacement(g Grant) Replacement {
	return Replacement{
		Grant: g,
		Create: func(context.Context) error {
			if err := f.record("create:" + g.MRID); err != nil {
				return err
			}
			f.grants.grants = append(f.grants.grants, g)
			return nil
		},
		Delete: func(context.Context) error {
			if err := f.record("delete:" + g.MRID); err != nil {
				return err
			}
			f.grants.grants = slices.DeleteFunc(f.grants.grants, func(x Grant) bool { return x.MRID == g.MRID })
			return nil
		},
	}
}

// opsFixture: grant-1 on [1000, 4600) with two executions, listed in the
// source in the reverse of their time order (ctrl-b starts at 1000,
// ctrl-a at 2000), so a test naming "the first" proves the ledger sorts.
func opsFixture() (*Ledger, *fakeWriters) {
	g := baseGrant()
	a := baseExecution()
	a.MRID, a.Window = "ctrl-a", Window{Start: 2000, Duration: 600}
	b := baseExecution()
	b.MRID, b.Window = "ctrl-b", Window{Start: 1000, Duration: 600}
	grants := &fakeGrants{grants: []Grant{g}}
	controls := &fakeControls{controls: []Control{a, b}}
	return NewLedger(grants, controls), &fakeWriters{grants: grants, controls: controls}
}

func revisedGrant(mutate func(*Grant)) Grant {
	g := baseGrant()
	g.MRID = "grant-2"
	mutate(&g)
	return g
}

func TestCancelGrant_ExecutionsFirstThenGrant(t *testing.T) {
	t.Parallel()
	l, w := opsFixture()
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
	l, w := opsFixture()
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
	l, w := opsFixture()
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
		l, w := opsFixture()
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
		l, w := opsFixture()
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
		l, w := opsFixture()
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
		l, _ := opsFixture()
		if err := l.CancelGrant(context.Background(), Writers{}, "grant-1", "", 900); err == nil {
			t.Fatal("CancelGrant() with no writers = nil, want an error")
		}
	})
}

func TestRevise_FitsRelinksEveryExecution(t *testing.T) {
	t.Parallel()
	l, w := opsFixture()
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
			l, w := opsFixture()
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
	l, w := opsFixture()
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
			wantCalls: []string{"create:grant-2", "relink:ctrl-b->grant-2", "relink:ctrl-a->grant-2", "relink:ctrl-b->grant-1", "delete:grant-2"},
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
			l, w := opsFixture()
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
	l, w := opsFixture()
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
	l, w := opsFixture()
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
	l, w := opsFixture()
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
			check: wantPlainError,
		},
		{
			name: "empty mRID",
			build: func(w *fakeWriters) func(Grant) (Replacement, error) {
				return func(Grant) (Replacement, error) {
					return w.replacement(revisedGrant(func(g *Grant) { g.MRID = "" })), nil
				}
			},
			check: wantPlainError,
		},
		{
			name: "another EndDevice",
			build: func(w *fakeWriters) func(Grant) (Replacement, error) {
				return func(Grant) (Replacement, error) {
					return w.replacement(revisedGrant(func(g *Grant) { g.EndDeviceID = "edev-other" })), nil
				}
			},
			check: wantPlainError,
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
			check: wantPlainError,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			l, w := opsFixture()
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
