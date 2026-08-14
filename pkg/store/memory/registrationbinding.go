package memory

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
)

// The EndDevice and its Registration are one act.
//
// Before this file, RegistrationLink was stamped on every EndDevice by the
// POST /edev handler while nothing ever wrote a Registration record, so
// GET /edev/{id}/rg returned a handler 404 on a route that was mounted and
// a link that was advertised. IEEE 2030.5 registration is how a client
// obtains its pIN, so an EndDevice advertising a Registration it does not
// have breaks the mechanism a client uses to register at all.
//
// Note this is a DIFFERENT defect shape from a mint-a-Location-at-an-
// unrouted-path bug. This one is route-mounted-resource-absent, and
// assembly.AssertMintableHrefs does NOT
// catch it, because the href does resolve to a registered pattern. That
// bounds what the assertion buys: it closes the routing half of the class
// and leaves the population half open. RegisteredEndDeviceStore closes the
// population half, and nothing else in the codebase does.
//
// The rule this type enforces, in both directions:
//
//	an EndDevice served under key k carries a RegistrationLink
//	if and only if the Registration store holds a record under k.
//
// Both directions matter. Stamping without a record is the original defect.
// A record without a link is a Registration a client can never discover,
// and 2018 section 4.4 p.19 is explicit that "If a function set is not
// implemented, Link elements to resources in that function set SHALL NOT be
// included". Enforcing the rule in one type, on one key, is what makes the
// advertise-versus-serve pair unable to drift: there are no two call sites
// to keep in step.

// DefaultRegistrationPollRate is the pollRate a served Registration carries
// when the policy does not name one. It is sep.xsd's own declared default
// for the attribute (schema line 190, use="optional" default="900"), so
// stamping it explicitly changes no client's interpretation; it only makes
// the value visible on the wire instead of implied by omission.
const DefaultRegistrationPollRate uint32 = 900

// RegistrationPINFunc supplies the registration pIN for the device whose
// certificate-derived LFDI is lfdi. ok=false means no pIN is provisioned for
// that device, which is not an error: it is the fail-closed answer, and the
// binding responds by creating no Registration and advertising no link.
//
// Core deliberately does not generate pIN values and takes no position on
// what any pIN is. A pIN is a shared secret precisely BECAUSE the SFDI and
// LFDI are derived from public certificate material that any TLS peer can
// recompute (2018 section 6.3.5), so a pIN derived from device identity
// would validate nothing. Generation and operator provisioning, and the
// range and check-digit validator, are both future work. Neither has landed
// at the time of writing, so this binding neither generates nor validates:
// it transports whatever the embedder supplies.
//
// Implementations MUST NOT log the value they return, and callers MUST NOT
// place it in an error message; see RegistrationPolicy.
type RegistrationPINFunc func(lfdi string) (pin uint32, ok bool)

// RegistrationPolicy is the embedder-supplied half of the binding: what pIN
// a device gets, and at what rate a client should poll the resource.
//
// The zero value provisions nothing. That is deliberate and fail-closed: a
// server that cannot say what a device's pIN is has no Registration to
// serve, and therefore must not advertise one.
//
// SECRET HANDLING. The pIN is a shared secret in the registration exchange.
// No code in this package logs a pIN, embeds one in an error message, or
// writes one anywhere but the Registration record itself. Preserve that when
// touching this file.
type RegistrationPolicy struct {
	// PIN resolves the pIN for a device by its LFDI. A nil PIN provisions
	// no Registration for any device.
	//
	// It is keyed on the LFDI, never on the URL index, because the index is
	// a server-chosen addressing artifact that means nothing in an
	// operator's provisioning file: handing it to the resolver would look
	// up a device the operator never configured.
	PIN RegistrationPINFunc

	// PollRate is the pollRate attribute stamped on every Registration this
	// binding creates. Zero means DefaultRegistrationPollRate.
	PollRate uint32
}

// pollRate returns the rate to stamp, resolving zero to the schema default.
func (p RegistrationPolicy) pollRate() uint32 {
	if p.PollRate == 0 {
		return DefaultRegistrationPollRate
	}
	return p.PollRate
}

// RegistrationHref is the canonical resource URL of the Registration
// belonging to the EndDevice stored under key. It is the one place the
// "/edev/{id}/rg" shape is written on the write side, so the link an
// EndDevice advertises and the URL the registration handler answers on
// cannot disagree by transcription.
func RegistrationHref(key string) string { return "/edev/" + key + "/rg" }

