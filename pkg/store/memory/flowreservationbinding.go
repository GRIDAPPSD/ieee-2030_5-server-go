package memory

import (
	"context"
	"errors"
	"fmt"
	"log"
	"slices"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
)

// Advertising the flow reservation lists is the mount decision, not a
// handler's.
//
// The rule this type enforces, in both directions:
//
//	an EndDevice served under key k carries FlowReservationRequestListLink
//	to /edev/k/frq and FlowReservationResponseListLink to /edev/k/frp if and
//	only if this server serves the flow reservation function set.
//
// The second direction is 2018 section 4.4 p.19: "If a function set is not
// implemented, Link elements to resources in that function set SHALL NOT be
// included." The gate is Stores.FlowReservationRequests, the identical
// condition registerNewFunctionSetRoutes uses to mount GET, POST /edev/{id}/frq
// and GET /edev/{id}/frp; one decision enables the routes and the
// advertisement together, the same property logEventLinkedEndDevices states
// as "THE GATE IS THE MOUNT GATE".
//
// ONE DEPARTURE FROM [LogEventLinkedEndDeviceStore] AND [RegisteredEndDeviceStore]:
// both of those are constructed only when their function set is served, and
// are simply absent otherwise. That is fail-closed against the SERVER
// minting a link, but not against a CLIENT minting one: POST /edev and
// PUT /edev/{id} both unmarshal a client body into sep2.EndDevice, so a
// client-supplied link on a resource the server never decorates is never
// cleared. This type is therefore always in the chain; the mount decision
// picks which of its two arms it has, [NewFlowReservationLinkedEndDeviceStore]
// (derive the link) or [NewFlowReservationUnservedEndDeviceStore] (strip the
// field on every path, including a value a client tried to install).

// FlowReservationRequestListHref is the canonical resource URL of the
// FlowReservationRequestList belonging to the EndDevice stored under key.
//
// It is the one place the "/edev/{id}/frq" shape is written on the write
// side, so the link advertised and the route mounted cannot disagree by
// transcription; a change here that is not matched in assembly fails the
// mintable-href assertion rather than reaching a client.
func FlowReservationRequestListHref(key string) string { return "/edev/" + key + "/frq" }

// FlowReservationResponseListHref is [FlowReservationRequestListHref]'s twin
// for the FlowReservationResponseList.
func FlowReservationResponseListHref(key string) string { return "/edev/" + key + "/frp" }

// FlowReservationLinkedEndDeviceStore decorates any [store.EndDeviceStore] so
// every EndDevice it serves carries, or is stripped of, its flow reservation
// list links according to which arm constructed it.
//
// It satisfies the same interface it decorates, so it drops in wherever the
// undecorated store was used and every read path, the POST /edev response
// included, goes through it.
//
// READ SEMANTICS DIFFER FROM A PLAIN STORE, on purpose, exactly as they do
// for its siblings. Both links are DERIVED on the way out from the device's
// own store key (the served arm) or cleared outright (the unserved arm),
// never served as stored, so a value a client tried to install through
// PUT /edev/{id} is not served back to anyone.
type FlowReservationLinkedEndDeviceStore struct {
	devs   store.EndDeviceStore
	served bool

	// reqs and resps are set only on the served arm: the unserved arm's
	// routes are never mounted, so no client could have created a record
	// under this server's own function set for it to cascade.
	reqs  store.ScopedStore[sep2.FlowReservationRequest]
	resps store.ScopedStore[sep2.FlowReservationResponse]
}

// compile-time proof the decorator is substitutable for what it decorates,
// and that it can be probed as part of a Delete chain (deleteProber).
var (
	_ store.EndDeviceStore = (*FlowReservationLinkedEndDeviceStore)(nil)
	_ deleteProber         = (*FlowReservationLinkedEndDeviceStore)(nil)
)

// NewFlowReservationLinkedEndDeviceStore decorates devs so every served
// EndDevice advertises its flow reservation lists.
//
// Construct it only when the flow reservation routes are mounted: section
// 4.4 p.19 forbids advertising a function set the server does not serve.
//
// devs is required. Failing at construction, which happens once when the
// server is assembled, is loud; a nil decorated store would fail at request
// time inside net/http's per-request recover, turning a mis-wired server
// into a silent 500. The guard asks [store.IsAbsent] rather than comparing
// against nil, for the reason argued at [NewRegisteredEndDeviceStore]: devs
// is an interface, and an interface holding a nil concrete pointer is not
// equal to nil.
//
// reqs and resps back the cascade Delete performs: both are required, since
// the served arm is chosen exactly when the flow reservation routes are
// mounted, and those routes are backed by these same two stores.
func NewFlowReservationLinkedEndDeviceStore(
	devs store.EndDeviceStore,
	reqs store.ScopedStore[sep2.FlowReservationRequest],
	resps store.ScopedStore[sep2.FlowReservationResponse],
) *FlowReservationLinkedEndDeviceStore {
	if store.IsAbsent(devs) {
		panic("memory: NewFlowReservationLinkedEndDeviceStore: devs (EndDeviceStore) must not be nil")
	}
	if store.IsAbsent(reqs) || store.IsAbsent(resps) {
		panic("memory: NewFlowReservationLinkedEndDeviceStore: reqs and resps (ScopedStore) must not be nil")
	}
	return &FlowReservationLinkedEndDeviceStore{devs: devs, served: true, reqs: reqs, resps: resps}
}

