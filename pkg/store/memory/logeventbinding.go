package memory

import (
	"context"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
)

// Mounting the LogEvent function set and advertising it are one act.
//
// Before this file NO production path assigned EndDevice.LogEventListLink. The
// only assignments anywhere in the tree were in a wire-order test fixture, so
// the LogEvent list was served at an address a client had no way to learn, and
// CSIP V1.2 BASIC-027 step 2, "using the EndDevice instance, find the
// LogEventListLink", could not pass against this server at any address.
//
// The routing half of this same fix moved the list from /edev/{id}/log to the
// WADL address /edev/{id}/lel (sep_wadl.xml:1358). Moving the route without
// closing the advertisement half would have left the function set exactly as
// dark as it was, one path over, which is why both halves land together.
//
// The rule this type enforces, in both directions:
//
//	an EndDevice served under key k carries a LogEventListLink to
//	/edev/k/lel if and only if this server serves the LogEvent
//	function set.
//
// The second direction is 2018 section 4.4 p.19: "If a function set is not
// implemented, Link elements to resources in that function set SHALL NOT be
// included." It is enforced structurally rather than by a conditional at each
// serving handler: whoever assembles the router decorates the EndDevice store
// exactly when it mounts the routes, and a server that does not mount them
// never constructs this type, so there is no call site that could advertise
// what another call site does not serve.
//
// Note what this type is NOT. Unlike its sibling [RegisteredEndDeviceStore],
// which asks whether a Registration RECORD exists, this decorator asks nothing
// of any store: a LogEventList is a list, and a device with no events has an
// empty one, which is a legitimate resource rather than an absent one. The
// condition is whether the function set is served at all, and that is a
// property of the assembled router, decided once, not per device.

// LogEventListHref is the canonical resource URL of the LogEventList belonging
// to the EndDevice stored under key, per sep_wadl.xml:1358 and 2018 A.3.5.1.
//
// It is the one place the "/edev/{id}/lel" shape is written on the write side,
// so the link an EndDevice advertises and the URL the router mounts cannot
// disagree by transcription. The router derives its pattern from the same
// shape; a change here that is not matched there fails the mintable-href
// assertion in pkg/sep2srv/assembly rather than reaching a client.
func LogEventListHref(key string) string { return "/edev/" + key + "/lel" }

// LogEventLinkedEndDeviceStore decorates any [store.EndDeviceStore] so every
// EndDevice it serves advertises its LogEventList.
//
// It satisfies the same interface it decorates, so it drops in wherever the
// undecorated store was used and every read path, the POST /edev response
// included, goes through it.
//
// READ SEMANTICS DIFFER FROM A PLAIN STORE, on purpose, exactly as they do for
// [RegisteredEndDeviceStore]. The link is DERIVED on the way out from the
// device's own store key rather than served as stored, so a value a client
// tried to install through PUT /edev/{id} is not served back to anyone. The
// link states where THIS server serves the list; it is not a field a client
// gets to assert.
type LogEventLinkedEndDeviceStore struct {
	devs store.EndDeviceStore
}

// compile-time proof the decorator is substitutable for what it decorates.
var _ store.EndDeviceStore = (*LogEventLinkedEndDeviceStore)(nil)

// NewLogEventLinkedEndDeviceStore decorates devs.
//
// Construct it only when the LogEvent routes are mounted. Constructing it
// otherwise advertises a function set the server does not serve, which section
// 4.4 p.19 forbids and which is the defect this type exists to prevent rather
// than a milder version of it.
//
// devs is required. Failing at construction, which happens once when the server
// is assembled, is loud; a nil decorated store would fail at request time inside
// net/http's per-request recover, turning a mis-wired server into a silent 500.
//
// The guard asks [store.IsAbsent] rather than comparing against nil, for the
// reason argued at [NewRegisteredEndDeviceStore]: devs is an interface, and
// an interface holding a nil concrete pointer is not equal
// to nil, so a plain comparison let the one mis-wiring a consumer actually
// produces past the check that exists to catch it.
func NewLogEventLinkedEndDeviceStore(devs store.EndDeviceStore) *LogEventLinkedEndDeviceStore {
	if store.IsAbsent(devs) {
		panic("memory: NewLogEventLinkedEndDeviceStore: devs (EndDeviceStore) must not be nil")
	}
	return &LogEventLinkedEndDeviceStore{devs: devs}
}

