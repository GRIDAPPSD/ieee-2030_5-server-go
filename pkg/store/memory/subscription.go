package memory

import (
	"context"
	"strings"
	"sync"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
)

// SubscriptionStore wraps the generic Store with secondary indexes
// for lookup by subscribed resource and by device ID.
//
// If a persistence path is configured (see NewSubscriptionStoreWithPersistence,
// GRIDAPPSD/ieee-2030_5-server-go#224), successful Create / Delete
// operations flush a fresh JSON snapshot to disk via atomic rename. The
// default zero-value store has no persistence path and behaves exactly
// like the original pure in-memory store.
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
// persistence (the original behavior; pure RAM).
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

// ListByResource returns all subscriptions for a given resource href,
// paired with their storage IDs. The Manager threads each record's ID
// through to its notification worker so that a 4xx response from the
// receiver can be cleaned up via Delete (CSIP V1.2 ERR-002,
// GRIDAPPSD/ieee-2030_5-server-go#225).
func (s *SubscriptionStore) ListByResource(_ context.Context, resourceHref string) ([]SubscriptionRecord, error) {
	s.idxMu.RLock()
	ids := make([]string, len(s.resourceIndex[resourceHref]))
	copy(ids, s.resourceIndex[resourceHref])
	s.idxMu.RUnlock()

	var result []SubscriptionRecord
	for _, id := range ids {
		sub, err := s.Store.Get(context.Background(), id)
		if err == nil {
			result = append(result, SubscriptionRecord{ID: id, Subscription: sub})
		}
	}
	return result, nil
}

// ListByDevice returns all subscriptions scoped to the given EndDevice
// ID. The EndDevice scope is derived from each subscription's Href
// (the canonical "/edev/{edevID}/sub/{subID}" shape produced by
// HandleCreateSubscription). Subscriptions whose Href does not match
// that shape are absent from the per-EndDevice view.
func (s *SubscriptionStore) ListByDevice(_ context.Context, deviceID string) ([]sep2.Subscription, error) {
	s.idxMu.RLock()
	ids := make([]string, len(s.deviceIndex[deviceID]))
	copy(ids, s.deviceIndex[deviceID])
	s.idxMu.RUnlock()

	result := make([]sep2.Subscription, 0, len(ids))
	for _, id := range ids {
		sub, err := s.Store.Get(context.Background(), id)
		if err == nil {
			result = append(result, sub)
		}
	}
	return result, nil
}

// ListByDeviceWithIDs returns the EndDevice-scoped subscriptions paired
// with their storage IDs, matching the ListByResource record shape.
// Used by the GET /edev/{id}/sub handler so it can page the result by
// stable storage ID. IDs are returned in insertion order to keep paging
// deterministic.
func (s *SubscriptionStore) ListByDeviceWithIDs(_ context.Context, deviceID string) ([]SubscriptionRecord, error) {
	s.idxMu.RLock()
	ids := make([]string, len(s.deviceIndex[deviceID]))
	copy(ids, s.deviceIndex[deviceID])
	s.idxMu.RUnlock()

	result := make([]SubscriptionRecord, 0, len(ids))
	for _, id := range ids {
		sub, err := s.Store.Get(context.Background(), id)
		if err == nil {
			result = append(result, SubscriptionRecord{ID: id, Subscription: sub})
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
	if edevID := edevIDFromHref(sub.Href); edevID != "" {
		s.deviceIndex[edevID] = append(s.deviceIndex[edevID], id)
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
	if edevID := edevIDFromHref(sub.Href); edevID != "" {
		ids := s.deviceIndex[edevID]
		for i, sid := range ids {
			if sid == id {
				s.deviceIndex[edevID] = append(ids[:i], ids[i+1:]...)
				break
			}
		}
	}
}

// edevIDFromHref extracts the EndDevice ID segment from a subscription
// href of the canonical shape "/edev/{edevID}/sub/{subID}".
// Non-conforming hrefs (no /edev/ prefix, missing /sub/ segment, empty
// id) return "" so the caller treats the subscription as out-of-scope
// for per-EndDevice listing.
func edevIDFromHref(href string) string {
	const prefix = "/edev/"
	if !strings.HasPrefix(href, prefix) {
		return ""
	}
	rest := href[len(prefix):]
	slash := strings.IndexByte(rest, '/')
	if slash <= 0 {
		return ""
	}
	if !strings.HasPrefix(rest[slash:], "/sub/") && rest[slash:] != "/sub" {
		return ""
	}
	return rest[:slash]
}
