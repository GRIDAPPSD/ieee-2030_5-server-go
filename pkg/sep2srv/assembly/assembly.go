// Package assembly exports BuildProtocolRouter, Stores, RouterConfig, and
// AuthPolicy: the surface a consumer server (or a test) needs to assemble
// a fully-wired IEEE 2030.5 protocol mux from core handlers.
//
// Design (2026-06-23):
//
//   - Stores is a verbatim lift of the reference server's Stores struct; every
//     field resolves to pkg/store or pkg/store/memory.
//   - RouterConfig replaces *config.Config so core never imports internal/config.
//   - AuthPolicy injects the auth seam (middleware wrap, identity accessor, SFDI
//     prefix guard) so core never imports internal/auth.
//   - BuildProtocolRouter wires all core handlers plus the four ported families
//     (enddevice, der, fsa, registration) and returns the composed http.Handler
//     and the sorted pattern list for boot-time logging.
//
// # This package is the server's router and is destined to move
//
// A router is server state. A 2030.5 client has no router: it issues requests
// against hrefs a server handed it. Under the four-layer target architecture
// (core is the shared library for client and server, server-go is the server,
// the bridge grafts onto the server), this package belongs in server-go, and it
// lives in core today only because the server currently does. Nothing here is
// part of the shared client-and-server surface, and callers should not treat it
// as such.
//
// That applies to everything routing-shaped in this package, including the
// mintable-href assertion in hrefs.go and the guards that back it in
// hrefsource_test.go and pathvalue_test.go: they check the router against
// itself, so they move with the router rather than calcifying here. Keep them
// coupled to BuildProtocolRouter and to the handler packages, and to nothing in
// the client half of core, so that the relocation stays a move rather than a
// breaking API change.
//
// The relocation is sequenced with the layering split, not with any one card.
// See the bridge/core boundary analysis for the reasoning, and pkg/store's
// package documentation for the same note about the store contract.
package assembly

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"sort"
	"strings"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2/encoding"
	coreconfiguration "github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/handlers/configuration"
	coredcap "github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/handlers/dcap"
	coreder "github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/handlers/der"
	coredevinfo "github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/handlers/device_info"
	coreedev "github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/handlers/enddevice"
	coreflowrsv "github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/handlers/flow_reservation"
	corefsa "github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/handlers/fsa"
	corelisthandler "github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/handlers/listhandler"
	corelogevent "github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/handlers/logevent"
	coremessaging "github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/handlers/messaging"
	coremetering "github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/handlers/metering"
	corepowerstatus "github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/handlers/power_status"
	corereg "github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/handlers/registration"
	coreresponse "github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/handlers/response"
	coresdev "github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/handlers/sdev"
	coresep2time "github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/handlers/sep2time"
	coresingleton "github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/handlers/singleton"
	coresub "github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/handlers/subscription"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/paging"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/srverr"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

// Stores holds all resource stores for the protocol router.
//
// # The handles are interfaces
//
// Every collection here is declared as a pkg/store interface rather than as a
// pkg/store/memory type, so a consumer can attach state that is not the
// in-memory store. Three fields are not, and cannot be until pkg/store models
// what they do: EndDeviceIndexes is an index allocator rather than a
// collection, Subscriptions is queried by resource href and by device with
// records that carry a store key alongside the resource, and AdminFSAs is an
// operator-facing management plane whose List takes no paging at all. Each is
// documented at its own field.
//
// # A nil handle means the function set is not wired, and nil is subtler now
//
// A nil handle here is how a consumer says it does not serve a function set:
// the routes are not mounted and no link to them is advertised, per
// IEEE 2030.5-2018 section 4.4 p.19. That remains true, but "nil" cannot be
// tested by comparison any more. An interface holding a nil pointer is not
// equal to nil, so a consumer that assigns a nil *memory.ScopedStore into one
// of these fields, which is what leaving a field out of a store constructor and
// copying the struct produces, would previously have unmounted the function set
// and would now mount it over a handle that panics on first request.
//
// Every gate below therefore asks [store.IsAbsent] rather than comparing
// against nil. Read that function's documentation before adding a field here:
// a new gate written as a nil comparison reintroduces exactly that fault, and
// it fails at request time on a consumer's deployment rather than in this
// repository's tests.
//
// # THE GATE IS PER FAMILY, NOT PER FIELD
//
// The paragraph above says "every gate", and there are thirteen of them. Each
// reads ONE ANCHOR field and mounts a whole family behind it; fifteen handles
// have no gate of their own and are mounted on the strength of a sibling:
//
//	anchor                    family members it also mounts
//	Registrations             (its own route only)
//	FSAs                      (its own routes only)
//	Subscriptions             (its own routes only)
//	MirrorUsagePoints         MirrorMeterReadings
//	DERs                      DERCapabilities, DERSettings, DERStatuses,
//	                          DERAvailabilities, DERPrograms, DERControls,
//	                          DefaultDERControls, DERCurves
//	UsagePoints               MeterReadings, Readings, ReadingTypes
//	Configurations            (its own routes only)
//	DeviceStatuses            (its own routes only)
//	LogEvents                 (its own routes only)
//	PowerStatuses             (its own routes only)
//	MessagingPrograms         TextMessages
//	FlowReservationRequests   FlowReservationResponses
//	ResponseSets              Responses
//
// So wiring an anchor is a PROMISE that its whole family is wired. A member
// left out is a mis-wired deployment, not a narrower server: the routes that
// read it are already mounted and the links that point at them are already
// minted by handler packages that consult no store at all.
//
// Such a member is not dereferenced as a nil. BuildProtocolRouter substitutes a
// store that refuses every operation, logs the substitution once naming the
// field, and the routes answer 500 rather than panicking. See miswired.go for
// why the routes stay mounted, why the answer is a 500 and not an empty list,
// and why per-field gating was rejected.
//
// EndDevices is on NEITHER side of that table: it has no gate, because /edev is
// the root of the discovery walk and its routes mount whenever stores is
// non-nil. An absent EndDevices handle is refused like a mis-wired member
// instead: the ownership gate answers 500 on every /edev/{id} route, and the
// routes themselves, POST /edev and GET /edev included, are served over the
// refusing substitute requireEndDevices returns.
type Stores struct {
	EndDevices store.EndDeviceStore

	// EndDeviceManagers holds the provisioned (manager, managed) LFDI pairs
	// the ownership gate and GET /edev consult. Absent means no delegation:
	// every caller reaches only the EndDevice whose stored LFDI is its own.
	EndDeviceManagers store.EndDeviceManagementStore

	// EndDeviceIndexes allocates the opaque, server-chosen index that
	// addresses an EndDevice in resource URLs ("/edev/3/rg"). It is an
	// ADDRESSING mechanism only: device identity remains the
	// certificate-derived LFDI on the EndDevice record, and every ownership
	// check still compares that stored LFDI against the caller's
	// certificate.
	//
	// Nil is permitted and BuildProtocolRouter substitutes a fresh
	// process-local allocator, which keeps a zero-value Stores usable in
	// tests. A production server SHOULD supply
	// memory.NewEndDeviceIndexWithPersistence so indices survive restart;
	// with the process-local substitute they do not, and a restart
	// re-addresses the fleet. See memory.EndDeviceIndex for why that
	// matters and what a client's poll cycle does and does not recover.
	EndDeviceIndexes *memory.EndDeviceIndex
	// Registrations holds the Registration served at /edev/{id}/rg.
	//
	// memory.NewRegistrationStore is the in-memory implementation and
	// memory.NewRegistrationStoreWithPersistence the durable-snapshot one;
	// this field names neither, because which one a consumer wires is its
	// decision and no route here depends on the answer.
	Registrations store.ResourceStore[sep2.Registration]

	// RegistrationPolicy supplies the pIN and pollRate for the Registration
	// that is created with every EndDevice.
	//
	// The zero value provisions nothing, and that is fail-closed rather
	// than degraded: a server that cannot say what a device's pIN is has no
	// Registration to serve, so the device is served with no
	// RegistrationLink at all instead of one that answers 404. Wiring a
	// PIN resolver is what turns registration on.
	//
	// It is honored only when Registrations is non-nil, which is the same
	// condition that mounts GET /edev/{id}/rg: link and route are enabled
	// by one decision, so neither can exist without the other.
	//
	// An EndDevices store that is ALREADY a *memory.RegisteredEndDeviceStore
	// is left alone and this field is ignored for it. That is the path an
	// embedder that seeds devices at boot must take: the coupling has to be
	// in place before the first seeded Create, which happens before this
	// router is built.
	RegistrationPolicy memory.RegistrationPolicy

	MirrorUsagePoints   store.ResourceStore[sep2.MirrorUsagePoint]
	MirrorMeterReadings store.ScopedStore[sep2.MirrorMeterReading]

	// DER stores
	DERs              store.ScopedStore[sep2.DER]
	DERCapabilities   store.ScopedStore[sep2.DERCapability]
	DERSettings       store.ScopedStore[sep2.DERSettings]
	DERStatuses       store.ScopedStore[sep2.DERStatus]
	DERAvailabilities store.ScopedStore[sep2.DERAvailability]
	// DERPrograms holds the DERPrograms served under an FSA.
	//
	// memory.NewDERProgramStore is the in-memory implementation and
	// memory.NewDERProgramStoreWithPersistence the durable-snapshot one.
	// Reads and writes both go through this one handle: before it was
	// interface-typed the list route reached past the persistence wrapper to
	// the store it embedded, which was harmless only because the wrapper
	// shadows no read method.
	DERPrograms        store.ScopedStore[sep2.DERProgram]
	DERControls        store.ScopedStore[sep2.DERControl]
	DefaultDERControls store.ScopedStore[sep2.DefaultDERControl]
	DERCurves          store.ResourceStore[sep2.DERCurve]

	// FSA store
	FSAs store.ScopedStore[sep2.FunctionSetAssignments]

	// AdminFSAs is the admin FSA management plane
	// (GRIDAPPSD/ieee-2030_5-server-go#163: operator-authored templates,
	// program links, device assignments). Distinct from FSAs above which
	// is the spec-facing scoped surface.
	AdminFSAs *memory.AdminFSAStore

	// Subscription store
	Subscriptions *memory.SubscriptionStore

	// Server-side metering
	UsagePoints   store.ResourceStore[sep2.UsagePoint]
	MeterReadings store.ScopedStore[sep2.MeterReading]
	Readings      store.ScopedStore[sep2.Reading]
	ReadingTypes  store.ResourceStore[sep2.ReadingType]

	// New function sets
	Configurations           store.ScopedStore[sep2.Configuration]
	DeviceStatuses           store.ScopedStore[sep2.DeviceStatus]
	LogEvents                store.ScopedStore[sep2.LogEvent]
	PowerStatuses            store.ScopedStore[sep2.PowerStatus]
	MessagingPrograms        store.ResourceStore[sep2.MessagingProgram]
	TextMessages             store.ScopedStore[sep2.TextMessage]
	FlowReservationRequests  store.ScopedStore[sep2.FlowReservationRequest]
	FlowReservationResponses store.ScopedStore[sep2.FlowReservationResponse]
	ResponseSets             store.ResourceStore[sep2.ResponseSet]
	Responses                store.ScopedStore[sep2.Response]
}

