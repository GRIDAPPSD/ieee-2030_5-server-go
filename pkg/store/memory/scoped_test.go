package memory_test

import (
	"context"
	"errors"
	"testing"

	"github.com/craig8/ieee-2030_5-go/pkg/store"
	"github.com/craig8/ieee-2030_5-go/pkg/store/memory"
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

	if s.HasParent("p1") {
		t.Error("should not have parent before any operations")
	}

	_ = s.Create(ctx, "p1", "a", testItem{})

	if !s.HasParent("p1") {
		t.Error("should have parent after create")
	}
}
