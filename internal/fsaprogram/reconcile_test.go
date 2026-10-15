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

func adminWith(t *testing.T, key, mrid string, devices ...string) *memory.AdminFSAStore {
	t.Helper()
	ctx := context.Background()
	admin := memory.NewAdminFSAStore()
	if err := admin.Create(ctx, key, sep2.FunctionSetAssignments{MRID: mrid, Description: "d"}); err != nil {
		t.Fatal(err)
	}
	for _, d := range devices {
		if err := admin.AssignDevice(ctx, key, d); err != nil {
			t.Fatal(err)
		}
	}
	return admin
}

func TestReconcile(t *testing.T) {
	ctx := context.Background()
	admin := adminWith(t, "f", "f", "1", "2")
	dev := memory.NewScopedStore[sep2.FunctionSetAssignments]()
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

func TestReconcile_ReplacesExistingRecord(t *testing.T) {
	ctx := context.Background()
	admin := adminWith(t, "f", "f", "1")
	dev := memory.NewScopedStore[sep2.FunctionSetAssignments]()
	if err := dev.Create(ctx, "1", "f", sep2.FunctionSetAssignments{MRID: "other", Description: "stale"}); err != nil {
		t.Fatal(err)
	}
	if err := Reconcile(ctx, admin, dev); err != nil {
		t.Fatal(err)
	}
	got, err := dev.Get(ctx, "1", "f")
	if err != nil || got.MRID != "f" || got.Description != "d" {
		t.Errorf("record = %+v, %v, want the admin record", got, err)
	}
}

func TestReconcile_KeyDiffersFromMRID(t *testing.T) {
	ctx := context.Background()
	admin := adminWith(t, "key-A", "MRa", "1")
	dev := memory.NewScopedStore[sep2.FunctionSetAssignments]()
	if err := Reconcile(ctx, admin, dev); err != nil {
		t.Fatal(err)
	}
	got, err := dev.Get(ctx, "1", "key-A")
	if err != nil || got.Href != "/edev/1/fsa/key-A" || got.MRID != "MRa" {
		t.Errorf("record = %+v, %v, want one under the store key", got, err)
	}
}

func TestReconcile_EmptyIsNoop(t *testing.T) {
	dev := memory.NewScopedStore[sep2.FunctionSetAssignments]()
	if err := Reconcile(context.Background(), memory.NewAdminFSAStore(), dev); err != nil {
		t.Fatal(err)
	}
}

type failingWriter struct{ createErr, deleteErr error }

func (w failingWriter) Create(context.Context, string, string, sep2.FunctionSetAssignments) error {
	return w.createErr
}
func (w failingWriter) Delete(context.Context, string, string) error { return w.deleteErr }

func TestReconcile_WriteErrorReachesCaller(t *testing.T) {
	boom := errors.New("disk full")
	admin := adminWith(t, "f", "f", "1")
	for name, w := range map[string]failingWriter{
		"create fails":                {createErr: boom},
		"delete fails after conflict": {createErr: store.ErrAlreadyExists, deleteErr: boom},
	} {
		t.Run(name, func(t *testing.T) {
			err := Reconcile(context.Background(), admin, w)
			if !errors.Is(err, boom) {
				t.Fatalf("err = %v, want it to wrap %v", err, boom)
			}
		})
	}
}