// RouterConfig carries the scalar configuration values the protocol router
// needs. It replaces the server-side *config.Config so core never imports
// internal/config. Field types match config.Config exactly.
type RouterConfig struct {
	TZOffset    int32
	DSTOffset   int32
	DSTStart    int64 // DST start (unix seconds)
	DSTEnd      int64 // DST end (unix seconds)
	TimeQuality uint8 // sep2.TimeQuality* values

	// PostRateProvider supplies the server's preferred
	// MirrorUsagePoint.postRate for the client creating a mirror, keyed on
	// that client's LFDI. It is threaded to POST /mup; see
	// metering.PostRateProvider for the sep.xsd:6487 basis and the override
	// semantics.
	//
	// Nil (the zero value) means the server states no preference, and POST
	// /mup stores whatever postRate the client supplied, which is the
	// behavior every consumer had before this field existed. It lives on
	// RouterConfig rather than as a new BuildProtocolRouter parameter so
	// adding it breaks no existing caller.
	//
	// Rate policy is server-owned, not core-owned: core deliberately ships
	// no default here, because "how often may this client post to me" is an
	// ingest-budget question only the deploying server can answer.
	PostRateProvider coremetering.PostRateProvider
}

// AuthPolicy bundles the three auth touch points the protocol router and the
// ported handlers need. The server wires its internal/auth implementations;
// tests supply pass-through stubs. Core imports nothing from internal/auth.
//
// Zero-value safety: a zero-value AuthPolicy is safe and fail-closed.
// BuildProtocolRouter substitutes deny-all stubs for any nil func field
// before wiring, so a nil Identity or SFDIPrefix never causes a nil-panic
// at request time: they return ok=false (403) and an error (500)
// respectively. A nil Wrap installs NO middleware (no ACL enforcement);
// this is intentional for tests but is NOT safe for production: see the
// Wrap field comment below.
type AuthPolicy struct {
	// Wrap composes the identity and ACL middleware around the protocol mux.
	// The server passes auth.IdentityMiddleware composed with
	// auth.ACLMiddleware(auth.DefaultACLRules()); a test passes a no-op.
	//
	// WARNING: nil Wrap disables ALL middleware for the protocol mux,
	// meaning no TLS identity is extracted and no ACL rules are applied.
	// This is only safe for unit tests. A production server MUST supply a
	// non-nil Wrap that includes at minimum auth.IdentityMiddleware and
	// auth.ACLMiddleware; BuildProtocolRouter logs a warning when Wrap is nil.
	Wrap func(http.Handler) http.Handler

	// Identity extracts the authenticated device identity from the request
	// context. Replaces the direct auth.GetIdentity calls in the ported
	// edev and registration handlers. Returns ok=false when unauthenticated,
	// causing those handlers to return 403 Forbidden.
	//
	// If nil, BuildProtocolRouter substitutes a deny-all stub (always
	// returns ok=false) so handlers fail closed rather than panicking.
	Identity func(ctx context.Context) (lfdi, sfdi string, ok bool)

	// SFDIPrefix derives the EndDevice id prefix from an SFDI, replacing
	// auth.ExtractSFDIPrefix (the short-SFDI guard,
	// GRIDAPPSD/ieee-2030_5-server-go#13). Injected so the guard policy
	// stays server-owned.
	//
	// If nil, BuildProtocolRouter substitutes a stub that always returns an
	// error so the create path fails with 500 rather than panicking.
	SFDIPrefix func(sfdi string) (string, error)
}

// ResourceNotifier is aliased from the enddevice package so consumers can
// name assembly.ResourceNotifier without importing the enddevice subpackage.
type ResourceNotifier = coreedev.ResourceNotifier

