package dercontrol

import (
	"context"
	"errors"
	"fmt"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/handlers/sep2time"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
)

// LifecycleReader is the read-only subset of a lifecycle store the
// serve-time EventStatus derivation needs.
type LifecycleReader interface {
	Get(ctx context.Context, parentID, id string) (LifecycleRecord, error)
}

// StatusPolicy tells a DerivedStatusStore how to reach an item's Event and
// store id, and which items it may derive a status for.
type StatusPolicy[T any] struct {
	// Event returns the item's Event, read for its interval and creation
	// time and written with the derived EventStatus.
	Event func(item *T) *sep2.Event

	// ID recovers the item's store id from the item itself (its href);
	// ok is false when it cannot.
	ID func(parentID string, item T) (string, bool)

	// ServerAuthored is true when every item in the store was created by
	// this server, so every item is derived: one with no lifecycle record
	// reads the zero record, one with no interval reads start 0, and a list
	// member with no id fails the list. When false, only items with a
	// lifecycle record are this server's to derive; any other item (a boot
	// fixture or CSIP loader control) is served exactly as stored.
	ServerAuthored bool
}

// DerivedStatusStore decorates a scoped store of events so Get and List
// serve the EventStatus DeriveStatus computes from each item's lifecycle
// record, keyed by the same (parent, id) pair as the item. Every write
// delegates unchanged, so the stored item is never rewritten.
type DerivedStatusStore[T store.Copier[T]] struct {
	inner      store.ScopedStore[T]
	lifecycles LifecycleReader
	policy     StatusPolicy[T]
}

// NewDerivedStatusStore decorates inner with lifecycles under policy.
func NewDerivedStatusStore[T store.Copier[T]](inner store.ScopedStore[T], lifecycles LifecycleReader, policy StatusPolicy[T]) *DerivedStatusStore[T] {
	return &DerivedStatusStore[T]{inner: inner, lifecycles: lifecycles, policy: policy}
}

// Get returns the item with its EventStatus derived. A lifecycle lookup
// failure other than "no record" fails the read: EventStatus is mandatory
// (2018 multiplicity 1, and clients SHALL check it before acting), so a
// skipped derivation could serve a cancelled event as live.
func (s *DerivedStatusStore[T]) Get(ctx context.Context, parentID, id string) (T, error) {
	item, err := s.inner.Get(ctx, parentID, id)
	if err != nil {
		return item, err
	}
	if err := s.derive(ctx, sep2time.Now().Unix(), parentID, id, &item); err != nil {
		var zero T
		return zero, err
	}
	return item, nil
}

// List returns a page with each member derived against one instant, and
// fails whole on a lookup failure, as Get does.
func (s *DerivedStatusStore[T]) List(ctx context.Context, parentID string, opts store.ListOptions) (store.ListResult[T], error) {
	result, err := s.inner.List(ctx, parentID, opts)
	if err != nil {
		return result, err
	}
	now := sep2time.Now().Unix()
	for i := range result.Items {
		id, ok := s.policy.ID(parentID, result.Items[i])
		if !ok {
			if s.policy.ServerAuthored {
				return store.ListResult[T]{}, fmt.Errorf("dercontrol: a member of %s has no store id in its href", parentID)
			}
			continue
		}
		if err := s.derive(ctx, now, parentID, id, &result.Items[i]); err != nil {
			return store.ListResult[T]{}, err
		}
	}
	return result, nil
}

func (s *DerivedStatusStore[T]) derive(ctx context.Context, now int64, parentID, id string, item *T) error {
	ev := s.policy.Event(item)
	if ev.Interval == nil && !s.policy.ServerAuthored {
		return nil
	}
	lc, err := s.lifecycles.Get(ctx, parentID, id)
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			return fmt.Errorf("dercontrol: loading lifecycle record for %s/%s: %w", parentID, id, err)
		}
		if !s.policy.ServerAuthored {
			return nil
		}
		lc = LifecycleRecord{}
	}
	var start int64
	if ev.Interval != nil {
		start = ev.Interval.Start
	}
	status := DeriveStatus(now, ev.CreationTime, start, lc)
	ev.EventStatus = &status
	return nil
}

func (s *DerivedStatusStore[T]) Count(ctx context.Context, parentID string) (uint32, error) {
	return s.inner.Count(ctx, parentID)
}

func (s *DerivedStatusStore[T]) HasParent(ctx context.Context, parentID string) (bool, error) {
	return s.inner.HasParent(ctx, parentID)
}

func (s *DerivedStatusStore[T]) Parents(ctx context.Context) ([]string, error) {
	return s.inner.Parents(ctx)
}

func (s *DerivedStatusStore[T]) Create(ctx context.Context, parentID, id string, item T) error {
	return s.inner.Create(ctx, parentID, id, item)
}

func (s *DerivedStatusStore[T]) Update(ctx context.Context, parentID, id string, item T) error {
	return s.inner.Update(ctx, parentID, id, item)
}

func (s *DerivedStatusStore[T]) Delete(ctx context.Context, parentID, id string) error {
	return s.inner.Delete(ctx, parentID, id)
}