// Create stores the device with its LogEventListLink derived from the key it is
// stored under, discarding whatever the caller supplied.
func (s *LogEventLinkedEndDeviceStore) Create(ctx context.Context, id string, device sep2.EndDevice) error {
	device.LogEventListLink = &sep2.ListLink{Href: LogEventListHref(id)}
	return s.devs.Create(ctx, id, device)
}

// Update replaces the stored device, re-deriving the link from id. A client's
// PUT body cannot install a link of its own choosing.
func (s *LogEventLinkedEndDeviceStore) Update(ctx context.Context, id string, device sep2.EndDevice) error {
	device.LogEventListLink = &sep2.ListLink{Href: LogEventListHref(id)}
	return s.devs.Update(ctx, id, device)
}

// Delete removes the device. There is no LogEvent record to remove alongside
// it: the events are held in a separate scoped store keyed by the same device
// id, and removing them is the EndDevice DELETE handler's concern rather than
// this decorator's.
func (s *LogEventLinkedEndDeviceStore) Delete(ctx context.Context, id string) error {
	return s.devs.Delete(ctx, id)
}

// Get returns the device stored under id with its link derived from id.
func (s *LogEventLinkedEndDeviceStore) Get(ctx context.Context, id string) (sep2.EndDevice, error) {
	device, err := s.devs.Get(ctx, id)
	if err != nil {
		return sep2.EndDevice{}, err
	}
	device.LogEventListLink = &sep2.ListLink{Href: LogEventListHref(id)}
	return device, nil
}

// GetBySFDI returns the device with the given SFDI, link-derived.
func (s *LogEventLinkedEndDeviceStore) GetBySFDI(ctx context.Context, sfdi string) (sep2.EndDevice, error) {
	device, err := s.devs.GetBySFDI(ctx, sfdi)
	if err != nil {
		return sep2.EndDevice{}, err
	}
	return s.deriveByHref(device), nil
}

// GetByLFDI returns the device with the given LFDI, link-derived.
func (s *LogEventLinkedEndDeviceStore) GetByLFDI(ctx context.Context, lfdi string) (sep2.EndDevice, error) {
	device, err := s.devs.GetByLFDI(ctx, lfdi)
	if err != nil {
		return sep2.EndDevice{}, err
	}
	return s.deriveByHref(device), nil
}

// List returns a page of devices, each link-derived.
func (s *LogEventLinkedEndDeviceStore) List(ctx context.Context, opts store.ListOptions) (store.ListResult[sep2.EndDevice], error) {
	result, err := s.devs.List(ctx, opts)
	if err != nil {
		return store.ListResult[sep2.EndDevice]{}, err
	}
	for i := range result.Items {
		result.Items[i] = s.deriveByHref(result.Items[i])
	}
	return result, nil
}

// Count delegates: the link changes what a device advertises, not how many
// devices exist.
func (s *LogEventLinkedEndDeviceStore) Count(ctx context.Context) (uint32, error) {
	return s.devs.Count(ctx)
}

// deriveByHref applies the derivation on the read paths that hand back a device
// without its store key, recovering the key from the device's own Href with the
// same helper [RegisteredEndDeviceStore] uses.
//
// A key that cannot be recovered STRIPS the link rather than guessing one. That
// is the fail-closed direction: an unadvertised list is recoverable by a client
// re-reading the device under its canonical href, while a link derived from a
// malformed href would point at a list this server does not serve, which is the
// defect the whole card is closing.
func (s *LogEventLinkedEndDeviceStore) deriveByHref(device sep2.EndDevice) sep2.EndDevice {
	key, ok := keyFromEndDeviceHref(device.Href)
	if !ok {
		device.LogEventListLink = nil
		return device
	}
	device.LogEventListLink = &sep2.ListLink{Href: LogEventListHref(key)}
	return device
}