// BuildProtocolRouter creates the HTTP router for the protocol listener
// and returns the canonical pattern list mounted on its protocol mux.
// The notifier is invoked on resource state changes that drive subscription
// fan-out (e.g. CSIP V1.2 MAINT-002 EndDevice DELETE). Pass nil to disable
// notification: tests that don't care about subscriptions can do this.
//
// The admin surface (AdminCertService, admin_* routes) and the test-mutation
// surface (RegisterMutationHandlers) are NOT included: they are
// server-config-specific and not part of the core export. Admin FSA create
// (HandleCreateFSA in the fsa handler package) is one such consumer-wired
// admin handler: it is wired by the consuming server on its own admin mux,
// not here.
func BuildProtocolRouter(
	cfg RouterConfig,
	stores *Stores,
	authPolicy AuthPolicy,
	serverSFDI, serverLFDI string,
	notifier ResourceNotifier,
) (http.Handler, []string) {
	// F1: substitute deny-all stubs for nil func fields so zero-value
	// AuthPolicy is safe and fail-closed, never a nil-panic at request time.
	if authPolicy.Identity == nil {
		log.Print("assembly: AuthPolicy.Identity is nil: every request is treated as unauthenticated and refused")
		authPolicy.Identity = func(_ context.Context) (string, string, bool) {
			return "", "", false // deny: handlers return 403
		}
	}
	if authPolicy.SFDIPrefix == nil {
		authPolicy.SFDIPrefix = func(_ string) (string, error) {
			return "", fmt.Errorf("SFDIPrefix not configured: deny")
		}
	}

	// F2: nil Wrap disables all middleware (no TLS identity extraction, no
	// ACL enforcement). Log loudly so a production misconfiguration is visible.
	if authPolicy.Wrap == nil {
		log.Print("assembly: AuthPolicy.Wrap is nil: no identity middleware and no ACL enforcement; safe for tests only")
	}

	top := http.NewServeMux()

	protocolMux := newRecordingMux()
	protocolMux.HandleFunc("GET /dcap", coredcap.HandleDeviceCapability())
	protocolMux.HandleFunc("GET /tm", coresep2time.HandleTime(coresep2time.TimeParams{
		TZOffset:    cfg.TZOffset,
		DSTOffset:   cfg.DSTOffset,
		DSTStart:    cfg.DSTStart,
		DSTEnd:      cfg.DSTEnd,
		TimeQuality: cfg.TimeQuality,
	}))
	protocolMux.HandleFunc("GET /sdev", coresdev.HandleSelfDevice(serverSFDI, serverLFDI))
	protocolMux.HandleFunc("GET /sdev/sdi", coredevinfo.HandleDeviceInformation(serverLFDI))

	if stores != nil {
		// Every helper registers through the ownership gate, so each
		// /edev/{id}-scoped route is bound to the caller wherever it is mounted.
		gated := newOwnershipGate(protocolMux, stores.EndDevices, stores.EndDeviceManagers, authPolicy.Identity)
		registerEndDeviceRoutes(gated, stores, authPolicy, notifier)
		registerMirrorRoutes(gated, stores, authPolicy, cfg.PostRateProvider)
		registerDERRoutes(gated, stores)
		registerMeteringRoutes(gated, stores)
		registerNewFunctionSetRoutes(gated, stores)
	}

	var protocolChain http.Handler
	if authPolicy.Wrap != nil {
		protocolChain = authPolicy.Wrap(protocolMux)
	} else {
		protocolChain = protocolMux
	}

	for _, prefix := range topLevelMounts {
		top.Handle(prefix, protocolChain)
	}

	return bufferContentLength(encoding.NamespaceMiddleware(top)), protocolMux.Patterns()
}

// topLevelMounts are the prefixes under which the protocol mux is mounted on
// the outer mux. A pattern registered on the protocol mux whose family is
// absent here is unreachable no matter how well formed it is, which is the
// same advertised-but-unrouted defect one layer up, so the href probe in
// hrefs.go mounts the same list rather than testing the protocol mux alone.
//
// The paired bare and trailing-slash entries are both required: "/edev"
// matches only the collection, "/edev/" matches everything beneath it.
var topLevelMounts = []string{
	"/dcap", "/tm", "/sdev", "/sdev/",
	"/edev", "/edev/",
	"/mup", "/mup/",
	"/dc", "/dc/",
	"/upt", "/upt/", "/rt", "/rt/",
	"/msg", "/msg/",
	"/rsps", "/rsps/",
}

// routeRegistrar is the surface the register*Routes helpers need from a mux.
// *http.ServeMux satisfies it; recordingMux below satisfies it and captures
// the pattern list.
type routeRegistrar interface {
	HandleFunc(pattern string, handler func(http.ResponseWriter, *http.Request))
}

// recordingMux is a thin wrapper around *http.ServeMux that captures every
// pattern handed to HandleFunc so Patterns() can enumerate them at boot.
type recordingMux struct {
	mux      *http.ServeMux
	patterns []string
}

func newRecordingMux() *recordingMux {
	return &recordingMux{mux: http.NewServeMux()}
}

func (r *recordingMux) HandleFunc(pattern string, h func(http.ResponseWriter, *http.Request)) {
	r.mux.HandleFunc(pattern, h)
	r.patterns = append(r.patterns, pattern)
}

func (r *recordingMux) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	r.mux.ServeHTTP(w, req)
}

// Patterns returns a sorted, de-duplicated copy of every registered pattern.
func (r *recordingMux) Patterns() []string {
	if len(r.patterns) == 0 {
		return nil
	}
	out := make([]string, len(r.patterns))
	copy(out, r.patterns)
	sort.Strings(out)
	w := 0
	for i, v := range out {
		if i == 0 || v != out[w-1] {
			out[w] = v
			w++
		}
	}
	return out[:w]
}

// notifyRemover is a local interface for the type assertion in asNotifyRemoved.
// core's *subscription.Manager satisfies it.
// Defined here at the consumer (Pike rule: interfaces at the consumer).
type notifyRemover interface {
	NotifyRemoved(ctx context.Context, sub sep2.Subscription) error
}

// asNotifyRemoved extracts NotifyRemoved as a function value if n implements
// notifyRemover, or returns nil. Keeps the router free of a hard import on
// the subscription package: ResourceNotifier is the published parameter surface
// and the production *subscription.Manager satisfies both interfaces.
func asNotifyRemoved(n ResourceNotifier) func(context.Context, sep2.Subscription) error {
	if n == nil {
		return nil
	}
	if nr, ok := n.(notifyRemover); ok {
		return nr.NotifyRemoved
	}
	return nil
}

// notificationURIValidator is satisfied by *subscription.Manager, so the
// create route vets a notificationURI under the same policy delivery uses.
type notificationURIValidator interface {
	ValidateNotificationURI(ctx context.Context, uri string) error
}

// asNotificationURIValidator returns n's validator, or nil so the create
// handler falls back to the default DestinationPolicy.
func asNotificationURIValidator(n ResourceNotifier) func(context.Context, string) error {
	if v, ok := n.(notificationURIValidator); ok {
		return v.ValidateNotificationURI
	}
	return nil
}

// registrationBoundEndDevices returns the EndDevice store the /edev routes
// must use: one that writes an EndDevice and its Registration as a single
// act and serves a RegistrationLink only when the record behind it exists.
//
// It decorates rather than replaces, and it declines to decorate twice. An
// embedder that seeds devices at boot has to build the binding itself,
// because seeding happens before this router is assembled; when it hands us
// the bound store it already has, wrapping it again would put a second
// binding with an empty policy in front of the configured one and silently
// deprovision the fleet.
//
// A nil Registrations store means the /rg route is not mounted at all, so
// there is nothing to bind to and nothing to advertise. The undecorated
// store is returned and no EndDevice carries a RegistrationLink, which is
// what 2018 section 4.4 p.19 requires of an unimplemented function set.
func registrationBoundEndDevices(stores *Stores) store.EndDeviceStore {
	if store.IsAbsent(stores.EndDevices) || store.IsAbsent(stores.Registrations) {
		return stores.EndDevices
	}
	if bound, ok := stores.EndDevices.(*memory.RegisteredEndDeviceStore); ok {
		return bound
	}
	return memory.NewRegisteredEndDeviceStore(stores.EndDevices, stores.Registrations, stores.RegistrationPolicy)
}

// logEventLinkedEndDevices returns the EndDevice store the /edev routes must
// use so every served EndDevice advertises its LogEventList.
//
// THE GATE IS THE MOUNT GATE. It decorates exactly when Stores.LogEvents is
// non-nil, which is the same condition registerNewFunctionSetRoutes uses to
// mount GET, POST /edev/{id}/lel and the instance routes. One decision enables
// the link and the routes together, so a server cannot advertise a function set
// it does not serve (2018 section 4.4 p.19) nor serve one it does not advertise
// (which is the state this card found: routes mounted, no link anywhere, so
// CSIP BASIC-027 step 2 could not pass). Changing either gate without the other
// is the regression to look for.
//
// It declines to decorate twice for the same reason registrationBoundEndDevices
// does: an embedder that seeds devices at boot builds the binding itself, and a
// second wrapper in front of the configured one buys nothing.
func logEventLinkedEndDevices(devs store.EndDeviceStore, stores *Stores) store.EndDeviceStore {
	if store.IsAbsent(devs) || store.IsAbsent(stores.LogEvents) {
		return devs
	}
	if linked, ok := devs.(*memory.LogEventLinkedEndDeviceStore); ok {
		return linked
	}
	return memory.NewLogEventLinkedEndDeviceStore(devs)
}

