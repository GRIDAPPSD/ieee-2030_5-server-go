// Package storetest provides a conformance suite for the store contract and a
// second implementation of it.
//
// It exists to answer a question the contract cannot answer about itself:
// whether the interfaces in pkg/store describe a storage boundary, or merely
// describe the shape of the in-memory store that happened to be written first.
// The suite here runs unchanged against any implementation, and [Fake] and
// [ScopedFake] are a genuinely independent second implementation that it
// passes, which is the evidence that the boundary is real.
//
// A durable backend should be developed against [RunResourceStoreSuite],
// [RunScopedStoreSuite] and [RunTransientFailureSuite] rather than against a
// hand-written set of tests, so that "it satisfies the contract" is a checked
// claim rather than a hopeful one.
//
// This package is for testing only. It imports testing and nothing in the
// production path imports it.
package storetest

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
)

// Resource is the value type the conformance suite stores.
//
// It carries a slice field on purpose: copy semantics are only meaningfully
// testable against a type where a shallow copy would leak a shared reference.
type Resource struct {
	ID   string
	Body string
	Tags []string
}

// Copy returns an independent copy, cloning the slice field.
func (r Resource) Copy() Resource {
	out := r
	out.Tags = slices.Clone(r.Tags)
	return out
}

// entry is one stored resource. The flat fake leaves parent empty.
type entry[T store.Copier[T]] struct {
	parent string
	id     string
	value  T
}

// Fake is a second implementation of [store.ResourceStore], backed by an
// unordered slice that is sorted at list time.
//
// The structure is deliberately unlike the in-memory store's map plus
// maintained sort order: an interface that only the original implementation's
// data structure can satisfy is not a contract. Nothing here is efficient, and
// nothing here needs to be.
//
// A Fake can be told to fail, which the in-memory store cannot do. That is
// what makes the transient-failure behaviour of the contract testable at all.
type Fake[T store.Copier[T]] struct {
	mu      sync.Mutex
	entries []entry[T]
	failure error
}

// NewFake returns an empty Fake.
func NewFake[T store.Copier[T]]() *Fake[T] {
	return &Fake[T]{}
}

// SetFailure makes every subsequent operation return err, modeling a backend
// that has become unreachable. Pass nil to restore normal operation.
//
// The injected error is returned as-is and is not one of the store sentinels,
// so a caller matching with errors.Is sees neither ErrNotFound nor
// ErrAlreadyExists, which is exactly the case the error contract exists for.
func (f *Fake[T]) SetFailure(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failure = err
}

func (f *Fake[T]) find(id string) int {
	for i := range f.entries {
		if f.entries[i].id == id {
			return i
		}
	}
	return -1
}

// Get implements store.ResourceReader.
func (f *Fake[T]) Get(_ context.Context, id string) (T, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	var zero T
	if f.failure != nil {
		return zero, f.failure
	}
	i := f.find(id)
	if i < 0 {
		return zero, store.ErrNotFound
	}
	return f.entries[i].value.Copy(), nil
}

// List implements store.ResourceReader.
func (f *Fake[T]) List(_ context.Context, opts store.ListOptions) (store.ListResult[T], error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.failure != nil {
		return store.ListResult[T]{}, f.failure
	}
	return paginate(slices.Clone(f.entries), opts)
}

// Count implements store.ResourceReader.
func (f *Fake[T]) Count(_ context.Context) (uint32, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.failure != nil {
		return 0, f.failure
	}
	return uint32(len(f.entries)), nil
}

// Create implements store.ResourceStore.
func (f *Fake[T]) Create(_ context.Context, id string, resource T) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.failure != nil {
		return f.failure
	}
	if f.find(id) >= 0 {
		return store.ErrAlreadyExists
	}
	f.entries = append(f.entries, entry[T]{id: id, value: resource.Copy()})
	return nil
}

// Update implements store.ResourceStore.
func (f *Fake[T]) Update(_ context.Context, id string, resource T) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.failure != nil {
		return f.failure
	}
	i := f.find(id)
	if i < 0 {
		return store.ErrNotFound
	}
	f.entries[i].value = resource.Copy()
	return nil
}

// Delete implements store.ResourceStore.
func (f *Fake[T]) Delete(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.failure != nil {
		return f.failure
	}
	i := f.find(id)
	if i < 0 {
		return store.ErrNotFound
	}
	f.entries = slices.Delete(f.entries, i, i+1)
	return nil
}

// ScopedFake is a second implementation of [store.ScopedStore], backed by a
// flat slice of (parent, id, value) tuples.
//
// The in-memory implementation is a map of per-parent sub-stores, so a parent
// is a first-class object there and can exist while holding nothing. Here a
// parent exists only as a property of the tuples that mention it, which is
// closer to how a relational backend would store this and is the reason the
// contract states only that a parent holding at least one resource must be
// reported.
type ScopedFake[T store.Copier[T]] struct {
	mu      sync.Mutex
	entries []entry[T]
	failure error
}

// NewScopedFake returns an empty ScopedFake.
func NewScopedFake[T store.Copier[T]]() *ScopedFake[T] {
	return &ScopedFake[T]{}
}

// SetFailure makes every subsequent operation return err. See
// [Fake.SetFailure].
func (f *ScopedFake[T]) SetFailure(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failure = err
}

func (f *ScopedFake[T]) find(parentID, id string) int {
	for i := range f.entries {
		if f.entries[i].parent == parentID && f.entries[i].id == id {
			return i
		}
	}
	return -1
}

