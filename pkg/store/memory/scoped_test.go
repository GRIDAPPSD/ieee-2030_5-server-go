package memory_test

import (
	"context"
	"errors"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

func TestScopedStoreIsolation(t *testing.T) {
	s := memory.NewScopedStore[testItem]()
	ctx := context.Background()

	// Add items to different parents
	_ = s.Create(ctx, "parent-A", "1", testItem{Name: "a1", Value: 1})
	_ = s.Create(ctx, "parent-A", "2", testItem{Name: "a2", Value: 2})
	_ = s.Create(ctx, "parent-B", "1", testItem{Name: "b1", Value: 10})

	// Parent A should have 2 items
	countA, _ := s.Count(ctx, "parent-A")
	if countA != 2 {
		t.Errorf("parent-A count = %d, want 2", countA)
	}

	// Parent B should have 1 item
	countB, _ := s.Count(ctx, "parent-B")
	if countB != 1 {
		t.Errorf("parent-B count = %d, want 1", countB)
	}

	// Get from correct parent
	got, err := s.Get(ctx, "parent-B", "1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "b1" {
		t.Errorf("got %q from parent-B, want b1", got.Name)
	}
}

func TestScopedStoreList(t *testing.T) {
	s := memory.NewScopedStore[testItem]()
	ctx := context.Background()

	for i := 0; i < 5; i++ {
		id := string(rune('a' + i))
		_ = s.Create(ctx, "p1", id, testItem{Name: id, Value: i})
	}

	result, err := s.List(ctx, "p1", store.ListOptions{Limit: 3})
	if err != nil {
		t.Fatal(err)
	}
	if result.All != 5 {
		t.Errorf("All = %d, want 5", result.All)
	}
	if result.Results != 3 {
		t.Errorf("Results = %d, want 3", result.Results)
	}
}

func TestScopedStoreEmptyParent(t *testing.T) {
	s := memory.NewScopedStore[testItem]()
	ctx := context.Background()

	result, err := s.List(ctx, "nonexistent", store.ListOptions{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if result.All != 0 {
		t.Errorf("empty parent All = %d, want 0", result.All)
	}
}

func TestScopedStoreDelete(t *testing.T) {
	s := memory.NewScopedStore[testItem]()
	ctx := context.Background()

	_ = s.Create(ctx, "p1", "a", testItem{Name: "a"})
	_ = s.Delete(ctx, "p1", "a")

	_, err := s.Get(ctx, "p1", "a")
	if !errors.Is(err, store.ErrNotFound) {
		t.Error("deleted item should not be found")
	}
}

func TestScopedStoreHasParent(t *testing.T) {
	s := memory.NewScopedStore[testItem]()
	ctx := context.Background()

	has, err := s.HasParent(ctx, "p1")
	if err != nil {
		t.Fatalf("HasParent: %v", err)
	}
	if has {
		t.Error("should not have parent before any operations")
	}

	_ = s.Create(ctx, "p1", "a", testItem{})

	has, err = s.HasParent(ctx, "p1")
	if err != nil {
		t.Fatalf("HasParent: %v", err)
	}
	if !has {
		t.Error("should have parent after create")
	}
}

// TestScopedStoreDeleteParentRemovesOnlyThatParentsResources is the cascade
// primitive HandleDeleteMirrorUsagePoint relies on.
//
// Two parents are populated, not one. A DeleteParent that dropped the whole
// stores map, or that cleared the wrong bucket, is indistinguishable from a
// correct one when only a single parent exists, and a cascade that took a
// sibling's children with it would be a silent deletion of another device's
// metering data.
func TestScopedStoreDeleteParentRemovesOnlyThatParentsResources(t *testing.T) {
	s := memory.NewScopedStore[testItem]()
	ctx := context.Background()

	_ = s.Create(ctx, "parent-A", "1", testItem{Name: "a1", Value: 1})
	_ = s.Create(ctx, "parent-A", "2", testItem{Name: "a2", Value: 2})
	_ = s.Create(ctx, "parent-B", "1", testItem{Name: "b1", Value: 10})

	removed, err := s.DeleteParent(ctx, "parent-A")
	if err != nil {
		t.Fatalf("DeleteParent: %v", err)
	}
	if removed != 2 {
		t.Errorf("removed = %d, want 2", removed)
	}

	// HasParent is the assertion that the ENTRY is gone, not just its contents:
	// Count answers zero for an absent parent and for an empty one alike, so a
	// Count alone would pass against a cascade that emptied the bucket and left
	// it in the map. It ran first for a second reason once, when a Count would
	// have recreated the entry under test; that hazard is gone.
	has, err := s.HasParent(ctx, "parent-A")
	if err != nil {
		t.Fatalf("HasParent: %v", err)
	}
	if has {
		t.Error("parent-A still present after DeleteParent")
	}

	if _, err := s.Get(ctx, "parent-A", "1"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("parent-A resource 1 still readable after DeleteParent (err = %v)", err)
	}

	// The sibling is untouched, values included.
	got, err := s.Get(ctx, "parent-B", "1")
	if err != nil {
		t.Fatalf("parent-B resource 1 was removed by a cascade scoped to parent-A: %v", err)
	}
	if got.Name != "b1" || got.Value != 10 {
		t.Errorf("parent-B resource 1 = %+v, want {b1 10} unchanged", got)
	}
	count, err := s.Count(ctx, "parent-B")
	if err != nil {
		t.Fatalf("Count: %v", err)
	}
	if count != 1 {
		t.Errorf("parent-B count = %d, want 1", count)
	}
}

// TestScopedStoreDeleteParentOnAnAbsentParentIsNotAnError pins the contract the
// cascade caller depends on: "no bucket" and "an empty bucket" are the same fact
// for a caller asking that nothing be left behind. Reporting ErrNotFound would
// make a DELETE of a mirror that never had readings fail, which is a request
// whose work is already done.
func TestScopedStoreDeleteParentOnAnAbsentParentIsNotAnError(t *testing.T) {
	s := memory.NewScopedStore[testItem]()
	ctx := context.Background()

	removed, err := s.DeleteParent(ctx, "never-existed")
	if err != nil {
		t.Fatalf("DeleteParent on an absent parent: %v", err)
	}
	if removed != 0 {
		t.Errorf("removed = %d, want 0", removed)
	}

	// It did not materialise the parent on the way past.
	has, err := s.HasParent(ctx, "never-existed")
	if err != nil {
		t.Fatalf("HasParent: %v", err)
	}
	if has {
		t.Error("DeleteParent materialised a bucket for an absent parent")
	}
}
