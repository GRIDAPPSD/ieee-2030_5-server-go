package activelist

import (
	"context"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/dercontrol"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/handlers/sep2time"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
)

// EventActive reports whether ev is Active at now (Unix seconds): the one
// rule every Active list serves by. An event with no interval or an ended one
// is not active, whatever it is stamped, because DeriveStatus reads an ended
// 2018-edition event as Active forever. Otherwise the event's EventStatus
// decides; one with none (never derived) reads as a record with no lifecycle.
//
// ev is read as a store serves it, so a store wrapped by
// [dercontrol.NewDerivedStatusStore] hands this its derived cancel and supersede.
func EventActive(ev *sep2.Event, now int64) bool {
	if dercontrol.IntervalOver(ev.Interval, now) {
		return false
	}
	if ev.EventStatus != nil {
		return ev.EventStatus.CurrentStatus == sep2.EventStatusActive
	}
	// An event with no status (never derived) is judged by the DER lifecycle rule.
	return dercontrol.DeriveStatus(now, ev.CreationTime, ev.Interval.Start, dercontrol.LifecycleRecord{}).CurrentStatus == sep2.EventStatusActive
}

// ActiveStore decorates a scoped store of events so List and Count see only
// the items [EventActive] at the instant of the call. It is the single
// active-list filter: each function set's ActiveXList is this decorator over
// that set's own list store, so the set's paging and order apply to the
// filtered collection.
//
// The filter runs before paging, so All and Results describe the active
// members and a page never holds a gap. Wrap it in store.OrderedScoped to
// serve a normative order: that reads this store unbounded and pages itself.
// Every other method delegates unchanged.
type ActiveStore[T store.Copier[T]] struct {
	store.ScopedStore[T]
	event func(item *T) *sep2.Event
}

// NewActiveStore filters inner by the Event event returns for each item.
func NewActiveStore[T store.Copier[T]](inner store.ScopedStore[T], event func(item *T) *sep2.Event) *ActiveStore[T] {
	return &ActiveStore[T]{ScopedStore: inner, event: event}
}

var _ store.ScopedStore[sep2.DERControl] = (*ActiveStore[sep2.DERControl])(nil)

// List returns the active members under parentID. Items keep the inner
// store's order; Start and Limit cut that order after the filter, and
// Unbounded returns every active member.
func (s *ActiveStore[T]) List(ctx context.Context, parentID string, opts store.ListOptions) (store.ListResult[T], error) {
	if err := opts.Validate(); err != nil {
		return store.ListResult[T]{}, err
	}
	whole, err := s.active(ctx, parentID, opts.Sort)
	if err != nil {
		return store.ListResult[T]{}, err
	}
	all := uint32(len(whole))
	items := whole[min(opts.Start, all):]
	if !opts.Unbounded {
		items = items[:min(opts.Limit, uint32(len(items)))]
	}
	if len(items) == 0 {
		items = nil
	}
	return store.ListResult[T]{All: all, Results: uint32(len(items)), Items: items}, nil
}

// Count is the number of active members under parentID.
func (s *ActiveStore[T]) Count(ctx context.Context, parentID string) (uint32, error) {
	whole, err := s.active(ctx, parentID, store.SortByIDAsc)
	return uint32(len(whole)), err
}

func (s *ActiveStore[T]) active(ctx context.Context, parentID string, sort store.SortKey) ([]T, error) {
	whole, err := s.ScopedStore.List(ctx, parentID, store.ListOptions{Unbounded: true, Sort: sort})
	if err != nil {
		return nil, err
	}
	now := sep2time.Now().Unix()
	var kept []T
	for i := range whole.Items {
		if EventActive(s.event(&whole.Items[i]), now) {
			kept = append(kept, whole.Items[i])
		}
	}
	return kept, nil
}
