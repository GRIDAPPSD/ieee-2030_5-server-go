//go:build csip_test_hooks

package server

import (
	"context"
	"errors"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

// failingDelete refuses to delete from the old scope, so a re-key fails after
// its create while an undo at the new scope still works.
type failingDelete struct {
	store.ScopedStore[sep2.DefaultDERControl]
}

func (f failingDelete) Delete(ctx context.Context, parent, id string) error {
	if parent == "e/old/p" {
		return errors.New("delete refused")
	}
	return f.ScopedStore.Delete(ctx, parent, id)
}

// A re-key whose delete fails must not leave a second copy at the new scope.
func TestMoveDefaultControl_FailedDeleteUndoesTheCreate(t *testing.T) {
	ctx := context.Background()
	inner := memory.NewScopedStore[sep2.DefaultDERControl]()
	dd := sep2.DefaultDERControl{MRID: "DD-1"}
	dd.Href = "/edev/e/fsa/old/derp/p/dderc"
	if err := inner.Create(ctx, "e/old/p", "default", dd); err != nil {
		t.Fatalf("seed: %v", err)
	}
	stores := &Stores{DefaultDERControls: failingDelete{inner}}

	err := moveDefaultControl(ctx, stores, "e/old/p", "e/new/p", func(h string) string { return h })
	if err == nil {
		t.Fatal("moveDefaultControl succeeded, want the delete error")
	}
	if _, err := inner.Get(ctx, "e/new/p", "default"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("default control at the new scope = %v, want ErrNotFound after the undo", err)
	}
	got, err := inner.Get(ctx, "e/old/p", "default")
	if err != nil || got.MRID != "DD-1" {
		t.Errorf("default control at the old scope = %+v, %v, want MRID DD-1 untouched", got, err)
	}
}