// endDeviceHrefPrefix is the addressing invariant the read side leans on:
// an EndDevice stored under key k is served at "/edev/k". The POST handler
// and every embedder's seeding path both establish it, and
// assembly's URL-index tests assert it on the served bytes.
const endDeviceHrefPrefix = "/edev/"

// keyFromEndDeviceHref recovers the store key from a served EndDevice.
//
// It exists because ResourceStore.List returns resources, not the keys they
// are stored under, and the read side needs the key to ask whether a
// Registration exists. Recovering it from Href rather than widening the
// store contract keeps the change local; the cost is that a device whose
// Href does not follow the addressing invariant yields no key, and the read
// side then strips the link rather than guessing. That is the fail-closed
// direction: an unadvertised Registration is recoverable by a client
// re-reading the device, an advertised-but-absent one is the defect.
func keyFromEndDeviceHref(href string) (string, bool) {
	rest, ok := strings.CutPrefix(href, endDeviceHrefPrefix)
	if !ok || rest == "" || strings.Contains(rest, "/") {
		return "", false
	}
	return rest, true
}

// RegisteredEndDeviceStore couples EndDevice records to Registration records
// so the two are written, removed and served as one pair.
//
// It decorates any store.EndDeviceStore and satisfies the same interface, so
// it can be dropped in wherever the undecorated store was used: the POST
// /edev handler and an embedder's boot-time seeding both reach the same
// Create, which is what makes the coupling structural rather than two call
// sites that happen to agree today.
//
// READ SEMANTICS DIFFER FROM A PLAIN STORE, on purpose. Get, List,
// GetBySFDI and GetByLFDI return the stored EndDevice with its
// RegistrationLink derived from whether a Registration record exists, rather
// than as stored. A plain store's contract is "hand back what was written";
// this type's contract is "hand back a device whose advertised links the
// server can actually serve". A stored link left over from a deleted
// Registration, or one a client tried to forge through PUT /edev/{id}, is
// therefore not served.
type RegisteredEndDeviceStore struct {
	devs   store.EndDeviceStore
	regs   store.ResourceStore[sep2.Registration]
	policy RegistrationPolicy
	now    func() int64
}

// compile-time proof the decorator is substitutable for what it decorates.
var _ store.EndDeviceStore = (*RegisteredEndDeviceStore)(nil)

// NewRegisteredEndDeviceStore binds devs to regs under policy.
//
// devs and regs are both required: a binding missing either half could only
// enforce the invariant in one direction, which is the state this type
// exists to eliminate. Failing at construction, which happens once when the
// server is assembled, is loud; failing at request time inside net/http's
// per-request recover would turn a mis-wired server into a silent 500.
//
// Both guards ask [store.IsAbsent] rather than comparing against nil. These
// parameters are interfaces, and an interface holding a nil concrete pointer
// is not equal to nil, so a plain comparison accepted the one shape a
// mis-wired consumer actually produces: the zero value of the concrete store
// it would otherwise have constructed. The diagnostic this function exists
// to give was withheld from precisely the caller who needed it, and the
// mis-wiring surfaced as a nil dereference on the first request instead.
// It is the same fault closed at the mount gates; read [store.IsAbsent]
// before writing a new guard here as a nil comparison.
func NewRegisteredEndDeviceStore(devs store.EndDeviceStore, regs store.ResourceStore[sep2.Registration], policy RegistrationPolicy) *RegisteredEndDeviceStore {
	if store.IsAbsent(devs) {
		panic("memory: NewRegisteredEndDeviceStore: devs (EndDeviceStore) must not be nil")
	}
	if store.IsAbsent(regs) {
		panic("memory: NewRegisteredEndDeviceStore: regs (RegistrationStore) must not be nil")
	}
	return &RegisteredEndDeviceStore{devs: devs, regs: regs, policy: policy}
}

// timestamp returns the dateTimeRegistered to stamp. The seam exists so a
// test can assert an exact field value rather than a range.
func (s *RegisteredEndDeviceStore) timestamp() int64 {
	if s.now != nil {
		return s.now()
	}
	return time.Now().Unix()
}

