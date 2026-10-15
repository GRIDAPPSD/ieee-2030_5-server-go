package fsaprogram

import (
	"context"
	"errors"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

func TestScoped_Fields(t *testing.T) {
	got := Scoped("3", "f", sep2.FunctionSetAssignments{MRID: "f", Description: "d"})
	if got.Href != "/edev/3/fsa/f" || got.MRID != "f" || got.Description != "d" ||
		got.DERProgramListLink == nil || got.DERProgramListLink.Href != "/edev/3/fsa/f/derp" {
		t.Errorf("Scoped = %+v", got)
	}
}

func TestReconcile(t *testing.T) {
	ctx := context.Background()
	admin := memory.NewAdminFSAStore()
	if err := admin.Create(ctx, "f", sep2.FunctionSetAssignments{MRID: "f", Description: "d"}); err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{"1", "2"} {
		if err := admin.AssignDevice(ctx, "f", d); err != nil {
			t.Fatal(err)
		}
	}

	dev := memory.NewScopedStore[sep2.FunctionSetAssignments]()
	// A record already present must not turn the rebuild into a failure.
	if err := dev.Create(ctx, "1", "f", Scoped("1", "f", sep2.FunctionSetAssignments{MRID: "f", Description: "d"})); err != nil {
		t.Fatal(err)
	}
	if err := Reconcile(ctx, admin, dev); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	for _, d := range []string{"1", "2"} {
		got, err := dev.Get(ctx, d, "f")
		if err != nil || got.Href != "/edev/"+d+"/fsa/f" || got.Description != "d" {
			t.Errorf("device %s = %+v, %v", d, got, err)
		}
	}
	if _, err := dev.Get(ctx, "3", "f"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("unassigned device err = %v, want ErrNotFound", err)
	}
}

func TestReconcile_EmptyIsNoop(t *testing.T) {
	dev := memory.NewScopedStore[sep2.FunctionSetAssignments]()
	if err := Reconcile(context.Background(), memory.NewAdminFSAStore(), dev); err != nil {
		t.Fatal(err)
	}
}
