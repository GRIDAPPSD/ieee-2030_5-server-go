package memory_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-go/pkg/store"
	"github.com/GRIDAPPSD/ieee-2030_5-go/pkg/store/memory"
)

// testItem implements store.Copier
type testItem struct {
	Name  string
	Value int
}

func (t testItem) Copy() testItem {
	return testItem{Name: t.Name, Value: t.Value}
}

func TestCreateAndGet(t *testing.T) {
	s := memory.NewStore[testItem]()
	ctx := context.Background()

	err := s.Create(ctx, "a", testItem{Name: "alpha", Value: 1})
	if err != nil {
		t.Fatal(err)
	}

	got, err := s.Get(ctx, "a")
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "alpha" || got.Value != 1 {
		t.Errorf("got %+v, want alpha/1", got)
	}
}

func TestGetNotFound(t *testing.T) {
	s := memory.NewStore[testItem]()
	_, err := s.Get(context.Background(), "missing")
	if !errors.Is(err, store.ErrNotFound) {
		t.Errorf("got %v, want ErrNotFound", err)
	}
}

func TestCreateDuplicate(t *testing.T) {
	s := memory.NewStore[testItem]()
	ctx := context.Background()

	_ = s.Create(ctx, "a", testItem{Name: "first"})
	err := s.Create(ctx, "a", testItem{Name: "second"})
	if !errors.Is(err, store.ErrAlreadyExists) {
		t.Errorf("got %v, want ErrAlreadyExists", err)
	}
}

func TestUpdate(t *testing.T) {
	s := memory.NewStore[testItem]()
	ctx := context.Background()

	_ = s.Create(ctx, "a", testItem{Name: "v1", Value: 1})
	err := s.Update(ctx, "a", testItem{Name: "v2", Value: 2})
	if err != nil {
		t.Fatal(err)
	}

	got, _ := s.Get(ctx, "a")
	if got.Name != "v2" || got.Value != 2 {
		t.Errorf("got %+v after update", got)
	}
}

func TestUpdateNotFound(t *testing.T) {
	s := memory.NewStore[testItem]()
	err := s.Update(context.Background(), "missing", testItem{})
	if !errors.Is(err, store.ErrNotFound) {
		t.Errorf("got %v, want ErrNotFound", err)
	}
}

func TestDelete(t *testing.T) {
	s := memory.NewStore[testItem]()
	ctx := context.Background()

	_ = s.Create(ctx, "a", testItem{Name: "alpha"})
	err := s.Delete(ctx, "a")
	if err != nil {
		t.Fatal(err)
	}

	_, err = s.Get(ctx, "a")
	if !errors.Is(err, store.ErrNotFound) {
		t.Error("item should be deleted")
	}

	count, _ := s.Count(ctx)
	if count != 0 {
		t.Errorf("count = %d after delete, want 0", count)
	}
}

func TestDeleteNotFound(t *testing.T) {
	s := memory.NewStore[testItem]()
	err := s.Delete(context.Background(), "missing")
	if !errors.Is(err, store.ErrNotFound) {
		t.Errorf("got %v, want ErrNotFound", err)
	}
}

func TestListPaging(t *testing.T) {
	s := memory.NewStore[testItem]()
	ctx := context.Background()

	for i := 0; i < 10; i++ {
		id := string(rune('a' + i))
		_ = s.Create(ctx, id, testItem{Name: id, Value: i})
	}

	tests := []struct {
		name    string
		opts    store.ListOptions
		wantAll uint32
		wantN   int
		wantFirst string
	}{
		{"default", store.ListOptions{Limit: 10}, 10, 10, "a"},
		{"limit 3", store.ListOptions{Limit: 3}, 10, 3, "a"},
		{"start 5 limit 3", store.ListOptions{Start: 5, Limit: 3}, 10, 3, "f"},
		{"start 8 limit 5", store.ListOptions{Start: 8, Limit: 5}, 10, 2, "i"},
		{"start beyond end", store.ListOptions{Start: 20, Limit: 5}, 10, 0, ""},
		{"limit 0", store.ListOptions{Limit: 0}, 10, 0, ""},
		{"after c limit 3", store.ListOptions{After: "c", Limit: 3}, 10, 3, "d"},
		{"after c start 2 limit 2", store.ListOptions{After: "c", Start: 2, Limit: 2}, 10, 2, "f"},
		{"after j (last)", store.ListOptions{After: "j", Limit: 5}, 10, 0, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := s.List(ctx, tt.opts)
			if err != nil {
				t.Fatal(err)
			}
			if result.All != tt.wantAll {
				t.Errorf("All = %d, want %d", result.All, tt.wantAll)
			}
			if int(result.Results) != tt.wantN {
				t.Errorf("Results = %d, want %d", result.Results, tt.wantN)
			}
			if len(result.Items) != tt.wantN {
				t.Errorf("len(Items) = %d, want %d", len(result.Items), tt.wantN)
			}
			if tt.wantN > 0 && result.Items[0].Name != tt.wantFirst {
				t.Errorf("first item = %q, want %q", result.Items[0].Name, tt.wantFirst)
			}
		})
	}
}

func TestListEmpty(t *testing.T) {
	s := memory.NewStore[testItem]()
	result, err := s.List(context.Background(), store.ListOptions{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if result.All != 0 || result.Results != 0 || len(result.Items) != 0 {
		t.Errorf("empty store list = %+v, want all zeros", result)
	}
}

func TestImmutability(t *testing.T) {
	s := memory.NewStore[testItem]()
	ctx := context.Background()

	_ = s.Create(ctx, "a", testItem{Name: "original", Value: 42})

	// Mutate the returned value
	got, _ := s.Get(ctx, "a")
	got.Name = "MUTATED"
	got.Value = 999

	// Re-read should return original
	got2, _ := s.Get(ctx, "a")
	if got2.Name != "original" || got2.Value != 42 {
		t.Errorf("store was mutated: got %+v, want original/42", got2)
	}
}

func TestConcurrency(t *testing.T) {
	s := memory.NewStore[testItem]()
	ctx := context.Background()
	var wg sync.WaitGroup

	// Concurrent writes
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			id := string(rune('A' + (i % 26)))
			_ = s.Create(ctx, id+string(rune('0'+i%10)), testItem{Name: id, Value: i})
		}(i)
	}

	// Concurrent reads
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = s.List(ctx, store.ListOptions{Limit: 10})
		}()
	}

	wg.Wait()

	count, _ := s.Count(ctx)
	if count == 0 {
		t.Error("expected some items after concurrent writes")
	}
}

func TestCount(t *testing.T) {
	s := memory.NewStore[testItem]()
	ctx := context.Background()

	count, _ := s.Count(ctx)
	if count != 0 {
		t.Errorf("empty store count = %d", count)
	}

	_ = s.Create(ctx, "a", testItem{})
	_ = s.Create(ctx, "b", testItem{})

	count, _ = s.Count(ctx)
	if count != 2 {
		t.Errorf("count = %d, want 2", count)
	}
}