// Create stores the EndDevice and its Registration as one act.
//
// When the policy resolves a pIN for the device, the Registration is written
// and the EndDevice is stamped with the matching RegistrationLink. When it
// does not, neither happens: the device is stored with no RegistrationLink,
// per 2018 section 4.4 p.19. A client-supplied RegistrationLink on the
// inbound device is discarded either way, because whether the server holds a
// Registration is not the client's to assert.
//
// If the Registration write fails after the EndDevice landed, the EndDevice
// is removed again. Leaving it would publish exactly the advertised-but-
// absent pair this type exists to prevent, and a failed registration that
// the client can retry is the better of the two outcomes.
func (s *RegisteredEndDeviceStore) Create(ctx context.Context, id string, device sep2.EndDevice) error {
	reg, provisioned := s.registrationFor(id, device)

	device.RegistrationLink = nil
	if provisioned {
		device.RegistrationLink = &sep2.Link{Href: RegistrationHref(id)}
	}

	if err := s.devs.Create(ctx, id, device); err != nil {
		return err
	}
	if !provisioned {
		return nil
	}

	if err := s.putRegistration(ctx, id, reg); err != nil {
		if delErr := s.devs.Delete(ctx, id); delErr != nil {
			// Both halves failed. Say so: the store is now in the state
			// this type promises never to publish, and a caller that only
			// saw the first error would not know to re-check.
			log.Printf("memory: EndDevice %q left stored after its Registration failed to write; rollback also failed: %v", id, delErr)
		}
		return err
	}
	return nil
}

// Update replaces the stored EndDevice, re-deriving RegistrationLink from
// whether a Registration record exists.
//
// It does not create a Registration. Update is not creation: the store
// contract says Update never creates an EndDevice either, and provisioning a
// pIN on a client-driven PUT would let a client bring a registration into
// being by editing its own record. A device that has no Registration comes
// back from this call with no RegistrationLink, which is the honest answer.
func (s *RegisteredEndDeviceStore) Update(ctx context.Context, id string, device sep2.EndDevice) error {
	link, err := s.linkFor(ctx, id)
	if err != nil {
		return err
	}
	device.RegistrationLink = link
	return s.devs.Update(ctx, id, device)
}

// Delete removes the EndDevice and its Registration together.
//
// The Registration is removed after the device, and an absent Registration
// is not an error: a device provisioned without a pIN never had one. Any
// other failure is reported, because a Registration surviving its EndDevice
// would be served to whoever the key is next allocated to.
func (s *RegisteredEndDeviceStore) Delete(ctx context.Context, id string) error {
	if err := s.devs.Delete(ctx, id); err != nil {
		return err
	}
	if err := s.regs.Delete(ctx, id); err != nil && !errors.Is(err, store.ErrNotFound) {
		return err
	}
	return nil
}

// Get returns the EndDevice stored under id with its RegistrationLink
// derived from the Registration store.
func (s *RegisteredEndDeviceStore) Get(ctx context.Context, id string) (sep2.EndDevice, error) {
	device, err := s.devs.Get(ctx, id)
	if err != nil {
		return sep2.EndDevice{}, err
	}
	link, err := s.linkFor(ctx, id)
	if err != nil {
		return sep2.EndDevice{}, err
	}
	device.RegistrationLink = link
	return device, nil
}

// GetBySFDI returns the device with the given SFDI, link-derived.
func (s *RegisteredEndDeviceStore) GetBySFDI(ctx context.Context, sfdi string) (sep2.EndDevice, error) {
	device, err := s.devs.GetBySFDI(ctx, sfdi)
	if err != nil {
		return sep2.EndDevice{}, err
	}
	return s.deriveByHref(ctx, device)
}

// GetByLFDI returns the device with the given LFDI, link-derived.
func (s *RegisteredEndDeviceStore) GetByLFDI(ctx context.Context, lfdi string) (sep2.EndDevice, error) {
	device, err := s.devs.GetByLFDI(ctx, lfdi)
	if err != nil {
		return sep2.EndDevice{}, err
	}
	return s.deriveByHref(ctx, device)
}

// List returns a page of EndDevices, each link-derived.
//
// A Registration lookup that fails while deriving one item's link is
// reported, not swallowed. A list that cannot be truthfully
// constructed must not be served as though it were: the caller (ultimately
// listhandler.ListHandler, which already routes a non-nil error here
// through srverr.Internal) answers 5xx rather than a 200 whose entries
// silently disagree with the Registration store's real state.
func (s *RegisteredEndDeviceStore) List(ctx context.Context, opts store.ListOptions) (store.ListResult[sep2.EndDevice], error) {
	result, err := s.devs.List(ctx, opts)
	if err != nil {
		return store.ListResult[sep2.EndDevice]{}, err
	}
	for i := range result.Items {
		derived, err := s.deriveByHref(ctx, result.Items[i])
		if err != nil {
			return store.ListResult[sep2.EndDevice]{}, err
		}
		result.Items[i] = derived
	}
	return result, nil
}