// NewFlowReservationUnservedEndDeviceStore decorates devs so every served
// EndDevice is stripped of its flow reservation list links, including a
// value a client tried to install through POST /edev or PUT /edev/{id}.
//
// Construct it when the flow reservation routes are NOT mounted. Without
// this arm, an unwired deployment would advertise a client-supplied
// FlowReservationRequestListLink or FlowReservationResponseListLink that
// this server answers 404 on, which is the exposure closed structurally by
// keeping this decorator in the chain always rather than omitting it.
//
// devs is required, for the same reason and the same guard as
// [NewFlowReservationLinkedEndDeviceStore].
func NewFlowReservationUnservedEndDeviceStore(devs store.EndDeviceStore) *FlowReservationLinkedEndDeviceStore {
	if store.IsAbsent(devs) {
		panic("memory: NewFlowReservationUnservedEndDeviceStore: devs (EndDeviceStore) must not be nil")
	}
	return &FlowReservationLinkedEndDeviceStore{devs: devs, served: false}
}

// links returns the pair to stamp on the device stored under id: derived
// from id on the served arm, both nil on the unserved arm. Every write and
// read path goes through this one place so the two links cannot drift apart
// or disagree with the arm that constructed the store.
func (s *FlowReservationLinkedEndDeviceStore) links(id string) (req, resp *sep2.ListLink) {
	if !s.served {
		return nil, nil
	}
	return &sep2.ListLink{Href: FlowReservationRequestListHref(id)}, &sep2.ListLink{Href: FlowReservationResponseListHref(id)}
}

// Create stores the device with its flow reservation links derived from the
// key it is stored under (or cleared, on the unserved arm), discarding
// whatever the caller supplied.
func (s *FlowReservationLinkedEndDeviceStore) Create(ctx context.Context, id string, device sep2.EndDevice) error {
	device.FlowReservationRequestListLink, device.FlowReservationResponseListLink = s.links(id)
	return s.devs.Create(ctx, id, device)
}

// Update replaces the stored device, re-deriving both links from id. A
// client's PUT body cannot install a link of its own choosing.
func (s *FlowReservationLinkedEndDeviceStore) Update(ctx context.Context, id string, device sep2.EndDevice) error {
	device.FlowReservationRequestListLink, device.FlowReservationResponseListLink = s.links(id)
	return s.devs.Update(ctx, id, device)
}

// probeDelete checks whether Delete(ctx, id) looks likely to succeed,
// without mutating anything: its own two collections, and whatever s.devs
// owns beneath it. See [deleteProber] and [probeScopedParent] for what this
// does and does not guarantee.
func (s *FlowReservationLinkedEndDeviceStore) probeDelete(ctx context.Context, id string) error {
	if err := probeInner(ctx, s.devs, id); err != nil {
		return err
	}
	if !s.served {
		return nil
	}
	if err := probeScopedParent(ctx, s.reqs, id); err != nil {
		return fmt.Errorf("checking flow reservation requests for %q: %w", id, err)
	}
	if err := probeScopedParent(ctx, s.resps, id); err != nil {
		return fmt.Errorf("checking flow reservation responses for %q: %w", id, err)
	}
	return nil
}

// ParentTaker is implemented by a scoped store that can remove a parent's
// whole collection and hand back an undo for it. The EndDevice delete uses
// it, so a later step failing puts back what an earlier one removed, under
// the keys the records were stored under.
type ParentTaker interface {
	// TakeParent removes every record under parentID and reports how many
	// went. A failure leaves the collection as it was and returns no undo; a
	// success returns an undo that recreates what was removed.
	TakeParent(ctx context.Context, parentID string) (undo func(context.Context) error, removed uint32, err error)
}

// takeParent removes s's collection under parentID with an undo when s can
// give one: a [ParentTaker], including the in-memory [ScopedStore]. A store
// that is neither is removed through its plain cascade and has no undo, so a
// later failing step cannot put its records back.
func takeParent[T store.Copier[T]](ctx context.Context, s store.ScopedStore[T], parentID string) (func(context.Context) error, uint32, error) {
	if t, ok := s.(ParentTaker); ok {
		return t.TakeParent(ctx, parentID)
	}
	n, err := cascadeScopedParent(ctx, s, parentID)
	return nil, n, err
}

