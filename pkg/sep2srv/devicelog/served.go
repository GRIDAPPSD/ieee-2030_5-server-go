package devicelog

import (
	"context"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/mrid"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
)

// MaxServedMRIDs bounds the mRIDs one request line names, so a poll of a long
// control list cannot grow its line without limit.
const MaxServedMRIDs = 8

// ServedStore decorates a ScopedStore so a successful Get or List notes the
// mRIDs of the resources it returned on the request's Note. The protocol
// DERControl and DefaultDERControl mounts use it; the admin handlers read the
// undecorated store and so record nothing.
type ServedStore[T store.Copier[T]] struct {
	store.ScopedStore[T]
	mrid func(T) string
}

// NewServedStore returns inner unchanged when it is absent, so an unwired
// store keeps its refusal behavior. mridOf reads the resource's mRID.
func NewServedStore[T store.Copier[T]](inner store.ScopedStore[T], mridOf func(T) string) store.ScopedStore[T] {
	if store.IsAbsent(inner) || mridOf == nil {
		return inner
	}
	return &ServedStore[T]{ScopedStore: inner, mrid: mridOf}
}

// Get returns the inner result and, on success, notes the resource's mRID.
func (s *ServedStore[T]) Get(ctx context.Context, parentID, id string) (T, error) {
	r, err := s.ScopedStore.Get(ctx, parentID, id)
	if err == nil {
		s.note(ctx, []T{r})
	}
	return r, err
}

// List returns the inner page and, on success, notes the mRIDs of the page's
// items.
func (s *ServedStore[T]) List(ctx context.Context, parentID string, opts store.ListOptions) (store.ListResult[T], error) {
	res, err := s.ScopedStore.List(ctx, parentID, opts)
	if err == nil {
		s.note(ctx, res.Items)
	}
	return res, err
}

// note sets "mrids" to the first MaxServedMRIDs well-formed mRIDs and
// "mridsMore" to how many served resources are not named in it, malformed or
// empty mRIDs included, so the count never claims fewer than were served. A
// malformed mRID is never written as text.
func (s *ServedStore[T]) note(ctx context.Context, items []T) {
	n := NoteFrom(ctx)
	if n == nil || len(items) == 0 {
		return
	}
	listed := make([]string, 0, min(len(items), MaxServedMRIDs))
	for _, it := range items {
		if len(listed) == MaxServedMRIDs {
			break
		}
		if m, ok := mrid.Canonical(s.mrid(it)); ok {
			listed = append(listed, m)
		}
	}
	if len(listed) > 0 {
		_ = n.Set("mrids", listed)
	}
	if more := len(items) - len(listed); more > 0 {
		_ = n.Set("mridsMore", more)
	}
}
