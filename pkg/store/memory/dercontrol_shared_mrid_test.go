package memory_test

import (
	"context"
	"errors"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

// TestDERControlStore_FanOutCopiesShareMRID: one FSA control is stored once
// per device under the same id and mRID (#972). The copies coexist, survive
// a reload, ByMRID refuses to pick one, and removing a copy leaves the
// others indexed.
func TestDERControlStore_FanOutCopiesShareMRID(t *testing.T) {
	s, path := newPersistedDERControlStore(t)
	ctx := context.Background()
	for _, parent := range []string{"d1/f/p", "d2/f/p", "d3/f/p"} {
		if err := s.Create(ctx, parent, "c1", mkControl("FAN", 100)); err != nil {
			t.Fatalf("Create %s: %v", parent, err)
		}
	}
	if _, _, _, err := s.ByMRID(ctx, "FAN"); !errors.Is(err, memory.ErrMRIDShared) {
		t.Fatalf("ByMRID of a shared mRID error = %v, want ErrMRIDShared", err)
	}

	revived, err := memory.NewDERControlStoreWithPersistence(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	for _, parent := range []string{"d1/f/p", "d2/f/p", "d3/f/p"} {
		got, err := revived.Get(ctx, parent, "c1")
		if err != nil || got.MRID != "FAN" {
			t.Fatalf("reloaded %s/c1 = %+v, %v; want mRID FAN", parent, got, err)
		}
	}

	if err := s.Delete(ctx, "d1/f/p", "c1"); err != nil {
		t.Fatalf("Delete d1: %v", err)
	}
	if err := s.Delete(ctx, "d2/f/p", "c1"); err != nil {
		t.Fatalf("Delete d2: %v", err)
	}
	parent, id, _, err := s.ByMRID(ctx, "FAN")
	if err != nil || parent != "d3/f/p" || id != "c1" {
		t.Fatalf("ByMRID after two deletes = (%q, %q, %v), want (d3/f/p, c1, nil)", parent, id, err)
	}
}

// TestDERControlStore_SharedMRIDNeedsSameID: sharing is only for copies of
// one control, so the same mRID under another id is still refused, in the
// same parent or another one.
func TestDERControlStore_SharedMRIDNeedsSameID(t *testing.T) {
	s, _ := newPersistedDERControlStore(t)
	ctx := context.Background()
	if err := s.Create(ctx, "d1/f/p", "c1", mkControl("FAN", 100)); err != nil {
		t.Fatalf("seed: %v", err)
	}
	for _, tc := range []struct{ parent, id string }{{"d1/f/p", "c2"}, {"d2/f/p", "c2"}} {
		err := s.Create(ctx, tc.parent, tc.id, mkControl("FAN", 200))
		if !errors.Is(err, memory.ErrMRIDConflict) {
			t.Errorf("Create %s/%s with a taken mRID = %v, want ErrMRIDConflict", tc.parent, tc.id, err)
		}
		if _, err := s.Get(ctx, tc.parent, tc.id); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("Get %s/%s after the refusal = %v, want ErrNotFound", tc.parent, tc.id, err)
		}
	}
}
