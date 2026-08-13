package memory_test

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

// The scoped store under reads of parents that do not exist.
//
// A read used to reach ForParent, which creates the per-parent bucket when it
// is absent, so a GET, a list or a count against an unknown parent LEFT STATE
// BEHIND. The parent id on every scoped route is a path segment the client
// chooses, so the growth is client-driven, unbounded, and stores nothing that
// any later request can retrieve: the buckets themselves are the leak. There is
// no eviction and no ceiling, which makes it a denial-of-service question
// rather than a tidiness one.
//
// The assertion is the parent COUNT after the reads, not the answers the reads
// gave. The answers were already correct: an empty bucket reads exactly like an
// absent parent, which is why the defect was invisible on the wire and why
// nothing in the existing suite caught it.

// absentParentReadProbes is the read count. It is large enough that the growth
// is unmistakable in the failure message rather than an off-by-one, and small
// enough to stay a unit test.
const absentParentReadProbes = 1000

// TestScopedStoreReadsDoNotMaterialiseAbsentParents drives every read on the
// contract against a distinct parent id that was never written, and requires
// the store to know no parents afterwards.
//
// Delete is included even though it is nominally a write: a delete against an
// unknown parent removes nothing and returns ErrNotFound, so it is a read of
// the parent map for allocation purposes and used to materialise a bucket on
// the way to failing.
func TestScopedStoreReadsDoNotMaterialiseAbsentParents(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	for _, probe := range []struct {
		name string
		read func(s *memory.ScopedStore[testItem], parentID string) error
	}{
		{"Get", func(s *memory.ScopedStore[testItem], parentID string) error {
			_, err := s.Get(ctx, parentID, "any-resource")
			if !errors.Is(err, store.ErrNotFound) {
				return err
			}
			return nil
		}},
		{"List", func(s *memory.ScopedStore[testItem], parentID string) error {
			_, err := s.List(ctx, parentID, store.ListOptions{Limit: 10})
			return err
		}},
		{"Count", func(s *memory.ScopedStore[testItem], parentID string) error {
			_, err := s.Count(ctx, parentID)
			return err
		}},
		{"HasParent", func(s *memory.ScopedStore[testItem], parentID string) error {
			_, err := s.HasParent(ctx, parentID)
			return err
		}},
		{"Delete", func(s *memory.ScopedStore[testItem], parentID string) error {
			err := s.Delete(ctx, parentID, "any-resource")
			if !errors.Is(err, store.ErrNotFound) {
				return err
			}
			return nil
		}},
	} {
		t.Run(probe.name, func(t *testing.T) {
			t.Parallel()

			s := memory.NewScopedStore[testItem]()

			for i := 0; i < absentParentReadProbes; i++ {
				if err := probe.read(s, "absent-parent-"+strconv.Itoa(i)); err != nil {
					t.Fatalf("%s against absent parent %d: %v", probe.name, i, err)
				}
			}

			parents, err := s.Parents(ctx)
			if err != nil {
				t.Fatalf("Parents: %v", err)
			}
			if len(parents) != 0 {
				t.Errorf("%d %s(s) against distinct absent parents left %d parent(s) in the store, want 0. "+
					"A read that allocates lets a client grow the store without bound with keys it chooses",
					absentParentReadProbes, probe.name, len(parents))
			}
		})
	}
}

// TestScopedStoreReadsDoNotMaterialiseTheParentTheyRead is the single-parent
// statement of the same fact, checked through HasParent rather than a count.
//
// It is separate because it names the consequence a caller sees: after this
// change a parent that has only ever been read is not reported present, so
// HasParent answers a question about what was WRITTEN. The whole-store count
// above would still pass if reads materialised one shared bucket.
func TestScopedStoreReadsDoNotMaterialiseTheParentTheyRead(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	s := memory.NewScopedStore[testItem]()

	if _, err := s.Get(ctx, "read-only-parent", "x"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("Get on an absent parent err = %v, want ErrNotFound", err)
	}
	if _, err := s.List(ctx, "read-only-parent", store.ListOptions{Limit: 10}); err != nil {
		t.Fatalf("List on an absent parent: %v", err)
	}
	if _, err := s.Count(ctx, "read-only-parent"); err != nil {
		t.Fatalf("Count on an absent parent: %v", err)
	}

	has, err := s.HasParent(ctx, "read-only-parent")
	if err != nil {
		t.Fatalf("HasParent: %v", err)
	}
	if has {
		t.Error("a parent that was only ever READ is reported present; reads must not create")
	}
}

