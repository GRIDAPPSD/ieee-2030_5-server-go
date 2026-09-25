package memory

import (
	"context"
	"log"

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
}

// compile-time proof the decorator is substitutable for what it decorates.
var _ store.EndDeviceStore = (*FlowReservationLinkedEndDeviceStore)(nil)

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
func NewFlowReservationLinkedEndDeviceStore(devs store.EndDeviceStore) *FlowReservationLinkedEndDeviceStore {
	if store.IsAbsent(devs) {
		panic("memory: NewFlowReservationLinkedEndDeviceStore: devs (EndDeviceStore) must not be nil")
	}
	return &FlowReservationLinkedEndDeviceStore{devs: devs, served: true}
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

// Delete removes the device. The flow reservation records are held in
// separate scoped stores keyed by the same device id; neither this call nor
// EndDevice DELETE removes them, so they survive under the dead key (issue
// 701).
func (s *FlowReservationLinkedEndDeviceStore) Delete(ctx context.Context, id string) error {
	return s.devs.Delete(ctx, id)
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
