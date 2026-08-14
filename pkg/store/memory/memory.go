package memory

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
)

// Store is a generic in-memory ResourceStore backed by a map and sorted key slice.
// Each instance has its own sync.RWMutex for fine-grained concurrency.
type Store[T store.Copier[T]] struct {
	mu   sync.RWMutex
	data map[string]T
	keys []string // sorted for deterministic list ordering
}

// NewStore creates a new empty in-memory store.
func NewStore[T store.Copier[T]]() *Store[T] {
	return &Store[T]{
		data: make(map[string]T),
	}
}

func (s *Store[T]) Get(_ context.Context, id string) (T, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	item, ok := s.data[id]
	if !ok {
		var zero T
		return zero, store.ErrNotFound
	}
	return item.Copy(), nil
}

// validateListOptions refuses the option combinations this implementation
// cannot serve a page for, before any state is read.
//
// It is a package-level function rather than inline in List because
// [ScopedStore.List] must apply the SAME refusals to a parent it does not know.
// That path used to reach here by materializing an empty bucket and listing it,
// an allocation that was later removed; sharing the check is what keeps
// removing the allocation from also removing the validation. A copy would drift,
// and the drift would show up as a malformed request being answered with an
// empty collection for unknown parents only.
func validateListOptions(opts store.ListOptions) error {
	// Self-contradictory options are refused: a page served for them would be
	// one of two different answers chosen silently.
	if err := opts.Validate(); err != nil {
		return err
	}

	// Reject an unrecognized sort key rather than silently serving the
	// default order: a caller that asked for an order it did not get would
	// page through a sequence that does not match what it requested.
	switch opts.Sort {
	case store.SortByIDAsc, store.SortByIDDesc:
		return nil
	default:
		return fmt.Errorf("%w: %d", store.ErrUnsupportedSort, uint8(opts.Sort))
	}
}

func (s *Store[T]) List(_ context.Context, opts store.ListOptions) (store.ListResult[T], error) {
	if err := validateListOptions(opts); err != nil {
		return store.ListResult[T]{}, err
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	all := uint32(len(s.keys))

	// s.keys is maintained ascending; descending is a reversed copy so the
	// store's own slice is never mutated under an RLock.
	keys := s.keys
	if opts.Sort == store.SortByIDDesc {
		keys = slices.Clone(s.keys)
		slices.Reverse(keys)
	}

	// Determine starting keys based on After param
	if opts.After != "" {
		idx := findAfter(keys, opts.After, opts.Sort)
		keys = keys[idx:]
	}

	// Apply Start offset
	if opts.Start >= uint32(len(keys)) {
		return store.ListResult[T]{All: all, Results: 0, Items: nil}, nil
	}
	keys = keys[opts.Start:]

	// Apply Limit, unless the caller asked for everything from here on.
	// opts.Limit is necessarily zero in the unbounded case: Validate rejects
	// the combination, so this is not a precedence choice made here.
	if !opts.Unbounded {
		limit := opts.Limit
		if limit == 0 {
			return store.ListResult[T]{All: all, Results: 0, Items: nil}, nil
		}
		if limit > uint32(len(keys)) {
			limit = uint32(len(keys))
		}
		keys = keys[:limit]
	}

	// Build result with copies
	items := make([]T, 0, len(keys))
	for _, k := range keys {
		items = append(items, s.data[k].Copy())
	}

	return store.ListResult[T]{
		All:     all,
		Results: uint32(len(items)),
		Items:   items,
	}, nil
}

func (s *Store[T]) Create(_ context.Context, id string, resource T) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, exists := s.data[id]; exists {
		return store.ErrAlreadyExists
	}

	s.data[id] = resource.Copy()
	idx, _ := slices.BinarySearch(s.keys, id)
	s.keys = slices.Insert(s.keys, idx, id)
	return nil
}

func (s *Store[T]) Update(_ context.Context, id string, resource T) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, exists := s.data[id]; !exists {
		return store.ErrNotFound
	}

	s.data[id] = resource.Copy()
	return nil
}

func (s *Store[T]) Delete(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, exists := s.data[id]; !exists {
		return store.ErrNotFound
	}

	delete(s.data, id)
	idx, found := slices.BinarySearch(s.keys, id)
	if found {
		s.keys = slices.Delete(s.keys, idx, idx+1)
	}
	return nil
}

func (s *Store[T]) Count(_ context.Context) (uint32, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return uint32(len(s.data)), nil
}

// findAfter returns the index of the first key strictly after the given key in
// the requested sort order. keys must already be ordered per sort.
//
// "After" is defined relative to the order, not to byte comparison: under
// SortByIDDesc the next page starts at the first key strictly less than after.
func findAfter(keys []string, after string, sort store.SortKey) int {
	cmp := strings.Compare
	if sort == store.SortByIDDesc {
		cmp = func(a, b string) int { return strings.Compare(b, a) }
	}
	idx, found := slices.BinarySearchFunc(keys, after, cmp)
	if found {
		return idx + 1
	}
	return idx
}