// cascadeScopedParent is deleteScopedParent that also reports the count.
func cascadeScopedParent[T store.Copier[T]](ctx context.Context, s store.ScopedStore[T], parentID string) (uint32, error) {
	cascader, ok := s.(parentCascader)
	if !ok {
		return 0, fmt.Errorf("the store (%T) cannot cascade a parent delete", s)
	}
	return cascader.DeleteParent(ctx, parentID)
}

// TakeParent implements [ParentTaker] for the in-memory store by detaching the
// parent's bucket, which already holds every record under its key. The undo
// recreates each record under that key.
func (s *ScopedStore[T]) TakeParent(ctx context.Context, parentID string) (func(context.Context) error, uint32, error) {
	s.mu.Lock()
	st, ok := s.stores[parentID]
	if ok {
		delete(s.stores, parentID)
	}
	s.mu.Unlock()
	if !ok {
		return nil, 0, nil
	}
	st.mu.RLock()
	keys := slices.Clone(st.keys)
	records := make([]T, len(keys))
	for i, k := range keys {
		records[i] = st.data[k].Copy()
	}
	st.mu.RUnlock()
	return func(ctx context.Context) error {
		var errs []error
		bucket := s.ForParent(parentID)
		for i, k := range keys {
			if err := bucket.Create(ctx, k, records[i]); err != nil {
				log.Printf("memory: could not restore %s/%s after a failed cascade: %v", parentID, k, err)
				errs = append(errs, fmt.Errorf("record %s/%s: %w", parentID, k, err))
			}
		}
		return errors.Join(errs...)
	}, uint32(len(keys)), nil
}

// dependentStore is a scoped store whose parent collection is removed together
// with a second store keyed the same way (a response and its lifecycle
// record).
type dependentStore[T store.Copier[T], D store.Copier[D]] struct {
	store.ScopedStore[T]
	dependents store.ScopedStore[D]
}

// WithDependents returns primary with its parent cascade extended to
// dependents: removing a parent from the result removes it from both, the
// dependents after the primary, and a failure restores whatever was already
// removed. Reads and writes pass through to primary. Either store absent
// returns primary unchanged.
func WithDependents[T store.Copier[T], D store.Copier[D]](primary store.ScopedStore[T], dependents store.ScopedStore[D]) store.ScopedStore[T] {
	if store.IsAbsent(primary) || store.IsAbsent(dependents) {
		return primary
	}
	return &dependentStore[T, D]{ScopedStore: primary, dependents: dependents}
}

// TakeParent implements [ParentTaker] across both stores. The primary goes
// first, so a crash between the two removals leaves a dependent with no
// primary rather than a primary with no dependent; the undo runs the other
// way, dependents first, so a crash mid-restore never serves a restored
// response without the cancel mark that belongs to it (design 2.2).
func (d *dependentStore[T, D]) TakeParent(ctx context.Context, parentID string) (func(context.Context) error, uint32, error) {
	undoPrimary, n, err := takeParent(ctx, d.ScopedStore, parentID)
	if err != nil {
		return nil, 0, err
	}
	undoDependents, _, err := takeParent(ctx, d.dependents, parentID)
	if err != nil {
		if undoPrimary != nil {
			if uerr := undoPrimary(context.WithoutCancel(ctx)); uerr != nil {
				err = errors.Join(err, uerr)
			}
		}
		return nil, 0, err
	}
	return func(ctx context.Context) error {
		var errs []error
		if undoDependents != nil {
			errs = append(errs, undoDependents(ctx))
		}
		if undoPrimary != nil {
			errs = append(errs, undoPrimary(ctx))
		}
		return errors.Join(errs...)
	}, n, nil
}

// DeleteParent removes the parent from both stores and reports how many
// primary records went. It is what makes the probe's capability check pass,
// so it must cascade the dependents too and not forward to the primary alone.
func (d *dependentStore[T, D]) DeleteParent(ctx context.Context, parentID string) (uint32, error) {
	_, n, err := d.TakeParent(ctx, parentID)
	return n, err
}

