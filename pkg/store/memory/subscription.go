package memory

import (
	"context"
	"errors"
	"strings"
	"sync"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
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

// Update replaces the stored subscription at id and keeps the resource index
// in step, so a subscription whose subscribedResource changed is delivered for
// the new resource and no longer for the old one. The embedded Store's own
// Update is shadowed on purpose: it would leave the index and the snapshot
// describing the previous record. The device index is untouched, because the
// device is part of the href and an update never moves a subscription between
// devices; the caller must pass the stored Href.
func (s *SubscriptionStore) Update(ctx context.Context, id string, sub sep2.Subscription) error {
	s.idxMu.Lock()
	old, err := s.Store.Get(ctx, id)
	if err != nil {
		s.idxMu.Unlock()
		return err
	}
	if err := s.Store.Update(ctx, id, sub); err != nil {
		s.idxMu.Unlock()
		return err
	}
	if old.SubscribedResource != sub.SubscribedResource {
		unindex(s.resourceIndex, old.SubscribedResource, id)
		index(s.resourceIndex, sub.SubscribedResource, id)
	}
	s.idxMu.Unlock()
	return s.persistSnapshot()
}

func (s *SubscriptionStore) Delete(ctx context.Context, id string) error {
	if err := s.removeOne(ctx, id); err != nil {
		return err
	}
	return s.persistSnapshot()
}

// DeleteIfNotificationURI deletes subscription id only while it still names
// uri, and reports whether it did. A receiver that answers 4xx ends the
// subscription it was sent for; a PUT that has since pointed the record at a
// different receiver must survive that answer. store.ErrNotFound means the
// record is already gone.
func (s *SubscriptionStore) DeleteIfNotificationURI(ctx context.Context, id, uri string) (bool, error) {
	deleted, err := s.removeWhere(ctx, id, func(sub sep2.Subscription) bool { return sub.NotificationURI == uri })
	if err != nil || !deleted {
		return false, err
	}
	return true, s.persistSnapshot()
}

// removeOne removes one subscription and its index entries, returning
// store.ErrNotFound when it is absent. It does not persist; callers do, so a
// batch can flush once. It holds idxMu from the read to the delete, as Update
// does, so a concurrent PUT cannot move the record's resource between the two
// and leave an index entry behind.
func (s *SubscriptionStore) removeOne(ctx context.Context, id string) error {
	_, err := s.removeWhere(ctx, id, nil)
	return err
}

// removeWhere is removeOne limited to a record that satisfies match (nil
// matches any), and reports whether it removed one.
func (s *SubscriptionStore) removeWhere(ctx context.Context, id string, match func(sep2.Subscription) bool) (bool, error) {
	s.idxMu.Lock()
	defer s.idxMu.Unlock()
	old, err := s.Store.Get(ctx, id)
	if err != nil {
		return false, err
	}
	if match != nil && !match(old) {
		return false, nil
	}
	s.removeIndexLocked(id, old)
	return true, s.Store.Delete(ctx, id)
}

// DeleteDevice removes every subscription that belongs to deviceID or watches
// one of its resources, and flushes one snapshot if it removed anything. It
// exists for the EndDevice delete cascade: the device index is reused when the
// same LFDI registers again, so subscriptions left behind would be served
// under the new record, and a subscription another device holds on
// /edev/{id} or beneath it would watch a resource that no longer exists.
//
// A subscription already gone is not an error, and nothing removed means no
// snapshot, so a persistence fault cannot fail a delete that had nothing to
// clean.
func (s *SubscriptionStore) DeleteDevice(ctx context.Context, deviceID string) error {
	return s.deleteDevice(ctx, deviceID, false)
}

// DeleteDeviceResources is [SubscriptionStore.DeleteDevice] for a delete whose
// notification is sent later by someone else (the out-of-band delete): it
// keeps the deleted device's own subscriptions to resources outside its own
// tree, such as the EndDeviceList, so they can still be notified. Those are
// removed when the index is next given to a device.
func (s *SubscriptionStore) DeleteDeviceResources(ctx context.Context, deviceID string) error {
	return s.deleteDevice(ctx, deviceID, true)
}

func (s *SubscriptionStore) deleteDevice(ctx context.Context, deviceID string, keepNotifiable bool) error {
	removed := 0
	for _, id := range s.deviceSubscriptionIDs(deviceID, keepNotifiable) {
		err := s.removeOne(ctx, id)
		switch {
		case err == nil:
			removed++
		case !errors.Is(err, store.ErrNotFound):
			return err
		}
	}
	if removed == 0 {
		return nil
	}
	return s.persistSnapshot()
}

// deviceSubscriptionIDs returns the ids of the subscriptions scoped to
// deviceID plus those, held by any device, whose subscribed resource is the
// device or lies beneath it. With keepNotifiable, the device's own
// subscriptions to resources outside its tree are left out.
func (s *SubscriptionStore) deviceSubscriptionIDs(deviceID string, keepNotifiable bool) []string {
	self := store.DeviceHref(deviceID)
	inTree := func(res string) bool { return res == self || strings.HasPrefix(res, self+"/") }
	s.idxMu.RLock()
	defer s.idxMu.RUnlock()
	seen := make(map[string]bool)
	var ids []string
	add := func(list []string) {
		for _, id := range list {
			if !seen[id] {
				seen[id] = true
				ids = append(ids, id)
			}
		}
	}
	for _, id := range s.deviceIndex[deviceID] {
		if keepNotifiable {
			if sub, err := s.Store.Get(context.Background(), id); err == nil && !inTree(sub.SubscribedResource) {
				continue
			}
		}
		add([]string{id})
	}
	for res, list := range s.resourceIndex {
		if inTree(res) {
			add(list)
		}
	}
	return ids
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
	index(s.resourceIndex, sub.SubscribedResource, id)
	index(s.deviceIndex, edevIDFromHref(sub.Href), id)
}

// removeIndexLocked is removeIndex for a caller already holding idxMu.
func (s *SubscriptionStore) removeIndexLocked(id string, sub sep2.Subscription) {
	unindex(s.resourceIndex, sub.SubscribedResource, id)
	unindex(s.deviceIndex, edevIDFromHref(sub.Href), id)
}

// index appends id under key; an empty key is out of scope and is skipped.
func index(idx map[string][]string, key, id string) {
	if key != "" {
		idx[key] = append(idx[key], id)
	}
}

// unindex removes id from under key, keeping the order of the rest.
func unindex(idx map[string][]string, key, id string) {
	if key == "" {
		return
	}
	ids := idx[key]
	for i, sid := range ids {
		if sid == id {
			idx[key] = append(ids[:i], ids[i+1:]...)
			return
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