// flowReservationLinkedEndDevices returns the EndDevice store the /edev
// routes must use so every served EndDevice carries, or is stripped of, its
// flow reservation list links.
//
// Unlike [logEventLinkedEndDevices] and [registrationBoundEndDevices], this
// decorator is never skipped: it always wraps devs, choosing between its
// served and unserved arms. Skipping it when Stores.FlowReservationRequests
// is absent would leave a client-supplied link on the resource uncleared,
// which is the one respect this pattern strengthens rather than copies; see
// the package comment on [FlowReservationLinkedEndDeviceStore] for why.
//
// THE GATE IS STILL THE MOUNT GATE: which arm is chosen is
// Stores.FlowReservationRequests, the identical condition
// registerNewFunctionSetRoutes uses to mount GET, POST /edev/{id}/frq and
// GET /edev/{id}/frp. Changing this gate without changing that one is the
// regression to look for, exactly as for its sibling.
//
// It also does not special-case a devs that is already this type: every
// method of the type re-derives both links from the OUTERMOST layer's own
// served field, so an inner layer built for a different Stores (or by an
// embedder) is overwritten rather than trusted, and this call's own gate
// always wins.
func flowReservationLinkedEndDevices(devs store.EndDeviceStore, stores *Stores) store.EndDeviceStore {
	if store.IsAbsent(devs) {
		return devs
	}
	if store.IsAbsent(stores.FlowReservationRequests) {
		return memory.NewFlowReservationUnservedEndDeviceStore(devs)
	}
	return memory.NewFlowReservationLinkedEndDeviceStore(devs)
}

// ownedEndDevices returns the fully-decorated EndDevice store the /edev
// routes and the read handle serve from: registration-bound,
// LogEventList-linked, flow-reservation-linked, and refusing rather than
// panicking when unwired. registerEndDeviceRoutes and NewReaderStores both
// call it, so the four-decorator chain is typed out in this one function
// rather than twice, and the two callers cannot drift apart.
//
// Order between the LogEvent and flow reservation decorators does not
// matter: each owns a disjoint set of fields on the served EndDevice and
// neither reads what the other writes.
//
// Not every reader of EndDevices goes through it: BuildProtocolRouter's own
// ownership gate reads stores.EndDevices directly, because it only compares
// the stored LFDI and never serves the record to a client, so the
// RegistrationLink, LogEventListLink and flow reservation link derivations
// make no difference to it.
func ownedEndDevices(stores *Stores) store.EndDeviceStore {
	return requireEndDevices(flowReservationLinkedEndDevices(logEventLinkedEndDevices(registrationBoundEndDevices(stores), stores), stores))
}

func registerEndDeviceRoutes(mux routeRegistrar, stores *Stores, authPolicy AuthPolicy, notifier ResourceNotifier) {
	// A nil index allocator is substituted rather than rejected so a
	// zero-value Stores stays usable, but the substitute is process-local:
	// log it, because on a production server it means every device is
	// re-addressed on restart.
	edevIndexes := stores.EndDeviceIndexes
	if edevIndexes == nil {
		log.Print("assembly: Stores.EndDeviceIndexes is nil: using a process-local EndDevice index; URL indices will NOT survive restart")
		edevIndexes = memory.NewEndDeviceIndex()
	}

	// Every EndDevice route reads and writes through the registration-bound
	// store, so the EndDevice and its Registration are one act on the write
	// side and one derivation on the read side. Every route below takes
	// edevs, not stores.EndDevices: a route left on the undecorated store
	// would be the one that reintroduces the drift.
	//
	// The LogEventList and flow reservation link advertisements layer on top
	// of that; order between those two does not matter, since each owns a
	// disjoint set of fields.
	//
	// All three decorators return an absent handle (stores.EndDevices itself)
	// unchanged, so requireEndDevices' substitute is applied exactly when
	// Stores.EndDevices is absent and never decorated. Registration and
	// LogEvent additionally skip decorating when their OWN function set is
	// not served; flow reservation does not, and always picks an arm, for
	// the reason its package comment gives.
	edevs := ownedEndDevices(stores)

	mux.HandleFunc("GET /edev", coreedev.HandleEndDeviceListForCaller(edevs, stores.EndDeviceManagers, authPolicy.Identity, 900))
	mux.HandleFunc("POST /edev", coreedev.HandleCreateEndDevice(edevs, edevIndexes, authPolicy.Identity, authPolicy.SFDIPrefix))
	mux.HandleFunc("GET /edev/{id}", coreedev.HandleEndDevice(edevs))
	mux.HandleFunc("PUT /edev/{id}", coreedev.HandleUpdateEndDevice(edevs))
	mux.HandleFunc("DELETE /edev/{id}", coreedev.HandleDeleteEndDevice(edevs, notifier))

	// Registration GET handler at /edev/{id}/rg
	// (GRIDAPPSD/ieee-2030_5-server-go#170).
	if !store.IsAbsent(stores.Registrations) {
		mux.HandleFunc("GET /edev/{id}/rg", corereg.HandleGetRegistration(edevs, stores.Registrations, authPolicy.Identity))
	}

	// FSA endpoints
	if !store.IsAbsent(stores.FSAs) {
		mux.HandleFunc("GET /edev/{id}/fsa", scopedListHandler[sep2.FunctionSetAssignments, sep2.FunctionSetAssignmentsList](
			stores.FSAs, "id", corefsa.BuildFSAList, 900,
		))
		mux.HandleFunc("GET /edev/{id}/fsa/{fsaId}", corefsa.HandleFSA(stores.FSAs))
	}

	// Subscription endpoints
	if !store.IsAbsent(stores.Subscriptions) {
		mux.HandleFunc("GET /edev/{id}/sub", coresub.HandleListSubscriptionsByDevice(stores.Subscriptions, 900))
		mux.HandleFunc("POST /edev/{id}/sub", coresub.HandleCreateSubscription(stores.Subscriptions, asNotificationURIValidator(notifier)))
		mux.HandleFunc("DELETE /edev/{id}/sub/{subId}", coresub.HandleDeleteSubscription(stores.Subscriptions, asNotifyRemoved(notifier)))
	}
}

