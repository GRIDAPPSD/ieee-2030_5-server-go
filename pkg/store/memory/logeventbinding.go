package memory

import (
	"context"
	"fmt"

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
	linkedEndDevices
	events store.ScopedStore[sep2.LogEvent]
}

// logEventLinks is the LogEventListLink half of the shared decorator.
type logEventLinks struct{}

func (logEventLinks) derive(_ context.Context, d *sep2.EndDevice, key string) {
	d.LogEventListLink = &sep2.ListLink{Href: LogEventListHref(key)}
}

func (l logEventLinks) forWrite(ctx context.Context, d *sep2.EndDevice, key string) {
	l.derive(ctx, d, key)
}

func (logEventLinks) strip(d *sep2.EndDevice) bool {
	had := d.LogEventListLink != nil
	d.LogEventListLink = nil
	return had
}

func (logEventLinks) linkNames() string { return "LogEventListLink" }

// compile-time proof the decorator is substitutable for what it decorates,
// and that it can be probed as part of a Delete chain (deleteProber).
var (
	_ store.EndDeviceStore = (*LogEventLinkedEndDeviceStore)(nil)
	_ deleteProber         = (*LogEventLinkedEndDeviceStore)(nil)
)

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
//
// events backs the cascade Delete performs; it is required, since this
// decorator is constructed exactly when the LogEvent routes are mounted, and
// those routes are backed by the same store.
func NewLogEventLinkedEndDeviceStore(devs store.EndDeviceStore, events store.ScopedStore[sep2.LogEvent]) *LogEventLinkedEndDeviceStore {
	if store.IsAbsent(devs) {
		panic("memory: NewLogEventLinkedEndDeviceStore: devs (EndDeviceStore) must not be nil")
	}
	if store.IsAbsent(events) {
		panic("memory: NewLogEventLinkedEndDeviceStore: events (ScopedStore) must not be nil")
	}
	return &LogEventLinkedEndDeviceStore{linkedEndDevices: linkedEndDevices{devs: devs, fields: logEventLinks{}}, events: events}
}

// probeDelete checks whether Delete(ctx, id) looks likely to succeed,
// without mutating anything: its own LogEvent collection, and whatever
// s.devs owns beneath it. See [deleteProber] and [probeScopedParent] for
// what this does and does not guarantee.
func (s *LogEventLinkedEndDeviceStore) probeDelete(ctx context.Context, id string) error {
	if err := probeInner(ctx, s.devs, id); err != nil {
		return err
	}
	if err := probeScopedParent(ctx, s.events, id); err != nil {
		return fmt.Errorf("checking log events for %q: %w", id, err)
	}
	return nil
}

// Delete cascades the device's LogEvent records before removing the device,
// so they do not survive under the dead key for a later device created at
// the same key to inherit (GRIDAPPSD/ieee-2030_5-server-go#701).
//
// probeDelete runs first over the WHOLE chain, this layer and everything
// s.devs owns, and nothing is mutated unless every layer reports it can
// succeed. Without that, an outer decorator's cascade (flow reservation
// requests and responses) could complete before this layer's LogEvent
// cascade failed, leaving the device present with those records already
// gone.
func (s *LogEventLinkedEndDeviceStore) Delete(ctx context.Context, id string) error {
	if err := s.probeDelete(ctx, id); err != nil {
		return err
	}
	if err := deleteScopedParent(ctx, s.events, id); err != nil {
		return fmt.Errorf("cascading log events for %q: %w", id, err)
	}
	return s.devs.Delete(ctx, id)
}