// Delete cascades the device's flow reservation request, response and
// response lifecycle records before removing the device, so none survives
// under the dead key for a later device created at the same key to inherit
// (GRIDAPPSD/ieee-2030_5-server-go#701). The unserved arm cascades nothing of
// its own: its routes are never mounted, so nothing could have been created
// for it to orphan; it still delegates to s.devs, whose own cascade (if any)
// runs as usual.
//
// probeDelete runs first over the WHOLE chain, this layer and everything
// s.devs owns, and nothing is mutated unless every layer reports it can
// succeed. The probe is a read check, so a persistent store can still fail
// its snapshot write afterwards. A failure at any later step, the device
// removal included, therefore undoes every collection already removed, in
// reverse order. A collection held by a store with no [ParentTaker] (not the
// in-memory or persistent stores) has no undo, and its records stay removed.
func (s *FlowReservationLinkedEndDeviceStore) Delete(ctx context.Context, id string) error {
	if err := s.probeDelete(ctx, id); err != nil {
		return err
	}
	if !s.served {
		return s.devs.Delete(ctx, id)
	}

	var undos []func(context.Context) error
	fail := func(err error) error {
		// A cancelled request context must not stop the restore.
		rctx := context.WithoutCancel(ctx)
		for i := len(undos) - 1; i >= 0; i-- {
			if uerr := undos[i](rctx); uerr != nil {
				err = errors.Join(err, fmt.Errorf("restoring flow reservation records for %q: %w", id, uerr))
			}
		}
		return err
	}

	undo, _, err := takeParent(ctx, s.reqs, id)
	if err != nil {
		return fmt.Errorf("cascading flow reservation requests for %q: %w", id, err)
	}
	if undo != nil {
		undos = append(undos, undo)
	}
	// A response store built with WithDependents removes the response
	// lifecycle records here too, after the responses.
	undo, _, err = takeParent(ctx, s.resps, id)
	if err != nil {
		return fail(fmt.Errorf("cascading flow reservation responses for %q: %w", id, err))
	}
	if undo != nil {
		undos = append(undos, undo)
	}
	if err := s.devs.Delete(ctx, id); err != nil {
		return fail(err)
	}
	return nil
}

// Get returns the device stored under id with its links derived from id.
func (s *FlowReservationLinkedEndDeviceStore) Get(ctx context.Context, id string) (sep2.EndDevice, error) {
	device, err := s.devs.Get(ctx, id)
	if err != nil {
		return sep2.EndDevice{}, err
	}
	device.FlowReservationRequestListLink, device.FlowReservationResponseListLink = s.links(id)
	return device, nil
}

// GetBySFDI returns the device with the given SFDI, link-derived.
func (s *FlowReservationLinkedEndDeviceStore) GetBySFDI(ctx context.Context, sfdi string) (sep2.EndDevice, error) {
	device, err := s.devs.GetBySFDI(ctx, sfdi)
	if err != nil {
		return sep2.EndDevice{}, err
	}
	return s.deriveByHref(device), nil
}

// GetByLFDI returns the device with the given LFDI, link-derived.
func (s *FlowReservationLinkedEndDeviceStore) GetByLFDI(ctx context.Context, lfdi string) (sep2.EndDevice, error) {
	device, err := s.devs.GetByLFDI(ctx, lfdi)
	if err != nil {
		return sep2.EndDevice{}, err
	}
	return s.deriveByHref(device), nil
}

// List returns a page of devices, each link-derived.
func (s *FlowReservationLinkedEndDeviceStore) List(ctx context.Context, opts store.ListOptions) (store.ListResult[sep2.EndDevice], error) {
	result, err := s.devs.List(ctx, opts)
	if err != nil {
		return store.ListResult[sep2.EndDevice]{}, err
	}
	for i := range result.Items {
		result.Items[i] = s.deriveByHref(result.Items[i])
	}
	return result, nil
}

// Count delegates: the links change what a device advertises, not how many
// devices exist.
func (s *FlowReservationLinkedEndDeviceStore) Count(ctx context.Context) (uint32, error) {
	return s.devs.Count(ctx)
}

// deriveByHref applies the derivation on the read paths that hand back a
// device without its store key, recovering the key from the device's own
// Href with the same helper [RegisteredEndDeviceStore] uses.
//
// A key that cannot be recovered STRIPS both links rather than guessing one,
// the same fail-closed direction [LogEventLinkedEndDeviceStore.deriveByHref]
// argues for: an unadvertised pair is recoverable by a client re-reading the
// device under its canonical href, while links derived from a malformed href
// would point at lists this server may not serve under that address. A strip
// that discards a link is logged, since it means an EndDevice was stored off
// the addressing scheme the rest of the package depends on.
func (s *FlowReservationLinkedEndDeviceStore) deriveByHref(device sep2.EndDevice) sep2.EndDevice {
	key, ok := keyFromEndDeviceHref(device.Href)
	if !ok {
		if device.FlowReservationRequestListLink != nil || device.FlowReservationResponseListLink != nil {
			log.Printf("memory: EndDevice href %q does not follow the /edev/{key} addressing invariant; serving it without FlowReservationRequestListLink or FlowReservationResponseListLink", device.Href)
		}
		device.FlowReservationRequestListLink = nil
		device.FlowReservationResponseListLink = nil
		return device
	}
	device.FlowReservationRequestListLink, device.FlowReservationResponseListLink = s.links(key)
	return device
}