func registerMirrorRoutes(mux routeRegistrar, stores *Stores, authPolicy AuthPolicy, postRateProvider coremetering.PostRateProvider) {
	if store.IsAbsent(stores.MirrorUsagePoints) {
		return
	}
	// MirrorMeterReadings has no gate of its own: it is mounted on the
	// strength of MirrorUsagePoints (see miswired.go).
	mirrorMeterReadings := requireScoped(stores.MirrorMeterReadings, "MirrorMeterReadings")
	// LFDIProvider extracts the device LFDI from the request context via the
	// injected AuthPolicy.Identity. Keeps internal/auth out of core (the same
	// callback-injection pattern that Phase D1 applied to the obs callback).
	lfdiProvider := coremetering.LFDIProvider(func(ctx context.Context) (string, bool) {
		lfdi, _, ok := authPolicy.Identity(ctx)
		return lfdi, ok
	})
	mux.HandleFunc("GET /mup", corelisthandler.ListHandler[sep2.MirrorUsagePoint, sep2.MirrorUsagePointList](
		stores.MirrorUsagePoints, coremetering.BuildMirrorUsagePointList, 300,
	))
	mux.HandleFunc("POST /mup", coremetering.HandleCreateMirrorUsagePoint(stores.MirrorUsagePoints, lfdiProvider, postRateProvider))
	mux.HandleFunc("GET /mup/{id}", coremetering.HandleMirrorUsagePoint(stores.MirrorUsagePoints, lfdiProvider))
	mux.HandleFunc("POST /mup/{id}/mr", coremetering.HandlePostMirrorMeterReading(
		stores.MirrorUsagePoints, mirrorMeterReadings, lfdiProvider,
	))

	// IEEE 2030.5-2018 section 10.11.3 rule (d): the client posts readings
	// "to the resource identified in the Metering server's response... (e.g.,
	// /mup/3)" -- that resource is the Location header POST /mup returns,
	// which HandleCreateMirrorUsagePoint sets to exactly "/mup/{id}". The
	// WADL marks POST /mup/{id} Mandatory. Before this route existed, a
	// client that followed our own advertised Location header got a 405: the
	// EPRI reference client does exactly that (retrieve.c's
	// process_response reads http_location() on the POST /mup response and
	// posts the follow-up MirrorMeterReading to that literal path, not to
	// our /mr convention). Mounting it against the identical handler used
	// for POST /mup/{id}/mr means both routes share stampMirrorMeterReading
	// and the section 10.11.3 rule (e) ownership gate, so the two can never
	// drift into minting different href shapes, or into enforcing creator
	// scope on one path and not the other, for the same resource kind.
	mux.HandleFunc("POST /mup/{id}", coremetering.HandlePostMirrorMeterReading(
		stores.MirrorUsagePoints, mirrorMeterReadings, lfdiProvider,
	))

	// The two Mandatory methods on the MirrorUsagePoint instance.
	// PUT is sep_wadl.xml:2303 and DELETE is
	// sep_wadl.xml:2323, both wx:mode="M", and neither was mounted: a client
	// following the Location header POST /mup returns, with a method the
	// standard requires a server to implement, got a 405 from the server that
	// had just named the URI.
	//
	// Mounting them also completes the Allow header on this path. Go's
	// ServeMux derives the 405 Allow set from the methods registered for a
	// matching pattern, so before this change a PUT was answered with an Allow
	// that listed only the methods core happened to serve, which is exactly the
	// section 4.3 c) 4) header a client uses to decide what it may do next.
	// Both new shapes are declared in MintableHrefs in this same change:
	// mounting and advertising are one act.
	//
	// Reachability through the bridge and server-go ACLs is NOT addressed here.
	// Both wrappers classify /mup as read-and-create and return 405 before the
	// mux, so these two methods stay dark in those deployments until the
	// wrapper tables are removed upstream. Editing those tables from here is
	// the cross-repo lockstep drift to avoid.
	mux.HandleFunc("PUT /mup/{id}", coremetering.HandlePutMirrorUsagePoint(
		stores.MirrorUsagePoints, lfdiProvider, postRateProvider,
	))
	mux.HandleFunc("DELETE /mup/{id}", coremetering.HandleDeleteMirrorUsagePoint(
		stores.MirrorUsagePoints, mirrorMeterReadings, lfdiProvider,
	))
}

func registerDERRoutes(mux routeRegistrar, stores *Stores) {
	if store.IsAbsent(stores.DERs) {
		return
	}

	// The DER family's own gate is Stores.DERs, above; every handle below is
	// mounted on the strength of it and has no gate of its own. requireScoped
	// and requireResource are what make that a stated contract rather than an
	// accident: a member left unwired is logged once here and refuses at
	// request time, instead of being dereferenced as a nil.
	// See miswired.go for why the routes stay mounted.
	derCapabilities := requireScoped(stores.DERCapabilities, "DERCapabilities")
	derSettings := requireScoped(stores.DERSettings, "DERSettings")
	derStatuses := requireScoped(stores.DERStatuses, "DERStatuses")
	derAvailabilities := requireScoped(stores.DERAvailabilities, "DERAvailabilities")
	derPrograms := requireScoped(stores.DERPrograms, "DERPrograms")
	derControls := requireScoped(stores.DERControls, "DERControls")
	defaultDERControls := requireScoped(stores.DefaultDERControls, "DefaultDERControls")
	derCurves := requireResource(stores.DERCurves, "DERCurves")

	dercap, derg, ders, dera := coreder.DERSingletonHandlers(
		derCapabilities, derSettings, derStatuses, derAvailabilities,
	)

	// Which DER sub-resource links this router is permitted to advertise, per
	// IEEE 2030.5-2018 section 4.4 ("If a function set is not implemented, Link
	// elements to resources in that function set SHALL NOT be included").
	//
	// It is declared HERE, next to the mounts it describes, because the answer
	// to "is this function set implemented" is a property of this function and
	// nothing else. A parallel constant elsewhere could rot; this cannot drift
	// further than the next four lines. The four sub-resources below are mounted
	// unconditionally whenever the DER function set is wired at all, which is
	// what makes all four bits true. A card that mounts a fifth flips its bit in
	// the same commit: mounting and advertising are one act.
	derLinks := coreder.DERLinkPolicy{
		Capability:   true,
		Settings:     true,
		Status:       true,
		Availability: true,
	}

	mux.HandleFunc("GET /edev/{id}/der", scopedListHandler[sep2.DER, sep2.DERList](
		stores.DERs, "id", coreder.DERListBuilder(derLinks), 900,
	))

	// The DER instance itself. Every DERList member carries this
	// href as its own, and before this route existed following it produced a 404
	// from the server that had just advertised it. HEAD comes free: a ServeMux
	// pattern registered for GET matches HEAD as well.
	//
	// PUT is mode O in the WADL (sep_wadl.xml:4116) but on the certified path:
	// SunSpec CTP CORE-014 and CORE-016 walk DERList through to the DER and PUT
	// to these resources. GET and PUT share ONE handler value so the two verbs
	// cannot drift into different scope derivations, the same reason the four
	// sub-resources above are registered as pairs against one handler.
	//
	// DELETE (mode O, not yet implemented) and POST (mode E) fall through
	// to a 405 carrying an Allow header derived from itemMethods, which section
	// 4.3 c) 4) requires and an unmounted path could not produce: it would 404.
	derInstance := scopedResourceHandler[sep2.DER](
		stores.DERs, "id", "derId", itemMethods{Put: true}, coreder.StampDERInstance(derLinks),
	)
	mux.HandleFunc("GET /edev/{id}/der/{derId}", derInstance)
	mux.HandleFunc("PUT /edev/{id}/der/{derId}", derInstance)

	mux.HandleFunc("GET /edev/{id}/der/{derId}/dercap", dercap)
	mux.HandleFunc("PUT /edev/{id}/der/{derId}/dercap", dercap)
	mux.HandleFunc("GET /edev/{id}/der/{derId}/derg", derg)
	mux.HandleFunc("PUT /edev/{id}/der/{derId}/derg", derg)
	mux.HandleFunc("GET /edev/{id}/der/{derId}/ders", ders)
	mux.HandleFunc("PUT /edev/{id}/der/{derId}/ders", ders)
	mux.HandleFunc("GET /edev/{id}/der/{derId}/dera", dera)
	mux.HandleFunc("PUT /edev/{id}/der/{derId}/dera", dera)

	// DERProgram under FSA. The handle is the configured store itself, not
	// something reached out of it: the route used to take the *ScopedStore that
	// the persistence wrapper embedded, which was correct only because the
	// wrapper shadows no read method and would have quietly bypassed one that
	// it did.
	mux.HandleFunc("GET /edev/{id}/fsa/{fsaId}/derp", scopedListHandler[sep2.DERProgram, sep2.DERProgramList](
		derPrograms, "id", coreder.BuildDERProgramList, 900,
	))

	// A DERProgram's own href, so the FSA-to-DERProgramList-to-member link
	// walk CSIP v2.0 s5.2.3.1 requires actually resolves.
	// Scoped by device {id} only, matching the list route above (and the
	// DERProgram store's own scoping): {fsaId} is part of the mounted path
	// shape, not a filter on which programs are visible under it. Read-only:
	// core exposes no write route for a single DERProgram, mirroring the
	// DERControl single-resource route just below. The empty itemMethods is
	// what says so, and it renders exactly the Allow this route answered with
	// before the method set became declarative: GET, HEAD.
	mux.HandleFunc("GET /edev/{id}/fsa/{fsaId}/derp/{derpId}",
		scopedResourceHandler[sep2.DERProgram](derPrograms, "id", "derpId", itemMethods{}, nil))

	// DERControl under DERProgram
	mux.HandleFunc("GET /edev/{id}/fsa/{fsaId}/derp/{derpId}/derc",
		scopedListHandlerDeep[sep2.DERControl, sep2.DERControlList](
			derControls, coreder.BuildDERControlList, 900,
		))

	// A DERControl's own href, so an activated event resolves. A client that
	// activates an event arms a fast poll against the event's own URI, so
	// without this route that poll 404s and the client tears the event down
	// (the EPRI reference client turns a non-200 into RETRIEVE_FAIL and then
	// calls remove_stub), which caps delivery at one event per client.
	//
	// Scoped by the SAME composite parent key as the list route above
	// (id/fsaId/derpId, see scopedResourceHandlerDeep), so a control is
	// reachable only under the device path it was stored beneath. Read-only:
	// the DOWN path writes controls through the store, never over HTTP.
	//
	// Stamped with the response request on the way out, exactly as the list
	// route stamps its members. The adapter drops the request
	// because the stamp does not depend on it: replyTo names one server-owned
	// URI and responseRequired is a constant, unlike the DER instance stamp
	// above, which derives links from path values.
	mux.HandleFunc("GET /edev/{id}/fsa/{fsaId}/derp/{derpId}/derc/{dercId}",
		scopedResourceHandlerDeep[sep2.DERControl](derControls, "dercId",
			func(_ *http.Request, ctrl *sep2.DERControl) { coreder.StampResponseRequest(ctrl) }))

	// DefaultDERControl is utility-set, not device-written: only GET is
	// mounted for protocol clients. The handler itself also refuses PUT with
	// 405 (GET, HEAD), so this stays true even if a PUT mount is added back
	// here without reading why it was removed (#456).
	mux.HandleFunc("GET /edev/{id}/fsa/{fsaId}/derp/{derpId}/dderc",
		coreder.DefaultDERControlHandler(defaultDERControls))

	// Global DERCurve
	mux.HandleFunc("GET /dc", corelisthandler.ListHandler[sep2.DERCurve, sep2.DERCurveList](
		derCurves, coreder.BuildDERCurveList, 900,
	))
}

