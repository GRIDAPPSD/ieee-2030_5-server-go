package memory_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/storetest"
)

var _ store.ScopedStore[storetest.Resource] = (*memory.PersistentScopedStore[storetest.Resource])(nil)

func newPersistentResources(t *testing.T) (*memory.PersistentScopedStore[storetest.Resource], string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "resources.json")
	s, err := memory.NewPersistentScopedStore[storetest.Resource](path, "resources")
	if err != nil {
		t.Fatalf("NewPersistentScopedStore: %v", err)
	}
	return s, path
}

func res(id, body string, tags ...string) storetest.Resource {
	return storetest.Resource{ID: id, Body: body, Tags: tags}
}

func TestPersistentScopedStore_ConformanceSuite(t *testing.T) {
	storetest.RunScopedStoreSuite(t, func(t *testing.T) store.ScopedStore[storetest.Resource] {
		s, _ := newPersistentResources(t)
		return s
	})
}

func TestPersistentScopedStore_EmptyPathIsInMemory(t *testing.T) {
	s, err := memory.NewPersistentScopedStore[storetest.Resource]("", "resources")
	if err != nil {
		t.Fatalf("constructor: %v", err)
	}
	if s.Persists() {
		t.Fatal("Persists() = true for an empty path")
	}
	if !s.RollsBackOnFailure() {
		t.Fatal("RollsBackOnFailure() = false, want true")
	}
	if err := s.Create(context.Background(), "p", "a", res("a", "x")); err != nil {
		t.Fatalf("Create: %v", err)
	}
}

func TestPersistentScopedStore_PersistsOnceWired(t *testing.T) {
	s, _ := newPersistentResources(t)
	if !s.Persists() {
		t.Fatal("Persists() = false with a path configured")
	}
}

func TestPersistentScopedStore_ReloadRoundTripsTwoParents(t *testing.T) {
	ctx := context.Background()
	s, path := newPersistentResources(t)
	for _, w := range []struct{ parent, id, body string }{
		{"edev-1", "a", "one"},
		{"edev-1", "b", "two"},
		{"edev-2", "a", "three"},
	} {
		if err := s.Create(ctx, w.parent, w.id, res(w.id, w.body, "t-"+w.body)); err != nil {
			t.Fatalf("Create %s/%s: %v", w.parent, w.id, err)
		}
	}
	if err := s.Update(ctx, "edev-1", "b", res("b", "two-updated", "u")); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if err := s.Delete(ctx, "edev-1", "a"); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	re, err := memory.NewPersistentScopedStore[storetest.Resource](path, "resources")
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	parents, err := re.Parents(ctx)
	if err != nil {
		t.Fatalf("Parents: %v", err)
	}
	if !slices.Equal(parents, []string{"edev-1", "edev-2"}) {
		t.Fatalf("Parents = %v, want [edev-1 edev-2]", parents)
	}
	got, err := re.Get(ctx, "edev-1", "b")
	if err != nil {
		t.Fatalf("Get edev-1/b: %v", err)
	}
	if got.Body != "two-updated" || !slices.Equal(got.Tags, []string{"u"}) {
		t.Fatalf("edev-1/b = %+v, want body two-updated tags [u]", got)
	}
	got, err = re.Get(ctx, "edev-2", "a")
	if err != nil {
		t.Fatalf("Get edev-2/a: %v", err)
	}
	if got.Body != "three" || !slices.Equal(got.Tags, []string{"t-three"}) {
		t.Fatalf("edev-2/a = %+v, want body three tags [t-three]", got)
	}
	if _, err := re.Get(ctx, "edev-1", "a"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("deleted edev-1/a after reload: err = %v, want ErrNotFound", err)
	}
	if n, _ := re.Count(ctx, "edev-1"); n != 1 {
		t.Fatalf("Count(edev-1) = %d, want 1", n)
	}
}

