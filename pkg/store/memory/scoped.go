package memory

import (
	"context"
	"slices"
	"sync"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
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
//
// ForParent is deliberately NOT part of store.ScopedStore. It returns a
// concrete type, and it materializes a parent bucket, which no durable backend
// can implement sensibly and which lets an arbitrary path segment allocate.
//
// Nothing on the read half of this type calls it any more: Get, List, Count
// and Delete take [ScopedStore.parentStore], which looks a parent up
// without establishing one, and Create is the single caller left, where
// establishing the parent is the point. Reaching for ForParent from a read path
// reintroduces client-driven unbounded growth, because every parent id on the
// scoped surface is a path segment the client chose.
//
// It remains exported because callers outside this module hold it; new code
// should use the contract methods.
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

// parentStore returns the Store for parentID WITHOUT creating one, and reports
// whether it exists.
//
// This is the read half's counterpart to ForParent, and the difference
// matters entirely: the parent id reaching every scoped route is a path
// segment the client chose, so a read that creates on miss is an allocation
// primitive an authenticated client can drive without bound, storing nothing
// any later request can retrieve.
//
// It returns the child store and releases s.mu before the caller touches it,
// which is the lock discipline ForParent already had: this type never holds the
// parent lock across a call into a child store, so the two locks are never
// ordered against each other.
func (s *ScopedStore[T]) parentStore(parentID string) (*Store[T], bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	st, ok := s.stores[parentID]
	return st, ok
}

// HasParent reports whether a store exists for the given parent ID.
//
// The error return is always nil here because the lookup is a local map read,
// but it is part of store.ScopedReader so that a durable implementation, where
// this is a query that can fail, can report that failure instead of returning
// a bare false that a caller would read as "absent".
//
// No read materializes a bucket, so this reports what was WRITTEN. A parent
// still lingers after its last resource is deleted, though, so callers must
// not infer emptiness from a true: the store.ScopedReader contract leaves
// that implementation-defined and Count is the question to ask.
func (s *ScopedStore[T]) HasParent(_ context.Context, parentID string) (bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, ok := s.stores[parentID]
	return ok, nil
}

// Parents returns the known parent IDs in ascending order.
//
// The result may include parents that hold no resources, per the
// store.ScopedReader contract, which requires only that every parent holding at
// least one resource appears. Those can only come from a write: a parent
// emptied by Delete, or one established by ForParent, and no longer one
// that a read invented.
func (s *ScopedStore[T]) Parents(_ context.Context) ([]string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	parents := make([]string, 0, len(s.stores))
	for parentID := range s.stores {
		parents = append(parents, parentID)
	}
	slices.Sort(parents)
	return parents, nil
}

// Get retrieves a resource from a parent's store.
//
// An unknown parent is ErrNotFound, exactly as an unknown resource under a known
// parent is, per store.ScopedReader. The two were already indistinguishable to a
// caller: the empty bucket the old path created answered ErrNotFound too. What
// is gone is the bucket.
func (s *ScopedStore[T]) Get(ctx context.Context, parentID, id string) (T, error) {
	st, ok := s.parentStore(parentID)
	if !ok {
		var zero T
		return zero, store.ErrNotFound
	}
	return st.Get(ctx, id)
}

// List lists resources from a parent's store with paging.
//
// An unknown parent yields an empty page and a NIL error, which is what
// store.ScopedReader requires and what a known but empty parent yields. It is
// deliberately not ErrNotFound: a scoped list is addressed by a parent the
// client names, and reporting absence there would turn every list of a
// collection that has not been written into a 404 on the wire.
//
// The options are validated first even though there is nothing to page over.
// The old path got that for free by listing an empty bucket, and dropping it
// would mean a request the store cannot serve, an unsupported sort key or
// contradictory paging, is answered with an empty collection instead of a
// refusal, for unknown parents only. That is the error contract's "a failure
// must not be flattened into the commonest successful answer" in miniature.
func (s *ScopedStore[T]) List(ctx context.Context, parentID string, opts store.ListOptions) (store.ListResult[T], error) {
	st, ok := s.parentStore(parentID)
	if !ok {
		if err := validateListOptions(opts); err != nil {
			return store.ListResult[T]{}, err
		}
		return store.ListResult[T]{}, nil
	}
	return st.List(ctx, opts)
}

// Create adds a resource to a parent's store.
//
// This is the one operation that establishes a parent, and the only remaining
// caller of ForParent: a create under a parent that does not yet exist is the
// case the per-parent bucket exists for.
func (s *ScopedStore[T]) Create(ctx context.Context, parentID, id string, resource T) error {
	return s.ForParent(parentID).Create(ctx, id, resource)
}

// Update replaces a resource in a parent's store.
//
// An update against an unknown parent reports ErrNotFound. Update never
// creates, at either level.
func (s *ScopedStore[T]) Update(ctx context.Context, parentID, id string, resource T) error {
	st, ok := s.parentStore(parentID)
	if !ok {
		return store.ErrNotFound
	}
	return st.Update(ctx, id, resource)
}

// Delete removes a resource from a parent's store.
//
// An unknown parent is ErrNotFound, which is what deleting a resource under an
// empty bucket already reported. A delete is a read of the parent map: it
// removes a child, it never establishes a parent, so it must not allocate one on
// its way to reporting that there was nothing to remove.
func (s *ScopedStore[T]) Delete(ctx context.Context, parentID, id string) error {
	st, ok := s.parentStore(parentID)
	if !ok {
		return store.ErrNotFound
	}
	return st.Delete(ctx, id)
}

// Count returns the number of resources in a parent's store.
//
// An unknown parent counts zero and is not an error, per store.ScopedReader.
// Zero here is an answer about the collection, never a stand-in for a count that
// could not be taken.
func (s *ScopedStore[T]) Count(ctx context.Context, parentID string) (uint32, error) {
	st, ok := s.parentStore(parentID)
	if !ok {
		return 0, nil
	}
	return st.Count(ctx)
}

// DeleteParent removes a parent's whole collection and reports how many
// resources went with it.
//
// It exists for CASCADE deletion: when the parent resource is deleted, the
// children scoped under it have to go too, or the collection outlives the only
// thing that could address it. Deleting the parent and leaving its children is
// not a tidiness problem, it is a correctness one: the orphaned bucket stays in
// the map, so it is served to any code that names the same parent id, and a
// later resource created under a RE-USED id would inherit the dead parent's
// children as if they were its own.
//
// It is one operation rather than a list-then-delete loop for two reasons.
// The store exposes no way to enumerate KEYS, only values, so a loop would have
// to recover each child's id from a field of the child itself, which makes the
// cascade depend on a value a writer controls rather than on the key the store
// is actually indexed by. And a loop is not atomic: a child created between the
// list and the last delete survives the cascade and is orphaned by exactly the
// operation meant to prevent orphans.
//
// An absent parent is not an error. "No bucket" and "an empty bucket" are the
// same fact for a caller asking that nothing be left behind, and there is
// nothing for a cascade to fail at when there is nothing there; reporting
// ErrNotFound would make every caller special-case the case where its work is
// already done. The count distinguishes them for a caller that wants to log
// what it removed.
//
// The bucket is detached under the write lock BEFORE it is counted, so no
// further write can reach it through this ScopedStore and the count cannot
// disagree with what was removed. A writer that already holds the concrete
// *Store from ForParent can still write into the detached bucket, and that
// write is lost; this store offers no transaction that could prevent it, and
// the alternative of holding the parent lock across the child store's lock for
// the whole operation would invert the lock order the rest of this type uses.
func (s *ScopedStore[T]) DeleteParent(ctx context.Context, parentID string) (uint32, error) {
	s.mu.Lock()
	st, ok := s.stores[parentID]
	if ok {
		delete(s.stores, parentID)
	}
	s.mu.Unlock()

	if !ok {
		return 0, nil
	}
	return st.Count(ctx)
}