// scopedListHandler creates a list handler scoped by the path value named
// parentParam.
//
// parentParam is passed rather than hardcoded to "id" for the reason argued at
// [scopedResourceHandler], and this helper is where that defect actually
// shipped. The parent wildcard is not called {id} on every mounted shape:
// the metering family names it {uptId} and the messaging family {msgId}.
// r.PathValue on a wildcard the pattern does not declare returns "" rather than
// failing, so a hardcoded "id" scoped every lookup on those two shapes under the
// empty parent, and both lists served empty forever with a 200 and no log line.
// Naming the parameter at the mount is what prevents it: a wrong name is now a
// visible mismatch between the mount and the pattern, which the guard in
// pathvalue_test.go reads directly.
func scopedListHandler[T store.Copier[T], L any](
	scopedStore store.ScopedStore[T],
	parentParam string,
	buildList func(href string, result store.ListResult[T], pollRate uint32) L,
	pollRate uint32,
) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		parentID := r.PathValue(parentParam)
		st := store.Under(scopedStore, parentID)
		h := corelisthandler.ListHandler[T, L](st, buildList, pollRate)
		h.ServeHTTP(w, r)
	}
}

// scopedListHandlerDeep creates a list handler scoped by composite key id/fsaId/derpId.
func scopedListHandlerDeep[T store.Copier[T], L any](
	scopedStore store.ScopedStore[T],
	buildList func(href string, result store.ListResult[T], pollRate uint32) L,
	pollRate uint32,
) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		key := deepScopeKey(r)
		st := store.Under(scopedStore, key)
		h := corelisthandler.ListHandler[T, L](st, buildList, pollRate)
		h.ServeHTTP(w, r)
	}
}

// deepScopeKey builds the composite parent key id/fsaId/derpId that scopes
// resources nested under a DERProgram.
//
// The list handler and the single-resource handler MUST derive their scope
// identically, which is why this is one function rather than the expression
// repeated in each. Duplicating the expression would let the two routes
// drift, and a single-resource route that scoped more loosely than its list
// would widen what a store lookup can return.
//
// This key binds a RESOURCE to the path it was stored under. It does NOT
// bind a CALLER to that path; store scoping and caller ownership are different
// properties, and this function implements only the former. Caller ownership
// of {id} is enforced before any handler runs, by ownershipGate.
func deepScopeKey(r *http.Request) string {
	return r.PathValue("id") + "/" + r.PathValue("fsaId") + "/" + r.PathValue("derpId")
}

// scopedResourceHandlerDeep creates a read-only single-resource handler scoped
// by the same composite key id/fsaId/derpId as scopedListHandlerDeep, keyed
// within that scope by the path value named idParam.
//
// It serves the STORED value directly rather than rebuilding a document, so
// the bytes match what the list serves for the same resource field for field.
// Behavior on the two non-happy paths is deliberate:
//
//   - A miss (wrong device, wrong program, or an id that never existed) is a
//     clean 404 with no body, never a synthesized zero-valued resource. An
//     empty 200 would be worse than the 404 this route exists to fix: a client
//     would parse the zero value as a real resource and could act on it.
//   - A store error other than not-found is a 500, because it means the store
//     failed rather than that the resource is absent, and collapsing the two
//     would report a broken server as a missing resource.
//
// stamp, when non-nil, completes a resource for the wire before it is served,
// and takes the same shape as [scopedResourceHandler]'s so the two mounts are
// read the same way. It exists here so a resource kind whose LIST is stamped on
// the way out (DERControl's replyTo and responseRequired) is
// stamped identically on its own href: the two routes serve the same resource,
// and a field present on one and absent on the other is a conformance trap,
// which TestSingleDERControlBytesMatchListMember exists to catch.
func scopedResourceHandlerDeep[T store.Copier[T]](
	scopedStore store.ScopedStore[T],
	idParam string,
	stamp func(r *http.Request, resource *T),
) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			encoding.MethodNotAllowed(w, "GET, HEAD")
			return
		}

		resource, err := scopedStore.Get(r.Context(), deepScopeKey(r), r.PathValue(idParam))
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				http.Error(w, "not found", http.StatusNotFound)
				return
			}
			srverr.Internal(w, r, err)
			return
		}

		if stamp != nil {
			stamp(r, &resource)
		}

		encoding.WriteXML(w, http.StatusOK, &resource)
	}
}

// maxRequestBody caps how much of a request body this package reads into
// memory. It matches the limit the singleton handler applies to the DER
// sub-resources (handlers/singleton), so a client cannot find a larger document
// accepted on one DER path than on its neighbour.
const maxRequestBody = 1 << 20

// itemMethods declares which WADL-declared methods a [scopedResourceHandler]
// mount implements beyond GET and HEAD, which every mount serves.
//
// It is a parameter rather than a hardcoded switch because the WADL is the
// source of truth for the method set and it differs per resource. Passing it in
// means the Allow header on a 405 is generated from the same declaration that
// decides which branches exist, so the two cannot disagree. That matters
// directly: section 4.3 c) 4) requires an explicit 400 or 405 for a method in
// mode E, and a 405 whose Allow lies about what is served is barely better than
// the 404 an unmounted path would have produced.
type itemMethods struct {
	// Put mounts PUT on the resource, upserting the request body at the id the
	// path names.
	Put bool

	// Delete mounts DELETE on the resource, removing the record the path
	// names. It is a WRITE, and this helper carries no ownership binding of its
	// own: it constrains WHERE a record may be reached from, not WHO may reach
	// it. Set it only for a shape whose DELETE the WADL declares Mandatory, and
	// read the caveat at [scopedResourceHandler] before setting it for a new
	// one.
	Delete bool
}

// allow renders the Allow header value for a 405 on this mount.
//
// The order matches what http.ServeMux produces for the same method set (it
// sorts), so a 405 raised by the mux and a 405 raised by this handler cannot
// give a client two different Allow headers for the same shape.
func (m itemMethods) allow() string {
	allowed := []string{"GET", "HEAD"}
	if m.Delete {
		allowed = append(allowed, "DELETE")
	}
	if m.Put {
		allowed = append(allowed, "PUT")
	}
	sort.Strings(allowed)
	return strings.Join(allowed, ", ")
}

