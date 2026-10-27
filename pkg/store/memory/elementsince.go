package memory

import (
	"context"
	"errors"
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

// SinceStore is a ScopedStore that also reports per-element since times.
type SinceStore[T store.Copier[T]] interface {
	store.ScopedStore[T]
	store.ElementSinceReader
}

// NewElementSinceStore wraps inner. A nil clock means time.Now.
//
// The cascade probes decide by type assertion whether a store can remove a
// parent or a prefix, so the returned value carries DeleteParent and
// DeleteParentsWithPrefix only when inner has them; a decorator that always
// had them would pass the probe and then fail the delete.
func NewElementSinceStore[T store.Copier[T]](inner store.ScopedStore[T], fingerprint func(T) map[string]string, clock func() time.Time) SinceStore[T] {
	if clock == nil {
		clock = time.Now
	}
	b := &ElementSinceStore[T]{
		ScopedStore: inner,
		fingerprint: fingerprint,
		clock:       clock,
		since:       make(map[elementKey]map[string]elementSeen),
	}
	_, parent := inner.(parentCascader)
	_, prefix := inner.(prefixCascader)
	switch {
	case parent && prefix:
		return sinceBoth[T]{b}
	case parent:
		return sinceParent[T]{b}
	case prefix:
		return sincePrefix[T]{b}
	}
	return b
}

// NewDERStatusSinceStore wraps inner for DERStatus resources.
func NewDERStatusSinceStore(inner store.ScopedStore[sep2.DERStatus], clock func() time.Time) SinceStore[sep2.DERStatus] {
	return NewElementSinceStore(inner, derStatusElements, clock)
}

// NewDERAvailabilitySinceStore wraps inner for DERAvailability resources.
func NewDERAvailabilitySinceStore(inner store.ScopedStore[sep2.DERAvailability], clock func() time.Time) SinceStore[sep2.DERAvailability] {
	return NewElementSinceStore(inner, derAvailabilityElements, clock)
}

type sinceParent[T store.Copier[T]] struct{ *ElementSinceStore[T] }

func (s sinceParent[T]) DeleteParent(ctx context.Context, parentID string) (uint32, error) {
	return s.deleteParent(ctx, parentID)
}

type sincePrefix[T store.Copier[T]] struct{ *ElementSinceStore[T] }

func (s sincePrefix[T]) DeleteParentsWithPrefix(ctx context.Context, prefix string) (uint32, error) {
	return s.deletePrefix(ctx, prefix)
}

type sinceBoth[T store.Copier[T]] struct{ *ElementSinceStore[T] }

func (s sinceBoth[T]) DeleteParent(ctx context.Context, parentID string) (uint32, error) {
	return s.deleteParent(ctx, parentID)
}

func (s sinceBoth[T]) DeleteParentsWithPrefix(ctx context.Context, prefix string) (uint32, error) {
	return s.deletePrefix(ctx, prefix)
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
	err := s.ScopedStore.Delete(ctx, parentID, id)
	if err == nil || errors.Is(err, store.ErrNotFound) {
		delete(s.since, elementKey{parentID, id})
	}
	return err
}

// deleteParent and deletePrefix run only on wrappers built for an inner store
// that has the capability. They reconcile after the call even when it failed
// part way: a time is dropped for every resource the inner store no longer
// holds, so a partial removal leaves no time for a resource that is gone.
func (s *ElementSinceStore[T]) deleteParent(ctx context.Context, parentID string) (uint32, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n, err := s.ScopedStore.(parentCascader).DeleteParent(ctx, parentID)
	s.reconcile(ctx, func(k elementKey) bool { return k.parentID == parentID })
	return n, err
}

func (s *ElementSinceStore[T]) deletePrefix(ctx context.Context, prefix string) (uint32, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n, err := s.ScopedStore.(prefixCascader).DeleteParentsWithPrefix(ctx, prefix)
	s.reconcile(ctx, func(k elementKey) bool { return strings.HasPrefix(k.parentID, prefix) })
	return n, err
}

// reconcile must be called with s.mu held. A key whose resource cannot be
// confirmed gone (any error other than ErrNotFound) keeps its time.
func (s *ElementSinceStore[T]) reconcile(ctx context.Context, match func(elementKey) bool) {
	for k := range s.since {
		if !match(k) {
			continue
		}
		if _, err := s.ScopedStore.Get(ctx, k.parentID, k.id); errors.Is(err, store.ErrNotFound) {
			delete(s.since, k)
		}
	}
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
