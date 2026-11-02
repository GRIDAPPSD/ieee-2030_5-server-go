package memory

import "github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"

// ScopedStoreFrom is a [ScopedStore] that starts empty at a known time and
// says so: CompleteFrom is that time, as [JournaledScopedStore] reports the
// floor its replay started from. A reader can then tell a record that never
// existed from one a restart lost.
type ScopedStoreFrom[T store.Copier[T]] struct {
	*ScopedStore[T]
	from int64
}

// NewScopedStoreFrom is an empty [ScopedStore] holding every record from
// from on, and none from before it.
func NewScopedStoreFrom[T store.Copier[T]](from int64) *ScopedStoreFrom[T] {
	return &ScopedStoreFrom[T]{ScopedStore: NewScopedStore[T](), from: from}
}

// CompleteFrom is the time the store started empty.
func (s *ScopedStoreFrom[T]) CompleteFrom() int64 { return s.from }