func (f *ScopedFake[T]) under(parentID string) []entry[T] {
	var matched []entry[T]
	for i := range f.entries {
		if f.entries[i].parent == parentID {
			matched = append(matched, f.entries[i])
		}
	}
	return matched
}

// Get implements store.ScopedReader.
func (f *ScopedFake[T]) Get(_ context.Context, parentID, id string) (T, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	var zero T
	if f.failure != nil {
		return zero, f.failure
	}
	i := f.find(parentID, id)
	if i < 0 {
		return zero, store.ErrNotFound
	}
	return f.entries[i].value.Copy(), nil
}

// List implements store.ScopedReader.
func (f *ScopedFake[T]) List(_ context.Context, parentID string, opts store.ListOptions) (store.ListResult[T], error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.failure != nil {
		return store.ListResult[T]{}, f.failure
	}
	return paginate(f.under(parentID), opts)
}

// Count implements store.ScopedReader.
func (f *ScopedFake[T]) Count(_ context.Context, parentID string) (uint32, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.failure != nil {
		return 0, f.failure
	}
	return uint32(len(f.under(parentID))), nil
}

// HasParent implements store.ScopedReader.
func (f *ScopedFake[T]) HasParent(_ context.Context, parentID string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.failure != nil {
		return false, f.failure
	}
	return len(f.under(parentID)) > 0, nil
}

// Parents implements store.ScopedReader.
func (f *ScopedFake[T]) Parents(_ context.Context) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.failure != nil {
		return nil, f.failure
	}
	seen := make(map[string]struct{}, len(f.entries))
	parents := make([]string, 0, len(f.entries))
	for i := range f.entries {
		if _, ok := seen[f.entries[i].parent]; ok {
			continue
		}
		seen[f.entries[i].parent] = struct{}{}
		parents = append(parents, f.entries[i].parent)
	}
	slices.Sort(parents)
	return parents, nil
}

// Create implements store.ScopedStore.
func (f *ScopedFake[T]) Create(_ context.Context, parentID, id string, resource T) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.failure != nil {
		return f.failure
	}
	if f.find(parentID, id) >= 0 {
		return store.ErrAlreadyExists
	}
	f.entries = append(f.entries, entry[T]{parent: parentID, id: id, value: resource.Copy()})
	return nil
}

// Update implements store.ScopedStore.
func (f *ScopedFake[T]) Update(_ context.Context, parentID, id string, resource T) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.failure != nil {
		return f.failure
	}
	i := f.find(parentID, id)
	if i < 0 {
		return store.ErrNotFound
	}
	f.entries[i].value = resource.Copy()
	return nil
}

// Delete implements store.ScopedStore.
func (f *ScopedFake[T]) Delete(_ context.Context, parentID, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.failure != nil {
		return f.failure
	}
	i := f.find(parentID, id)
	if i < 0 {
		return store.ErrNotFound
	}
	f.entries = slices.Delete(f.entries, i, i+1)
	return nil
}

// paginate sorts matched into the requested order and applies After, Start and
// Limit. It is the fake's own implementation of the paging contract, written
// against the documented semantics rather than copied from the in-memory
// store, so that agreement between the two is evidence rather than tautology.
func paginate[T store.Copier[T]](matched []entry[T], opts store.ListOptions) (store.ListResult[T], error) {
	if err := opts.Validate(); err != nil {
		return store.ListResult[T]{}, err
	}

	switch opts.Sort {
	case store.SortByIDAsc:
		slices.SortFunc(matched, func(a, b entry[T]) int { return strings.Compare(a.id, b.id) })
	case store.SortByIDDesc:
		slices.SortFunc(matched, func(a, b entry[T]) int { return strings.Compare(b.id, a.id) })
	default:
		return store.ListResult[T]{}, fmt.Errorf("%w: %d", store.ErrUnsupportedSort, uint8(opts.Sort))
	}

	all := uint32(len(matched))

	if opts.After != "" {
		cut := 0
		for cut < len(matched) && !strictlyAfter(matched[cut].id, opts.After, opts.Sort) {
			cut++
		}
		matched = matched[cut:]
	}

	// An unbounded page skips the limit entirely rather than substituting a
	// large one, so nothing here depends on the collection's size.
	if opts.Start >= uint32(len(matched)) || (!opts.Unbounded && opts.Limit == 0) {
		return store.ListResult[T]{All: all}, nil
	}
	matched = matched[opts.Start:]
	if !opts.Unbounded && opts.Limit < uint32(len(matched)) {
		matched = matched[:opts.Limit]
	}

	items := make([]T, 0, len(matched))
	for i := range matched {
		items = append(items, matched[i].value.Copy())
	}
	return store.ListResult[T]{All: all, Results: uint32(len(items)), Items: items}, nil
}

// strictlyAfter reports whether id falls after the given key in sort order.
func strictlyAfter(id, after string, sort store.SortKey) bool {
	if sort == store.SortByIDDesc {
		return id < after
	}
	return id > after
}

// Compile-time proof that the fakes satisfy the same contract the in-memory
// store does. If the interfaces were shaped around the in-memory store's data
// structures, these assertions are where that would show.
var (
	_ store.ResourceReader[Resource] = (*Fake[Resource])(nil)
	_ store.ResourceStore[Resource]  = (*Fake[Resource])(nil)
	_ store.ScopedReader[Resource]   = (*ScopedFake[Resource])(nil)
	_ store.ScopedStore[Resource]    = (*ScopedFake[Resource])(nil)
)
