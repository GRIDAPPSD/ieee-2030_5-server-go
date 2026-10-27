package memory_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/storetest"
)

func TestJournaledScopedStoreSatisfiesScopedStoreContract(t *testing.T) {
	t.Parallel()
	storetest.RunScopedStoreSuite(t, func(t *testing.T) store.ScopedStore[storetest.Resource] {
		return openJournal(t, filepath.Join(t.TempDir(), "j.jsonl"))
	})
}

func openJournal(t *testing.T, path string) *memory.JournaledScopedStore[storetest.Resource] {
	t.Helper()
	s, err := memory.NewJournaledScopedStore[storetest.Resource](path, "Resource")
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// held is every record of s by parent, ids and values in list order.
func held(t *testing.T, s store.ScopedReader[storetest.Resource]) map[string][]storetest.Resource {
	t.Helper()
	ctx := context.Background()
	parents, err := s.Parents(ctx)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string][]storetest.Resource{}
	for _, p := range parents {
		page, err := s.List(ctx, p, store.ListOptions{Unbounded: true})
		if err != nil {
			t.Fatal(err)
		}
		if len(page.Items) > 0 {
			out[p] = page.Items
		}
	}
	return out
}

func equalHeld(a, b map[string][]storetest.Resource) bool {
	if len(a) != len(b) {
		return false
	}
	for p, items := range a {
		if !slices.EqualFunc(items, b[p], func(x, y storetest.Resource) bool {
			return x.ID == y.ID && x.Body == y.Body && slices.Equal(x.Tags, y.Tags)
		}) {
			return false
		}
	}
	return true
}