// scopedResourceHandler creates a single-resource handler scoped by the path
// value named parentParam alone, keyed within that scope by the path value
// named idParam.
//
// parentParam is passed rather than hardcoded to "id" because the parent
// wildcard is not called {id} on every mounted shape: the messaging family
// names it {msgId}. r.PathValue on a wildcard the pattern does not declare
// returns "", which would key every lookup under the empty parent, so the
// resource would be unreachable under its own parent and reachable under every
// other one. That is a silent wrong-scope defect rather than a visible error,
// and naming the parameter at the mount is what prevents it. The sibling list
// helper [scopedListHandler] carried exactly that defect and now takes the
// same argument for the same reason.
//
// It mirrors [scopedResourceHandlerDeep] one scope level up and keeps that
// function's contract on the non-happy paths, whose reasoning is argued there
// and not restated: a clean 404 on a miss, never a synthesized zero-valued
// resource, and a 500 rather than a 404 when the store fails for any other
// reason.
//
// The clean-404 rule is sharper here than it is one level up, because this
// handler can also accept PUT. A synthesized 200 would hand a client the
// sub-resource links of a resource that does not exist; the EPRI reference
// client follows exactly those links and PUTs into them; the singleton handler
// upserts on PUT; and store entries would then appear under an id nobody
// provisioned. A GET that fabricates a writable resource is resource creation
// through a read path. The 404 and the upserting PUT are a coherent pair only
// because the id comes from the path, which the scope and ownership layers above
// constrain, and never from this handler inventing one.
//
// stamp, when non-nil, completes a resource for the wire before it is served and
// before it is stored: it is where a resource's own href and its derived links
// are settled, so a document served from the store and a document just written
// by a client cannot disagree about either. It may be nil for a resource that
// needs no completion.
//
// WRITE SURFACE, stated plainly because it is easy to read the scoping above as
// more than it is. Both write methods this handler can mount, PUT and DELETE,
// are constrained here by the store scope and by NOTHING ELSE. The scope binds a
// record to the parent path it was stored under; it does not bind the CALLER to
// that parent. That binding exists only for parents under /edev/{id}, where
// ownershipGate refuses a caller who does not own {id} before this handler runs.
// Mounted under any other parent, this handler has no ownership check at all.
func scopedResourceHandler[T store.Copier[T]](
	scopedStore store.ScopedStore[T],
	parentParam string,
	idParam string,
	methods itemMethods,
	stamp func(r *http.Request, resource *T),
) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		parentKey := r.PathValue(parentParam)
		id := r.PathValue(idParam)

		switch {
		case r.Method == http.MethodGet, r.Method == http.MethodHead:
			resource, err := scopedStore.Get(r.Context(), parentKey, id)
			if err != nil {
				if errors.Is(err, store.ErrNotFound) {
					http.Error(w, "not found", http.StatusNotFound)
					return
				}
				srverr.Internal(w, r, err)
				return
			}
			if stamp != nil {
				stamp(r, &resource)
			}
			encoding.WriteXML(w, http.StatusOK, &resource)

		case r.Method == http.MethodPut && methods.Put:
			body, err := io.ReadAll(io.LimitReader(r.Body, maxRequestBody))
			if err != nil {
				http.Error(w, "read body failed", http.StatusBadRequest)
				return
			}
			var resource T
			if err := xml.Unmarshal(body, &resource); err != nil {
				srverr.BadRequestMessage(w, r, "invalid XML", err)
				return
			}
			// Stamped BEFORE the store write, not on the way back out, so the
			// stored document is the one the server vouches for. A client's own
			// href and any link it invented never reach the store.
			if stamp != nil {
				stamp(r, &resource)
			}
			if err := scopedStore.Create(r.Context(), parentKey, id, resource); err != nil {
				if !errors.Is(err, store.ErrAlreadyExists) {
					srverr.Internal(w, r, err)
					return
				}
				if err := scopedStore.Update(r.Context(), parentKey, id, resource); err != nil {
					srverr.Internal(w, r, err)
					return
				}
			}
			w.WriteHeader(http.StatusNoContent)

		case r.Method == http.MethodDelete && methods.Delete:
			// A miss is a 404, not a 204. A 204 for an id that was never
			// there tells a client its delete took effect, and a client that
			// believes a stale event is gone stops reconciling it.
			if err := scopedStore.Delete(r.Context(), parentKey, id); err != nil {
				if errors.Is(err, store.ErrNotFound) {
					http.Error(w, "not found", http.StatusNotFound)
					return
				}
				srverr.Internal(w, r, err)
				return
			}
			w.WriteHeader(http.StatusNoContent)

		default:
			encoding.MethodNotAllowed(w, methods.allow())
		}
	}
}

func registerMeteringRoutes(mux routeRegistrar, stores *Stores) {
	if store.IsAbsent(stores.UsagePoints) {
		return
	}
	// None of these three has a gate of its own: all are mounted on the
	// strength of UsagePoints (see miswired.go).
	meterReadings := requireScoped(stores.MeterReadings, "MeterReadings")
	readings := requireScoped(stores.Readings, "Readings")
	readingTypes := requireResource(stores.ReadingTypes, "ReadingTypes")
	mux.HandleFunc("GET /upt", corelisthandler.ListHandler[sep2.UsagePoint, sep2.UsagePointList](
		stores.UsagePoints, coremetering.BuildUsagePointList, 900,
	))
	mux.HandleFunc("POST /upt", coremetering.HandleCreateUsagePoint(stores.UsagePoints))
	mux.HandleFunc("GET /upt/{uptId}", coremetering.HandleUsagePoint(stores.UsagePoints))

	mux.HandleFunc("GET /upt/{uptId}/mr", scopedListHandler[sep2.MeterReading, sep2.MeterReadingList](
		meterReadings, "uptId", coremetering.BuildMeterReadingList, 900,
	))

	mux.HandleFunc("GET /upt/{uptId}/mr/{mrId}/r", func(w http.ResponseWriter, r *http.Request) {
		key := r.PathValue("uptId") + "/" + r.PathValue("mrId")
		st := store.Under(readings, key)
		h := corelisthandler.ListHandler[sep2.Reading, sep2.ReadingList](st, coremetering.BuildReadingList, 900)
		h.ServeHTTP(w, r)
	})

	mux.HandleFunc("GET /rt", corelisthandler.ListHandler[sep2.ReadingType, sep2.ReadingTypeList](
		readingTypes, coremetering.BuildReadingTypeList, 900,
	))
	mux.HandleFunc("GET /rt/{id}", coremetering.HandleReadingType(readingTypes))
}

