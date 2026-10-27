package memory

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
)

// ElementSinceStore decorates a ScopedStore so that each element of a stored
// resource carries the time its current value was first seen. The store keeps
// only the latest copy of a resource, so "unchanged for 9 h" cannot be read
// from it; this decorator is the one owner of that fact.
//
// Times live in memory beside the wrapped store and are dropped with the
// resource, so they follow it across a restart exactly when it does. Only a
// write the inner store accepted is recorded: a refused write changes nothing.
type ElementSinceStore[T store.Copier[T]] struct {
	store.ScopedStore[T]

	// fingerprint maps a resource to its present elements and a comparable
	// rendering of each value. It must leave out the per-post timestamps
	// (dateTime, readingTime), which change on every report of an unchanged
	// value.
	fingerprint func(T) map[string]string
	clock       func() time.Time

	mu    sync.Mutex
	since map[elementKey]map[string]elementSeen
}

type elementKey struct{ parentID, id string }

type elementSeen struct {
	value string
	at    time.Time
}

var _ store.ElementSinceReader = (*ElementSinceStore[sep2.DERStatus])(nil)

// NewElementSinceStore wraps inner. A nil clock means time.Now.
func NewElementSinceStore[T store.Copier[T]](inner store.ScopedStore[T], fingerprint func(T) map[string]string, clock func() time.Time) *ElementSinceStore[T] {
	if clock == nil {
		clock = time.Now
	}
	return &ElementSinceStore[T]{
		ScopedStore: inner,
		fingerprint: fingerprint,
		clock:       clock,
		since:       make(map[elementKey]map[string]elementSeen),
	}
}

// NewDERStatusSinceStore wraps inner for DERStatus resources.
func NewDERStatusSinceStore(inner store.ScopedStore[sep2.DERStatus], clock func() time.Time) *ElementSinceStore[sep2.DERStatus] {
	return NewElementSinceStore(inner, derStatusElements, clock)
}

// NewDERAvailabilitySinceStore wraps inner for DERAvailability resources.
func NewDERAvailabilitySinceStore(inner store.ScopedStore[sep2.DERAvailability], clock func() time.Time) *ElementSinceStore[sep2.DERAvailability] {
	return NewElementSinceStore(inner, derAvailabilityElements, clock)
}

// Create stores resource and records its elements.
func (s *ElementSinceStore[T]) Create(ctx context.Context, parentID, id string, resource T) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ScopedStore.Create(ctx, parentID, id, resource); err != nil {
		return err
	}
	s.record(elementKey{parentID, id}, resource)
	return nil
}

// Update replaces the stored resource and records its elements.
func (s *ElementSinceStore[T]) Update(ctx context.Context, parentID, id string, resource T) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ScopedStore.Update(ctx, parentID, id, resource); err != nil {
		return err
	}
	s.record(elementKey{parentID, id}, resource)
	return nil
}

// Delete removes the resource and its recorded times.
func (s *ElementSinceStore[T]) Delete(ctx context.Context, parentID, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ScopedStore.Delete(ctx, parentID, id); err != nil {
		return err
	}
	delete(s.since, elementKey{parentID, id})
	return nil
}

// DeleteParent forwards the bulk removal the inner store offers and drops the
// times under that parent. It refuses, as the cascade does, when the inner
// store has no such capability, so a decorated store never hides one.
func (s *ElementSinceStore[T]) DeleteParent(ctx context.Context, parentID string) (uint32, error) {
	inner, ok := s.ScopedStore.(parentCascader)
	if !ok {
		return 0, fmt.Errorf("the store (%T) cannot cascade a parent delete", s.ScopedStore)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	n, err := inner.DeleteParent(ctx, parentID)
	if err != nil {
		return n, err
	}
	for k := range s.since {
		if k.parentID == parentID {
			delete(s.since, k)
		}
	}
	return n, nil
}

// DeleteParentsWithPrefix forwards the one-operation prefix removal and drops
// the times under every parent it matched.
func (s *ElementSinceStore[T]) DeleteParentsWithPrefix(ctx context.Context, prefix string) (uint32, error) {
	inner, ok := s.ScopedStore.(prefixCascader)
	if !ok {
		return 0, fmt.Errorf("the store (%T) cannot cascade a prefix delete", s.ScopedStore)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	n, err := inner.DeleteParentsWithPrefix(ctx, prefix)
	if err != nil {
		return n, err
	}
	for k := range s.since {
		if strings.HasPrefix(k.parentID, prefix) {
			delete(s.since, k)
		}
	}
	return n, nil
}

// ElementSince implements store.ElementSinceReader.
func (s *ElementSinceStore[T]) ElementSince(parentID, id string) map[string]time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	seen := s.since[elementKey{parentID, id}]
	out := make(map[string]time.Time, len(seen))
	for name, e := range seen {
		out[name] = e.at
	}
	return out
}

// record must be called with s.mu held. An element absent from the new
// resource is dropped, so one that returns later starts a new interval.
func (s *ElementSinceStore[T]) record(k elementKey, resource T) {
	now := s.clock()
	prev := s.since[k]
	next := make(map[string]elementSeen)
	for name, value := range s.fingerprint(resource) {
		if p, ok := prev[name]; ok && p.value == value {
			next[name] = p
			continue
		}
		next[name] = elementSeen{value: value, at: now}
	}
	s.since[k] = next
}

func derStatusElements(s sep2.DERStatus) map[string]string {
	m := make(map[string]string)
	if s.AlarmStatus != nil {
		m["alarmStatus"] = fmt.Sprint(uint32(*s.AlarmStatus))
	}
	if s.ConnectStatus != nil {
		m["connectStatus"] = fmt.Sprint(uint8(s.ConnectStatus.Value))
	}
	if s.GenConnectStatus != nil {
		m["genConnectStatus"] = fmt.Sprint(uint8(s.GenConnectStatus.Value))
	}
	if s.StorConnectStatus != nil {
		m["storConnectStatus"] = fmt.Sprint(uint8(s.StorConnectStatus.Value))
	}
	if s.InverterStatus != nil {
		m["inverterStatus"] = fmt.Sprint(s.InverterStatus.Value)
	}
	if s.OperationalModeStatus != nil {
		m["operationalModeStatus"] = fmt.Sprint(s.OperationalModeStatus.Value)
	}
	if s.StateOfChargeStatus != nil {
		m["stateOfChargeStatus"] = fmt.Sprint(s.StateOfChargeStatus.Value)
	}
	if s.StorageModeStatus != nil {
		m["storageModeStatus"] = fmt.Sprint(s.StorageModeStatus.Value)
	}
	return m
}

func derAvailabilityElements(a sep2.DERAvailability) map[string]string {
	m := make(map[string]string)
	if a.AvailabilityDuration != nil {
		m["availabilityDuration"] = fmt.Sprint(*a.AvailabilityDuration)
	}
	if a.MaxChargeDuration != nil {
		m["maxChargeDuration"] = fmt.Sprint(*a.MaxChargeDuration)
	}
	if a.ReserveChargePercent != nil {
		m["reserveChargePercent"] = fmt.Sprint(uint16(*a.ReserveChargePercent))
	}
	if a.ReservePercent != nil {
		m["reservePercent"] = fmt.Sprint(uint16(*a.ReservePercent))
	}
	if a.StatVarAvail != nil {
		m["statVarAvail"] = fmt.Sprint(a.StatVarAvail.Multiplier, ",", a.StatVarAvail.Value)
	}
	if a.StatWAvail != nil {
		m["statWAvail"] = fmt.Sprint(a.StatWAvail.Multiplier, ",", a.StatWAvail.Value)
	}
	return m
}
