package store

import "context"

// AsReader narrows a [ResourceStore] to a [ResourceReader] by WRAPPING it,
// not by assigning it to a narrower interface variable. A plain assignment
// leaves the underlying value's dynamic type unchanged, so a caller can
// recover the write methods with a type assertion or a reflective method
// lookup; wrapping changes the dynamic type itself, so neither escape
// recovers Create, Update or Delete. This is [UnderReader]'s move without a
// parentID: where UnderReader narrows scope, AsReader narrows privilege, and
// the same rule applies: narrowing must not leave itself reversible.
func AsReader[T Copier[T]](s ResourceReader[T]) ResourceReader[T] {
	return resourceReaderOnly[T]{reader: s}
}

// resourceReaderOnly forwards only the [ResourceReader] methods of the
// reader it wraps, which may in fact satisfy the wider [ResourceStore].
type resourceReaderOnly[T Copier[T]] struct {
	reader ResourceReader[T]
}

func (v resourceReaderOnly[T]) Get(ctx context.Context, id string) (T, error) {
	return v.reader.Get(ctx, id)
}

func (v resourceReaderOnly[T]) List(ctx context.Context, opts ListOptions) (ListResult[T], error) {
	return v.reader.List(ctx, opts)
}

func (v resourceReaderOnly[T]) Count(ctx context.Context) (uint32, error) {
	return v.reader.Count(ctx)
}

// AsScopedReader is [AsReader] for the parent-scoped half of the contract:
// it narrows a [ScopedStore] to a [ScopedReader] by wrapping.
func AsScopedReader[T Copier[T]](s ScopedReader[T]) ScopedReader[T] {
	return scopedReaderOnly[T]{reader: s}
}

// scopedReaderOnly forwards only the [ScopedReader] methods of the reader it
// wraps, which may in fact satisfy the wider [ScopedStore].
type scopedReaderOnly[T Copier[T]] struct {
	reader ScopedReader[T]
}

func (v scopedReaderOnly[T]) Get(ctx context.Context, parentID, id string) (T, error) {
	return v.reader.Get(ctx, parentID, id)
}

func (v scopedReaderOnly[T]) List(ctx context.Context, parentID string, opts ListOptions) (ListResult[T], error) {
	return v.reader.List(ctx, parentID, opts)
}

func (v scopedReaderOnly[T]) Count(ctx context.Context, parentID string) (uint32, error) {
	return v.reader.Count(ctx, parentID)
}

func (v scopedReaderOnly[T]) HasParent(ctx context.Context, parentID string) (bool, error) {
	return v.reader.HasParent(ctx, parentID)
}

func (v scopedReaderOnly[T]) Parents(ctx context.Context) ([]string, error) {
	return v.reader.Parents(ctx)
}
