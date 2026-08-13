package store

import "context"

// Under returns the collection beneath parentID as a flat [ResourceStore],
// so a caller that already knows which parent it is working in can hand that
// collection to code written against the flat contract.
//
// It exists because the flat and scoped halves of this package are two shapes
// of the same collection, and the generic list handler is written against the
// flat one. Before the contract was interface-typed, a caller reached that
// shape through the in-memory implementation's ForParent, which returns a
// concrete per-parent store: the single largest reason handler code named
// *memory.ScopedStore rather than this contract. Under is the contract's own
// answer, and it is what the package documentation's note about ForParent's
// deliberate absence points callers at.
//
// It is a VIEW, not a materialization. Nothing is allocated in the underlying
// store, no parent bucket comes into existence, and every method is forwarded
// with parentID re-attached. That is the substantive difference from ForParent,
// whose create-on-read is the behaviour the contract refuses to carry: a GET
// through a view of an unknown parent reads nothing and creates nothing.
//
// The view holds parentID, so a caller that needs a second parent takes a
// second view rather than mutating this one.
func Under[T Copier[T]](s ScopedStore[T], parentID string) ResourceStore[T] {
	return scopedView[T]{scoped: s, parentID: parentID}
}

// UnderReader is [Under] for the read-only half of the contract: it returns the
// collection beneath parentID as a [ResourceReader].
//
// It takes a [ScopedReader] rather than a [ScopedStore] so a telemetry or
// administrative caller holding a read-only handle can narrow to one parent
// without first being handed write access it does not need. Narrowing scope
// must not widen privilege.
func UnderReader[T Copier[T]](s ScopedReader[T], parentID string) ResourceReader[T] {
	return scopedReaderView[T]{scoped: s, parentID: parentID}
}

// scopedReaderView adapts a [ScopedReader] bound to one parent to the flat
// [ResourceReader] contract.
type scopedReaderView[T Copier[T]] struct {
	scoped   ScopedReader[T]
	parentID string
}

func (v scopedReaderView[T]) Get(ctx context.Context, id string) (T, error) {
	return v.scoped.Get(ctx, v.parentID, id)
}

func (v scopedReaderView[T]) List(ctx context.Context, opts ListOptions) (ListResult[T], error) {
	return v.scoped.List(ctx, v.parentID, opts)
}

func (v scopedReaderView[T]) Count(ctx context.Context) (uint32, error) {
	return v.scoped.Count(ctx, v.parentID)
}

// scopedView adapts a [ScopedStore] bound to one parent to the flat
// [ResourceStore] contract.
type scopedView[T Copier[T]] struct {
	scoped   ScopedStore[T]
	parentID string
}

func (v scopedView[T]) Get(ctx context.Context, id string) (T, error) {
	return v.scoped.Get(ctx, v.parentID, id)
}

func (v scopedView[T]) List(ctx context.Context, opts ListOptions) (ListResult[T], error) {
	return v.scoped.List(ctx, v.parentID, opts)
}

func (v scopedView[T]) Count(ctx context.Context) (uint32, error) {
	return v.scoped.Count(ctx, v.parentID)
}

func (v scopedView[T]) Create(ctx context.Context, id string, resource T) error {
	return v.scoped.Create(ctx, v.parentID, id, resource)
}

func (v scopedView[T]) Update(ctx context.Context, id string, resource T) error {
	return v.scoped.Update(ctx, v.parentID, id, resource)
}

func (v scopedView[T]) Delete(ctx context.Context, id string) error {
	return v.scoped.Delete(ctx, v.parentID, id)
}
