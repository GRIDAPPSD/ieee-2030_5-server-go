package fsaprogram

import (
	"context"
	"errors"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

var errBoom = errors.New("boom")

type failCreate struct{ DeviceFSAWriter }

func (failCreate) Create(context.Context, string, string, sep2.FunctionSetAssignments) error {
	return errBoom
}

func partsOf(steps []Step) []string {
	var out []string
	for _, s := range steps {
		out = append(out, s.Part)
	}
	return out
}

func TestAssign_WritesBothAndNamesTheUndoSteps(t *testing.T) {
	ctx := context.Background()
	admin := adminWith(t, "f", "f")
	dev := memory.NewScopedStore[sep2.FunctionSetAssignments]()
	fsa, _ := admin.Get(ctx, "f")

	steps, err := Assign(ctx, admin, dev, "3", "f", fsa, RefuseIfAssigned)
	if err != nil {
		t.Fatal(err)
	}
	if got := partsOf(steps); len(got) != 2 || got[0] != "assignment" || got[1] != "scoped_fsa" {
		t.Fatalf("steps = %v, want [assignment scoped_fsa]", got)
	}
	if got, err := dev.Get(ctx, "3", "f"); err != nil || got.Href != "/edev/3/fsa/f" {
		t.Errorf("scoped = %+v, %v", got, err)
	}
	for i := len(steps) - 1; i >= 0; i-- {
		if err := steps[i].Undo(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if devs := admin.Devices(ctx, "f"); len(devs) != 0 {
		t.Errorf("devices after undo = %v", devs)
	}
	if _, err := dev.Get(ctx, "3", "f"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("scoped after undo err = %v, want ErrNotFound", err)
	}
}

func TestAssign_ExistingAssignment(t *testing.T) {
	ctx := context.Background()
	admin := adminWith(t, "f", "f", "3")
	dev := memory.NewScopedStore[sep2.FunctionSetAssignments]()
	fsa, _ := admin.Get(ctx, "f")

	steps, err := Assign(ctx, admin, dev, "3", "f", fsa, RefuseIfAssigned)
	var ae *AssignError
	if !errors.As(err, &ae) || ae.Stage != StageAssign || !errors.Is(err, store.ErrAlreadyExists) {
		t.Fatalf("RefuseIfAssigned err = %v, want an assign-stage ErrAlreadyExists", err)
	}
	if len(steps) != 0 {
		t.Errorf("refused steps = %v, want none", partsOf(steps))
	}
	if _, err := dev.Get(ctx, "3", "f"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("a refused assign wrote the scoped FSA: %v", err)
	}

	steps, err = Assign(ctx, admin, dev, "3", "f", fsa, CompleteIfAssigned)
	if err != nil {
		t.Fatal(err)
	}
	if got := partsOf(steps); len(got) != 1 || got[0] != "scoped_fsa" {
		t.Errorf("completing steps = %v, want [scoped_fsa]: the old assignment is not ours to undo", got)
	}
	if _, err := dev.Get(ctx, "3", "f"); err != nil {
		t.Errorf("scoped FSA after completing: %v", err)
	}
}

func TestAssign_NilDevicesSkipsTheScopedWrite(t *testing.T) {
	ctx := context.Background()
	admin := adminWith(t, "f", "f")
	steps, err := Assign(ctx, admin, nil, "3", "f", sep2.FunctionSetAssignments{MRID: "f"}, RefuseIfAssigned)
	if err != nil || len(steps) != 1 || steps[0].Part != "assignment" {
		t.Errorf("steps = %v, err = %v, want only the assignment", partsOf(steps), err)
	}
}

func TestAssign_ScopedFailureReturnsTheAssignmentStep(t *testing.T) {
	ctx := context.Background()
	admin := adminWith(t, "f", "f")
	dev := failCreate{memory.NewScopedStore[sep2.FunctionSetAssignments]()}
	steps, err := Assign(ctx, admin, dev, "3", "f", sep2.FunctionSetAssignments{MRID: "f"}, CompleteIfAssigned)
	var ae *AssignError
	if !errors.As(err, &ae) || ae.Stage != StageScopedWrite || !errors.Is(err, errBoom) {
		t.Fatalf("err = %v, want a scope_materialize error wrapping the store's", err)
	}
	if got := partsOf(steps); len(got) != 1 || got[0] != "assignment" {
		t.Errorf("steps = %v, want [assignment] to undo", got)
	}
}