func TestPersistentScopedStore_CorruptSnapshotRefusesToLoad(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bad.json")
	if err := os.WriteFile(path, []byte(`{"version":99,"records":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := memory.NewPersistentScopedStore[storetest.Resource](path, "resources"); err == nil {
		t.Fatal("constructor accepted an unknown snapshot version")
	}
}

func TestPersistentScopedStore_FailedCreateIsNotReadable(t *testing.T) {
	ctx := context.Background()
	s, path := newPersistentResources(t)
	blockPersist(t, path)

	if err := s.Create(ctx, "p", "a", res("a", "x")); err == nil {
		t.Fatal("Create succeeded on an unwritable path")
	}
	if _, err := s.Get(ctx, "p", "a"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("Get after failed Create: err = %v, want ErrNotFound", err)
	}
	if n, _ := s.Count(ctx, "p"); n != 0 {
		t.Fatalf("Count = %d, want 0", n)
	}
	if ok, _ := s.HasParent(ctx, "p"); ok {
		t.Fatal("a failed Create left its new parent behind")
	}
	if parents, _ := s.Parents(ctx); len(parents) != 0 {
		t.Fatalf("Parents = %v, want none", parents)
	}
}

func TestPersistentScopedStore_FailedCreateKeepsExistingParentAndSiblings(t *testing.T) {
	ctx := context.Background()
	s, path := newPersistentResources(t)
	if err := s.Create(ctx, "p", "a", res("a", "keep")); err != nil {
		t.Fatalf("seed: %v", err)
	}
	blockPersist(t, path)
	if err := s.Create(ctx, "p", "b", res("b", "lost")); err == nil {
		t.Fatal("Create succeeded on an unwritable path")
	}
	if _, err := s.Get(ctx, "p", "b"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("Get b: err = %v, want ErrNotFound", err)
	}
	got, err := s.Get(ctx, "p", "a")
	if err != nil || got.Body != "keep" {
		t.Fatalf("sibling a = %+v, %v; want body keep", got, err)
	}
}

func TestPersistentScopedStore_FailedUpdateRestoresPriorValue(t *testing.T) {
	ctx := context.Background()
	s, path := newPersistentResources(t)
	if err := s.Create(ctx, "p", "a", res("a", "before", "t1")); err != nil {
		t.Fatalf("seed: %v", err)
	}
	blockPersist(t, path)
	if err := s.Update(ctx, "p", "a", res("a", "after", "t2")); err == nil {
		t.Fatal("Update succeeded on an unwritable path")
	}
	got, err := s.Get(ctx, "p", "a")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Body != "before" || !slices.Equal(got.Tags, []string{"t1"}) {
		t.Fatalf("after failed Update = %+v, want body before tags [t1]", got)
	}
}

func TestPersistentScopedStore_FailedDeleteRestoresRecord(t *testing.T) {
	ctx := context.Background()
	s, path := newPersistentResources(t)
	if err := s.Create(ctx, "p", "a", res("a", "keep", "t")); err != nil {
		t.Fatalf("seed: %v", err)
	}
	blockPersist(t, path)
	if err := s.Delete(ctx, "p", "a"); err == nil {
		t.Fatal("Delete succeeded on an unwritable path")
	}
	got, err := s.Get(ctx, "p", "a")
	if err != nil {
		t.Fatalf("Get after failed Delete: %v", err)
	}
	if got.Body != "keep" || !slices.Equal(got.Tags, []string{"t"}) {
		t.Fatalf("restored record = %+v, want body keep tags [t]", got)
	}

	// The key index must have been restored too, or the next successful
	// snapshot would drop the record from disk.
	if err := os.RemoveAll(path + ".tmp"); err != nil {
		t.Fatal(err)
	}
	if err := s.Create(ctx, "p", "b", res("b", "next")); err != nil {
		t.Fatalf("Create after unblocking: %v", err)
	}
	re, err := memory.NewPersistentScopedStore[storetest.Resource](path, "resources")
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if _, err := re.Get(ctx, "p", "a"); err != nil {
		t.Fatalf("restored record missing after next snapshot: %v", err)
	}
}

func TestPersistentScopedStore_DuplicateCreateKeepsOriginal(t *testing.T) {
	ctx := context.Background()
	s, path := newPersistentResources(t)
	if err := s.Create(ctx, "p", "a", res("a", "first")); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := s.Create(ctx, "p", "a", res("a", "second")); !errors.Is(err, store.ErrAlreadyExists) {
		t.Fatalf("duplicate Create: err = %v, want ErrAlreadyExists", err)
	}
	re, err := memory.NewPersistentScopedStore[storetest.Resource](path, "resources")
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	got, err := re.Get(ctx, "p", "a")
	if err != nil || got.Body != "first" {
		t.Fatalf("reloaded = %+v, %v; want body first", got, err)
	}
}