// Count delegates: coupling changes which links are served, not how many
// devices exist.
func (s *RegisteredEndDeviceStore) Count(ctx context.Context) (uint32, error) {
	return s.devs.Count(ctx)
}

// registrationFor builds the Registration to store alongside device, or
// reports ok=false when the policy provisions no pIN for it.
//
// The pIN is resolved by LFDI, never by id: id is the URL index, an
// addressing artifact an operator's provisioning file has never seen.
func (s *RegisteredEndDeviceStore) registrationFor(id string, device sep2.EndDevice) (sep2.Registration, bool) {
	if s.policy.PIN == nil || device.LFDI == "" {
		return sep2.Registration{}, false
	}
	pin, ok := s.policy.PIN(device.LFDI)
	if !ok {
		return sep2.Registration{}, false
	}
	reg := sep2.Registration{
		DateTimeRegistered: s.timestamp(),
		PIN:                pin,
		PollRate:           s.policy.pollRate(),
	}
	reg.Href = RegistrationHref(id)
	return reg, true
}

// putRegistration writes reg under id, replacing any record already there.
//
// Replace rather than refuse: the key belongs to the EndDevice being
// created, so a record already under it belongs to a device that no longer
// exists. Serving that stale record to the new occupant of the key would
// hand one device another's pIN.
func (s *RegisteredEndDeviceStore) putRegistration(ctx context.Context, id string, reg sep2.Registration) error {
	err := s.regs.Create(ctx, id, reg)
	if errors.Is(err, store.ErrAlreadyExists) {
		return s.regs.Update(ctx, id, reg)
	}
	return err
}

// linkFor returns the RegistrationLink a device stored under id may
// advertise: non-nil exactly when a Registration record exists under id.
//
// A lookup that cannot complete is neither "present" nor "absent", so it is
// reported rather than guessed. Guessing present would advertise a resource
// the server may not be able to serve; guessing absent would silently drop a
// link a conformant client needs.
func (s *RegisteredEndDeviceStore) linkFor(ctx context.Context, id string) (*sep2.Link, error) {
	switch _, err := s.regs.Get(ctx, id); {
	case err == nil:
		return &sep2.Link{Href: RegistrationHref(id)}, nil
	case errors.Is(err, store.ErrNotFound):
		return nil, nil
	default:
		return nil, err
	}
}

// deriveByHref applies linkFor on the read paths that hand back a device
// without its store key, recovering the key from the device's own Href.
//
// A key that cannot be recovered strips the link and returns no error: the
// device's Href does not follow the "/edev/{key}" addressing invariant, so
// there is no key to ask the Registration store about. That silence is not
// a verified fact: Registrations are keyed by the store key, not the Href,
// so a Registration could still exist under the device's real key; this
// method has no way to ask. That case is logged, since it means an
// EndDevice was stored off the addressing scheme the rest of the package
// depends on.
//
// A key that IS recovered but whose Registration lookup fails is a
// different case and is NOT silenced: whether that device
// has a Registration is genuinely unknown, and reporting it as absent would
// tell a client the same thing 2018 section 4.4 p.19 reserves for a
// function set that truly is not implemented. The error is returned so
// GetBySFDI, GetByLFDI and List answer the way Get already does, and no pIN
// is logged or included in it either way.
func (s *RegisteredEndDeviceStore) deriveByHref(ctx context.Context, device sep2.EndDevice) (sep2.EndDevice, error) {
	key, ok := keyFromEndDeviceHref(device.Href)
	if !ok {
		if device.RegistrationLink != nil {
			log.Printf("memory: EndDevice href %q does not follow the /edev/{key} addressing invariant; serving it without a RegistrationLink", device.Href)
		}
		device.RegistrationLink = nil
		return device, nil
	}
	link, err := s.linkFor(ctx, key)
	if err != nil {
		return sep2.EndDevice{}, fmt.Errorf("determine whether a Registration exists for EndDevice %q: %w", device.Href, err)
	}
	device.RegistrationLink = link
	return device, nil
}