// Every write survives a reopen: the same parents, ids and values.
func TestJournaledScopedStore_ReopenHoldsTheSameRecords(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "j.jsonl")
	s := openJournal(t, path)
	for _, w := range []struct{ parent, id, body string }{
		{"dev1/a", "1", "one"}, {"dev1/a", "2", "two"}, {"dev1/b", "1", "three"},
		{"dev2", "1", "four"}, {"dev2", "2", "five"}, {"dev3", "1", "six"},
	} {
		if err := s.Create(ctx, w.parent, w.id, storetest.Resource{ID: w.id, Body: w.body, Tags: []string{w.parent}}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Update(ctx, "dev2", "1", storetest.Resource{ID: "1", Body: "four, updated"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(ctx, "dev2", "2"); err != nil {
		t.Fatal(err)
	}
	if n, err := s.DeleteParentsWithPrefix(ctx, "dev1/"); err != nil || n != 3 {
		t.Fatalf("DeleteParentsWithPrefix = %d, %v; want 3", n, err)
	}
	if n, err := s.DeleteParent(ctx, "dev3"); err != nil || n != 1 {
		t.Fatalf("DeleteParent = %d, %v; want 1", n, err)
	}
	want := map[string][]storetest.Resource{"dev2": {{ID: "1", Body: "four, updated"}}}
	if got := held(t, s); !equalHeld(got, want) {
		t.Fatalf("held = %+v, want %+v", got, want)
	}
	if got := held(t, openJournal(t, path)); !equalHeld(got, want) {
		t.Errorf("after reopen = %+v, want %+v", got, want)
	}
	if err := s.Create(ctx, "dev2", "1", storetest.Resource{ID: "1"}); !errors.Is(err, store.ErrAlreadyExists) {
		t.Errorf("duplicate create = %v, want ErrAlreadyExists", err)
	}
}

// A torn last line, an altered line and a create of an id already held are
// dropped, and the journal is rewritten without them.
func TestJournaledScopedStore_DamageIsDroppedAndRewritten(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "j.jsonl")
	s := openJournal(t, path)
	for _, id := range []string{"1", "2", "3"} {
		if err := s.Create(ctx, "p", id, storetest.Resource{ID: id, Body: "body " + id}); err != nil {
			t.Fatal(err)
		}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.SplitAfter(string(data), "\n")
	// Line 2 altered, line 1 repeated, and the last line torn.
	altered := strings.Replace(lines[1], "body 2", "body X", 1)
	damaged := lines[0] + altered + lines[0] + lines[2][:len(lines[2])/2]
	if err := os.WriteFile(path, []byte(damaged), 0o600); err != nil {
		t.Fatal(err)
	}
	r := openJournal(t, path)
	want := map[string][]storetest.Resource{"p": {{ID: "1", Body: "body 1"}}}
	if got := held(t, r); !equalHeld(got, want) {
		t.Fatalf("after damage = %+v, want %+v", got, want)
	}
	rewritten, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(rewritten) != lines[0] {
		t.Errorf("journal after open = %q, want only the first line %q", rewritten, lines[0])
	}
}

// A write whose append fails is not stored, and the journal is rewritten
// from memory before the next write, so the failure leaves nothing behind.
func TestJournaledScopedStore_FailedAppendIsUndone(t *testing.T) {
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "gone")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "j.jsonl")
	s := openJournal(t, path)
	if err := s.Create(ctx, "p", "1", storetest.Resource{ID: "1"}); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	if err := s.Create(ctx, "q", "1", storetest.Resource{ID: "1"}); err == nil {
		t.Fatal("create with no journal directory succeeded")
	}
	if err := s.Delete(ctx, "p", "1"); err == nil {
		t.Fatal("delete with no journal directory succeeded")
	}
	if n, err := s.DeleteParent(ctx, "p"); err == nil || n != 0 {
		t.Fatalf("DeleteParent with no journal directory = %d, %v; want 0 and an error", n, err)
	}
	want := map[string][]storetest.Resource{"p": {{ID: "1"}}}
	if got := held(t, s); !equalHeld(got, want) {
		t.Fatalf("after failed writes = %+v, want %+v", got, want)
	}
	if ok, _ := s.HasParent(ctx, "q"); ok {
		t.Error("a failed create left its parent behind")
	}
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := s.Create(ctx, "p", "2", storetest.Resource{ID: "2"}); err != nil {
		t.Fatal(err)
	}
	want["p"] = append(want["p"], storetest.Resource{ID: "2"})
	if got := held(t, openJournal(t, path)); !equalHeld(got, want) {
		t.Errorf("after recovery and reopen = %+v, want %+v", got, want)
	}
}

// Compact keeps what keep picks on disk, drops nothing from memory, and
// leaves the journal unchanged when keep fails.
func TestJournaledScopedStore_Compact(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "j.jsonl")
	s := openJournal(t, path)
	for _, id := range []string{"1", "2", "3", "4"} {
		if err := s.Create(ctx, "p", id, storetest.Resource{ID: id}); err != nil {
			t.Fatal(err)
		}
	}
	all := held(t, s)
	if err := s.Compact(ctx, func(string, []storetest.Resource) ([]bool, error) { return nil, errors.New("no") }); err == nil {
		t.Fatal("a failing keep compacted")
	}
	if err := s.Compact(ctx, func(string, []storetest.Resource) ([]bool, error) { return []bool{true}, nil }); err == nil {
		t.Fatal("a keep answering 1 of 4 records compacted")
	}
	if got := held(t, openJournal(t, path)); !equalHeld(got, all) {
		t.Fatalf("a failed compaction changed the journal: %+v", got)
	}
	var seen []string
	if err := s.Compact(ctx, func(parent string, items []storetest.Resource) ([]bool, error) {
		keep := make([]bool, len(items))
		for i, r := range items {
			seen = append(seen, r.ID)
			keep[i] = r.ID >= "3"
		}
		return keep, nil
	}); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(seen, []string{"1", "2", "3", "4"}) {
		t.Errorf("keep saw %v, want the ids ascending", seen)
	}
	if got := held(t, s); !equalHeld(got, all) {
		t.Errorf("compaction changed memory: %+v", got)
	}
	if err := s.Delete(ctx, "p", "1"); err != nil {
		t.Fatal(err)
	}
	if err := s.Sync(); err != nil {
		t.Fatal(err)
	}
	want := map[string][]storetest.Resource{"p": {{ID: "3"}, {ID: "4"}}}
	if got := held(t, openJournal(t, path)); !equalHeld(got, want) {
		t.Errorf("after compaction, a delete of a dropped record and reopen = %+v, want %+v", got, want)
	}
}