func registerNewFunctionSetRoutes(mux routeRegistrar, stores *Stores) {
	if !store.IsAbsent(stores.Configurations) {
		mux.HandleFunc("GET /edev/{id}/cfg", coreconfiguration.HandleConfiguration(stores.Configurations))
		mux.HandleFunc("PUT /edev/{id}/cfg", coreconfiguration.HandleConfiguration(stores.Configurations))
	}
	if !store.IsAbsent(stores.DeviceStatuses) {
		mux.HandleFunc("GET /edev/{id}/dstat", coresingleton.HandleSingletonGetPut[sep2.DeviceStatus](
			stores.DeviceStatuses,
			func(r *http.Request) string { return r.PathValue("id") },
			func(r *http.Request) sep2.DeviceStatus {
				ds := sep2.DeviceStatus{}
				ds.Href = "/edev/" + r.PathValue("id") + "/dstat"
				return ds
			},
		))
		mux.HandleFunc("PUT /edev/{id}/dstat", coresingleton.HandleSingletonGetPut[sep2.DeviceStatus](
			stores.DeviceStatuses,
			func(r *http.Request) string { return r.PathValue("id") },
			func(r *http.Request) sep2.DeviceStatus {
				ds := sep2.DeviceStatus{}
				ds.Href = "/edev/" + r.PathValue("id") + "/dstat"
				return ds
			},
		))
	}
	if !store.IsAbsent(stores.LogEvents) {
		// The LogEvent function set at its WADL address.
		//
		// These four routes used to be two, mounted at /edev/{id}/log with no
		// instance route at all, while the WADL declares the list at
		// /edev/{id1}/lel (sep_wadl.xml:1358) and the instance at
		// /edev/{id1}/lel/{id2} (sep_wadl.xml:1404); 2018 A.3.5.1 and A.3.5.2
		// give the same sample URIs and Annex A.1 p.132 makes the WADL
		// normative. The data was served correctly at an address no conforming
		// client looks for, and the Location the POST minted resolved nowhere.
		//
		// The /log routes are REMOVED rather than kept as an alias. Nothing
		// advertised them: no production path assigned LogEventListLink at all
		// before this card, so /log was reachable only by knowing the string.
		// Keeping it would mean holding a list, a POST and an instance
		// conformant at two addresses forever, and any drift between them shows
		// a client a different LogEvent set depending on which it used.
		//
		// GET, HEAD and POST are mode M on the list; GET, HEAD and DELETE are
		// mode M on the instance. Every Mandatory method is mounted. PUT and
		// DELETE on the list (mode E), and PUT (mode O) and POST (mode E) on
		// the instance, are not, and each answers 405 from http.ServeMux with
		// an Allow derived from the registered method set.
		//
		// DELETE on the instance is a WRITE; the caller's ownership of {id} is
		// enforced by ownershipGate, and scopedResourceHandler adds none of its
		// own. It is mounted because the WADL declares it Mandatory.
		mux.HandleFunc("GET /edev/{id}/lel", scopedListHandler[sep2.LogEvent, sep2.LogEventList](
			stores.LogEvents, "id", corelogevent.BuildLogEventList, 900,
		))
		mux.HandleFunc("POST /edev/{id}/lel", corelogevent.HandlePostLogEvent(stores.LogEvents))

		logEventInstance := scopedResourceHandler[sep2.LogEvent](
			stores.LogEvents, "id", "lelId", itemMethods{Delete: true}, nil)
		mux.HandleFunc("GET /edev/{id}/lel/{lelId}", logEventInstance)
		mux.HandleFunc("DELETE /edev/{id}/lel/{lelId}", logEventInstance)
	}
	if !store.IsAbsent(stores.PowerStatuses) {
		mux.HandleFunc("GET /edev/{id}/ps", corepowerstatus.HandlePowerStatus(stores.PowerStatuses))
		mux.HandleFunc("PUT /edev/{id}/ps", corepowerstatus.HandlePowerStatus(stores.PowerStatuses))
	}

	if !store.IsAbsent(stores.MessagingPrograms) {
		// TextMessages has no gate of its own: it is mounted on the strength
		// of MessagingPrograms (see miswired.go).
		textMessages := requireScoped(stores.TextMessages, "TextMessages")

		mux.HandleFunc("GET /msg", corelisthandler.ListHandler[sep2.MessagingProgram, sep2.MessagingProgramList](
			stores.MessagingPrograms, coremessaging.BuildMessagingProgramList, 900,
		))
		mux.HandleFunc("GET /msg/{msgId}", coremessaging.HandleMessagingProgram(stores.MessagingPrograms))
		mux.HandleFunc("GET /msg/{msgId}/tm", scopedListHandler[sep2.TextMessage, sep2.TextMessageList](
			textMessages, "msgId", coremessaging.BuildTextMessageList, 900,
		))
		mux.HandleFunc("POST /msg/{msgId}/tm", coremessaging.HandlePostTextMessage(textMessages))

		// The TextMessage instance. The POST above returns this
		// href in a Location header and nothing served it, so a client that
		// followed the URI the server had just handed it got a 404.
		//
		// GET and HEAD are Mandatory (sep_wadl.xml:2851 and 2857). PUT and POST
		// are mode E and DELETE is mode D, and none is implemented here, so each
		// answers 405: with only a GET pattern registered http.ServeMux produces
		// that 405 itself and derives Allow from the registered method set, which
		// is why the empty itemMethods below cannot disagree with what is served.
		//
		// Scoped by {msgId}, named explicitly: the parent wildcard on this shape
		// is not called {id}, and reading an undeclared one would silently key
		// every message under the empty parent.
		mux.HandleFunc("GET /msg/{msgId}/tm/{tmId}",
			scopedResourceHandler[sep2.TextMessage](textMessages, "msgId", "tmId", itemMethods{}, nil))
	}

	if !store.IsAbsent(stores.FlowReservationRequests) {
		// FlowReservationResponses has no gate of its own: it is mounted on
		// the strength of FlowReservationRequests, and one POST writes both
		// halves (see miswired.go).
		flowReservationResponses := requireScoped(stores.FlowReservationResponses, "FlowReservationResponses")

		mux.HandleFunc("GET /edev/{id}/frq", scopedListHandler[sep2.FlowReservationRequest, sep2.FlowReservationRequestList](
			stores.FlowReservationRequests, "id", coreflowrsv.BuildFlowReservationRequestList, 900,
		))
		mux.HandleFunc("POST /edev/{id}/frq", coreflowrsv.HandlePostFlowReservationRequest(
			stores.FlowReservationRequests, flowReservationResponses,
		))
		mux.HandleFunc("GET /edev/{id}/frp", scopedListHandler[sep2.FlowReservationResponse, sep2.FlowReservationResponseList](
			flowReservationResponses, "id", coreflowrsv.BuildFlowReservationResponseList, 900,
		))

		// The two FlowReservation instances. One POST mints both
		// hrefs: the Location header for the request it just created, and the
		// FlowReservationResponse href the client polls for the server's
		// decision. Neither was served, so a client that made a reservation
		// could not read the reservation back nor learn whether it was granted.
		//
		// GET and HEAD are Mandatory on both (sep_wadl.xml:3956 and 3962 for the
		// request, 4033 and 4039 for the response). Every other declared method
		// answers 405 rather than 404, from http.ServeMux, which derives Allow
		// from the registered method set.
		//
		// Read-only here. PUT on FlowReservationRequest is mode M
		// (sep_wadl.xml:3963) and is NOT mounted; the missing Mandatory PUT is
		// carried as a finding rather than mounted here.
		frqInstance := scopedResourceHandler[sep2.FlowReservationRequest](
			stores.FlowReservationRequests, "id", "frqId", itemMethods{}, nil)
		mux.HandleFunc("GET /edev/{id}/frq/{frqId}", frqInstance)

		frpInstance := scopedResourceHandler[sep2.FlowReservationResponse](
			flowReservationResponses, "id", "frpId", itemMethods{}, nil)
		mux.HandleFunc("GET /edev/{id}/frp/{frpId}", frpInstance)
	}

	if !store.IsAbsent(stores.ResponseSets) {
		// Responses has no gate of its own: it is mounted on the strength of
		// ResponseSets, and every DERControl this server emits carries a
		// replyTo into it (see miswired.go).
		responses := requireScoped(stores.Responses, "Responses")

		// Seed the default ResponseSet before the routes that serve it.
		//
		// Every DERControl this server emits carries a replyTo pointing into
		// this set, so an unseeded set would leave that href
		// dangling: the POST route would answer, but GET /rsps would list
		// nothing and GET /rsps/{id} would 404, and a client cannot tell an
		// empty channel from a server that invented one. Seeding is
		// idempotent and yields to a set a consumer created under the same id.
		if err := coreresponse.SeedDefaultSet(context.Background(), stores.ResponseSets); err != nil {
			log.Printf("assembly: seeding the default ResponseSet: %v; replyTo hrefs will not resolve", err)
		}

		mux.HandleFunc("GET /rsps", corelisthandler.ListHandler[sep2.ResponseSet, sep2.ResponseSetList](
			stores.ResponseSets, coreflowrsv.BuildResponseSetList, 900,
		))
		mux.HandleFunc("GET /rsps/{rspsId}", coreresponse.HandleResponseSet(stores.ResponseSets))
		mux.HandleFunc("GET /rsps/{rspsId}/rsp", func(w http.ResponseWriter, r *http.Request) {
			rspsID := r.PathValue("rspsId")
			inner := store.Under(responses, rspsID)
			corelisthandler.ListHandler[sep2.Response, sep2.ResponseList](
				inner, coreflowrsv.BuildResponseList, 900,
			)(w, r)
		})
		mux.HandleFunc("POST /rsps/{rspsId}/rsp", coreflowrsv.HandlePostResponse(responses))
		mux.HandleFunc("GET /rsps/{rspsId}/rsp/{rspId}", coreresponse.HandleResponse(responses))
	}
}

// suppress unused import
var _ = paging.DefaultLimit
