package memory

import (
	"context"
	"sync"

	"github.com/GRIDAPPSD/ieee-2030_5-go/pkg/store"
)

// ScopedStore manages per-parent resource stores.
// For example, MeterReadings scoped under UsagePoints, or Readings
// scoped under MeterReadings. Each parent ID gets its own Store[T].
type ScopedStore[T store.Copier[T]] struct {
	mu     sync.RWMutex
	stores map[string]*Store[T]
}

// NewScopedStore creates a new ScopedStore.
func NewScopedStore[T store.Copier[T]]() *ScopedStore[T] {
	return &ScopedStore[T]{
		stores: make(map[string]*Store[T]),
	}
}

// ForParent returns the Store for the given parent ID, creating it if needed.
func (s *ScopedStore[T]) ForParent(parentID string) *Store[T] {
	s.mu.RLock()
	st, ok := s.stores[parentID]
	s.mu.RUnlock()
	if ok {
		return st
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	// Double-check after acquiring write lock
	if st, ok := s.stores[parentID]; ok {
		return st
	}

	st = NewStore[T]()
	s.stores[parentID] = st
	return st
}

// HasParent returns true if a store exists for the given parent ID.
func (s *ScopedStore[T]) HasParent(parentID string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, ok := s.stores[parentID]
	return ok
}

// Get retrieves a resource from a parent's store.
func (s *ScopedStore[T]) Get(ctx context.Context, parentID, id string) (T, error) {
	return s.ForParent(parentID).Get(ctx, id)
}

// List lists resources from a parent's store with paging.
func (s *ScopedStore[T]) List(ctx context.Context, parentID string, opts store.ListOptions) (store.ListResult[T], error) {
	return s.ForParent(parentID).List(ctx, opts)
}

// Create adds a resource to a parent's store.
func (s *ScopedStore[T]) Create(ctx context.Context, parentID, id string, resource T) error {
	return s.ForParent(parentID).Create(ctx, id, resource)
}

// Delete removes a resource from a parent's store.
func (s *ScopedStore[T]) Delete(ctx context.Context, parentID, id string) error {
	return s.ForParent(parentID).Delete(ctx, id)
}

// Count returns the number of resources in a parent's store.
func (s *ScopedStore[T]) Count(ctx context.Context, parentID string) (uint32, error) {
	return s.ForParent(parentID).Count(ctx)
}
