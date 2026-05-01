package memory

import (
	"context"
	"slices"
	"sync"

	"github.com/GRIDAPPSD/ieee-2030_5-go/pkg/store"
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

func (s *Store[T]) List(_ context.Context, opts store.ListOptions) (store.ListResult[T], error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	all := uint32(len(s.keys))

	// Determine starting keys based on After param
	keys := s.keys
	if opts.After != "" {
		idx := s.findAfter(opts.After)
		keys = keys[idx:]
	}

	// Apply Start offset
	if opts.Start >= uint32(len(keys)) {
		return store.ListResult[T]{All: all, Results: 0, Items: nil}, nil
	}
	keys = keys[opts.Start:]

	// Apply Limit
	limit := opts.Limit
	if limit == 0 {
		return store.ListResult[T]{All: all, Results: 0, Items: nil}, nil
	}
	if limit > uint32(len(keys)) {
		limit = uint32(len(keys))
	}
	keys = keys[:limit]

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

// findAfter returns the index of the first key strictly greater than after.
func (s *Store[T]) findAfter(after string) int {
	idx, found := slices.BinarySearch(s.keys, after)
	if found {
		return idx + 1
	}
	return idx
}