// TestScopedStoreEmptyParentAfterItsLastResourceStillListsEmpty pins the second
// half of the contract the fix must not trade away: a parent that genuinely
// EXISTS but holds nothing lists empty with a nil error. It is not a 404 and it
// is not an error.
//
// The parent here is real: it was written to and then emptied. Collapsing this
// case into ErrNotFound is the obvious wrong way to remove the allocation, and
// it would render on the wire as a 404 for a collection that exists.
func TestScopedStoreEmptyParentAfterItsLastResourceStillListsEmpty(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	s := memory.NewScopedStore[testItem]()

	if err := s.Create(ctx, "real-parent", "a", testItem{Name: "a"}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := s.Delete(ctx, "real-parent", "a"); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	result, err := s.List(ctx, "real-parent", store.ListOptions{Limit: 10})
	if err != nil {
		t.Fatalf("List of an existing but empty parent: %v", err)
	}
	if result.All != 0 || result.Results != 0 || len(result.Items) != 0 {
		t.Errorf("existing empty parent listed %+v, want an empty page", result)
	}

	count, err := s.Count(ctx, "real-parent")
	if err != nil {
		t.Fatalf("Count of an existing but empty parent: %v", err)
	}
	if count != 0 {
		t.Errorf("existing empty parent count = %d, want 0", count)
	}
}

// TestScopedStoreListOfAnAbsentParentStillRefusesBadOptions is the boundary
// case in removing the allocation.
//
// The old path built an empty bucket and called its List, so an absent parent
// went through the SAME option validation a present one does. A short circuit
// that returns an empty page before validating would answer a request the store
// is not entitled to serve: a caller that asked for an order the store cannot
// provide would be told the collection is empty instead of being refused, which
// is the error contract's "a refusal is not an empty collection" inverted.
func TestScopedStoreListOfAnAbsentParentStillRefusesBadOptions(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	s := memory.NewScopedStore[testItem]()

	if _, err := s.List(ctx, "absent", store.ListOptions{Sort: store.SortKey(200)}); !errors.Is(err, store.ErrUnsupportedSort) {
		t.Errorf("List of an absent parent with an unknown sort key err = %v, want ErrUnsupportedSort", err)
	}
	if _, err := s.List(ctx, "absent", store.ListOptions{Unbounded: true, Limit: 5}); !errors.Is(err, store.ErrInvalidListOptions) {
		t.Errorf("List of an absent parent with contradictory paging err = %v, want ErrInvalidListOptions", err)
	}

	parents, err := s.Parents(ctx)
	if err != nil {
		t.Fatalf("Parents: %v", err)
	}
	if len(parents) != 0 {
		t.Errorf("a refused List left %d parent(s) behind, want 0", len(parents))
	}
}

// TestScopedStoreConcurrentReadOfAParentBeingCreated exercises the interleaving
// the fix introduces a question about: a read that no longer creates runs
// against a parent a concurrent writer is establishing.
//
// Either answer is correct, because the two orderings are both legal
// linearizations. What must NOT happen is a torn read, a panic, or a parent
// that exists in the map with a nil bucket. The race detector is what makes
// this test worth running; the assertions cover the ordering-independent facts
// the detector cannot state.
func TestScopedStoreConcurrentReadOfAParentBeingCreated(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	s := memory.NewScopedStore[testItem]()

	const parents = 200
	var wg sync.WaitGroup
	for i := 0; i < parents; i++ {
		parentID := "p" + strconv.Itoa(i)

		wg.Add(3)
		go func() {
			defer wg.Done()
			if err := s.Create(ctx, parentID, "a", testItem{Name: "a", Value: 1}); err != nil {
				t.Errorf("Create(%s): %v", parentID, err)
			}
		}()
		go func() {
			defer wg.Done()
			// Either ErrNotFound (read ordered first) or the resource (write
			// ordered first). Any other error is a defect.
			if _, err := s.Get(ctx, parentID, "a"); err != nil && !errors.Is(err, store.ErrNotFound) {
				t.Errorf("Get(%s): %v", parentID, err)
			}
		}()
		go func() {
			defer wg.Done()
			got, err := s.List(ctx, parentID, store.ListOptions{Limit: 10})
			if err != nil {
				t.Errorf("List(%s): %v", parentID, err)
				return
			}
			if got.All > 1 {
				t.Errorf("List(%s) All = %d, want 0 or 1", parentID, got.All)
			}
		}()
	}
	wg.Wait()

	// Every parent was written exactly once, so all of them must be present and
	// hold their single resource: a read racing the create must not have
	// displaced the bucket the create established.
	known, err := s.Parents(ctx)
	if err != nil {
		t.Fatalf("Parents: %v", err)
	}
	if len(known) != parents {
		t.Fatalf("after %d creates the store knows %d parent(s), want %d", parents, len(known), parents)
	}
	for i := 0; i < parents; i++ {
		parentID := "p" + strconv.Itoa(i)
		got, err := s.Get(ctx, parentID, "a")
		if err != nil {
			t.Fatalf("Get(%s) after the race: %v", parentID, err)
		}
		if got.Name != "a" || got.Value != 1 {
			t.Errorf("Get(%s) = %+v, want {a 1}", parentID, got)
		}
	}
}
