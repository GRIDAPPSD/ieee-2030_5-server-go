package memory

import (
	"context"
	"sync"

	"github.com/GRIDAPPSD/ieee-2030_5-go/pkg/sep2"
)

// SubscriptionStore wraps the generic Store with secondary indexes
// for lookup by subscribed resource and by device ID.
//
// If a persistence path is configured (see NewSubscriptionStoreWithPersistence),
// successful Create / Delete operations flush a fresh JSON snapshot to disk
// via atomic rename. The default zero-value store has no persistence path
// and behaves exactly like the pre-IEEE-077 in-memory store.
type SubscriptionStore struct {
	*Store[sep2.Subscription]
	idxMu         sync.RWMutex
	resourceIndex map[string][]string // subscribedResource -> []subID
	deviceIndex   map[string][]string // deviceID -> []subID

	// persistMu serializes on-disk snapshot writes. Held only for the
	// duration of marshal+write+rename; the primary Store and index
	// locks are released before the disk syscall to keep readers
	// non-blocked during persistence.
	persistMu   sync.Mutex
	persistPath string // empty = persistence disabled (pure in-memory)
}

// NewSubscriptionStore creates an in-memory SubscriptionStore with no
// persistence (the historical pre-IEEE-077 behavior; pure RAM).
func NewSubscriptionStore() *SubscriptionStore {
	return &SubscriptionStore{
		Store:         NewStore[sep2.Subscription](),
		resourceIndex: make(map[string][]string),
		deviceIndex:   make(map[string][]string),
	}
}

func (s *SubscriptionStore) Create(ctx context.Context, id string, sub sep2.Subscription) error {
	if err := s.Store.Create(ctx, id, sub); err != nil {
		return err
	}
	s.indexSub(id, sub)
	return s.persistSnapshot()
}

func (s *SubscriptionStore) Delete(ctx context.Context, id string) error {
	old, err := s.Store.Get(ctx, id)
	if err != nil {
		return err
	}
	s.removeIndex(id, old)
	if err := s.Store.Delete(ctx, id); err != nil {
		return err
	}
	return s.persistSnapshot()
}

// ListByResource returns all subscriptions for a given resource href.
func (s *SubscriptionStore) ListByResource(_ context.Context, resourceHref string) ([]sep2.Subscription, error) {
	s.idxMu.RLock()
	ids := s.resourceIndex[resourceHref]
	s.idxMu.RUnlock()

	var result []sep2.Subscription
	for _, id := range ids {
		sub, err := s.Store.Get(context.Background(), id)
		if err == nil {
			result = append(result, sub)
		}
	}
	return result, nil
}

// ListByDevice returns all subscriptions for a given device ID.
func (s *SubscriptionStore) ListByDevice(_ context.Context, deviceID string) ([]sep2.Subscription, error) {
	s.idxMu.RLock()
	ids := s.deviceIndex[deviceID]
	s.idxMu.RUnlock()

	var result []sep2.Subscription
	for _, id := range ids {
		sub, err := s.Store.Get(context.Background(), id)
		if err == nil {
			result = append(result, sub)
		}
	}
	return result, nil
}

func (s *SubscriptionStore) indexSub(id string, sub sep2.Subscription) {
	s.idxMu.Lock()
	defer s.idxMu.Unlock()
	if sub.SubscribedResource != "" {
		s.resourceIndex[sub.SubscribedResource] = append(s.resourceIndex[sub.SubscribedResource], id)
	}
}

func (s *SubscriptionStore) removeIndex(id string, sub sep2.Subscription) {
	s.idxMu.Lock()
	defer s.idxMu.Unlock()

	if sub.SubscribedResource != "" {
		ids := s.resourceIndex[sub.SubscribedResource]
		for i, sid := range ids {
			if sid == id {
				s.resourceIndex[sub.SubscribedResource] = append(ids[:i], ids[i+1:]...)
				break
			}
		}
	}
}

