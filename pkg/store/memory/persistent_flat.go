package memory

import (
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
)

// flatParent is the one parent id every record of a flat persisted store sits
// under. It is part of the on-disk shape, so changing it orphans a snapshot.
const flatParent = "all"

// NewPersistentStore builds a flat [store.ResourceStore] that reloads from a
// JSON snapshot at path. An empty path returns the plain in-memory [Store], so
// a caller with no data dir behaves exactly as before.
//
// It is a [PersistentScopedStore] with a single fixed parent, viewed through
// [store.Under]: the snapshot, rollback and locking rules are that store's, not
// a second copy. The cost is that the file names each record's parent ("all").
// label names the collection in error messages.
func NewPersistentStore[T store.Copier[T]](path, label string, opts ...PersistentScopedOption[T]) (store.ResourceStore[T], error) {
	if path == "" {
		return NewStore[T](), nil
	}
	scoped, err := NewPersistentScopedStore[T](path, label, opts...)
	if err != nil {
		return nil, err
	}
	return store.Under[T](scoped, flatParent), nil
}
