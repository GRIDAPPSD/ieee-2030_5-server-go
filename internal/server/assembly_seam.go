// assembly_seam.go builds the inputs to assembly.BuildProtocolRouter from
// the server's concrete types and provides the thin BuildProtocolRouter
// adapter that wraps them into a single call.
//
// Phase 1: the core router was constructed and testable behind a
// SEP2_USE_CORE_ROUTER env toggle. The in-tree BuildProtocolRouter
// remained the boot default.
//
// Phase 2: core is now the ONLY protocol router. The toggle
// (CoreRouterEnabled / SEP2_USE_CORE_ROUTER) and the SelectRouter indirection
// are deleted. BuildProtocolRouter below is a thin adapter that converts the
// server's concrete types and delegates unconditionally to
// assembly.BuildProtocolRouter.
//
// The assembly itself now lives in pkg/sep2server, which is the importable
// surface an in-process consumer grafts onto. This file keeps the
// projection from the server's own concrete types (*config.Config, *Stores,
// handler.ResourceNotifier) into that surface's Config, and keeps
// BuildProtocolRouter's signature so the CSIP harness and the seam tests are
// untouched. There is exactly one assembly, and both the standalone binary and
// an embedder reach it through the same call.
package server

import (
	"net/http"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/adminplane"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/config"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/flowreservation"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/handler"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2server"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/assembly"
)

// NewEmbedConfig projects the server's own concrete types onto the router-level
// half of the embeddable surface's Config: the time and rate policy, the
// stores, the auth policy and the notifier.
//
// It deliberately leaves the SERVING half (Addr, the TLS material, Middleware,
// ConnState) at its zero value. Those are decisions Run makes when it binds a
// listener, and leaving them out here is what keeps BuildProtocolRouter
// returning the bare protocol router that the CSIP harness and the
// route-surface test expect.
func NewEmbedConfig(cfg *config.Config, stores *Stores, notifier handler.ResourceNotifier) sep2server.Config {
	return newEmbedConfig(cfg, stores, adminplane.AdaptNotifier(notifier))
}

// newEmbedConfig is NewEmbedConfig for a notifier already adapted, so a
// caller that also hands the adapted notifier elsewhere adapts it once.
func newEmbedConfig(cfg *config.Config, stores *Stores, notifier assembly.ResourceNotifier) sep2server.Config {
	return sep2server.Config{
		Router:   NewCoreRouterConfig(cfg),
		Stores:   NewCoreStores(stores),
		Auth:     NewCoreAuthPolicy(),
		Notifier: notifier,
	}
}

// BuildProtocolRouter constructs the SEP2 protocol router via
// sep2server.BuildHandler using the server-side seam adapters
// (NewCoreRouterConfig, NewCoreStores, NewCoreAuthPolicy, adminplane.AdaptNotifier).
//
// The svc parameter is intentionally ignored: certificate management lives
// on the admin listener only (PR #246 Leon CRITICAL) and is never forwarded
// to the protocol router. Call sites that previously passed svc may continue
// to do so; it is dropped before core sees it.
//
// Replaces the in-tree BuildProtocolRouter deleted in Phase 2. Core's
// assembly.BuildProtocolRouter is now the sole protocol router; there is
// no longer a toggle or an in-tree alternative.
func BuildProtocolRouter(cfg *config.Config, stores *Stores, _ *handler.AdminCertService, serverSFDI, serverLFDI string, notifier handler.ResourceNotifier) (http.Handler, []string) {
	coreHandler, patterns := sep2server.BuildHandler(
		NewEmbedConfig(cfg, stores, notifier),
		sep2srv.Identity{SFDI: serverSFDI, LFDI: serverLFDI},
	)
	// wrapMutationHandlers is a no-op in production builds (see
	// test_mutations_notest.go). Under csip_test_hooks it wraps
	// coreHandler with an outer mux that serves /test/mutations/* and
	// falls through to coreHandler for all other paths, restoring the
	// call that lived in the deleted in-tree router.go.
	return wrapMutationHandlers(coreHandler, stores, notifier), patterns
}

// NewCoreRouterConfig maps the scalar fields from *config.Config to
// assembly.RouterConfig. Core never imports internal/config; the server
// supplies these scalars explicitly. Exported so the equivalence test
// (and Phase 2 wiring) can use it directly.
//
// PEN goes through EffectivePEN, not the raw field, so an explicit 0 (IANA-
// reserved) reaches the router as unset rather than as a real PEN (#665).
func NewCoreRouterConfig(cfg *config.Config) assembly.RouterConfig {
	return assembly.RouterConfig{
		TZOffset:    cfg.TZOffset,
		DSTOffset:   cfg.DSTOffset,
		DSTStart:    cfg.DSTStart,
		DSTEnd:      cfg.DSTEnd,
		TimeQuality: cfg.TimeQuality,
		PEN:         cfg.EffectivePEN(),

		FlowReservationDeadline: cfg.FlowReservationDeadline,
	}
}

// newFlowReservationQueue builds the process's one queue through the
// assembly's own constructor, over the same stores the routes are given, and
// records each response's author in the answer store when one is wired. A nil
// notifier notifies no one.
func newFlowReservationQueue(stores *Stores, notifier assembly.ResourceNotifier) *flowreservation.Queue {
	q := assembly.NewFlowReservationQueue(NewCoreStores(stores), stores.PEN, adminplane.FlowReservationConfig(stores.FlowReservationDeadline).Deadline, notifier)
	q.RecordAnswers(adminplane.FlowReservationAnswers(stores))
	return q
}

// recoverAtBoot is the call Run makes; tests replace it to observe when it runs.
var recoverAtBoot = adminplane.RecoverFlowReservations

// startRetentionAtBoot is the call Run makes; tests replace it to observe
// when it runs and when its stop is called.
var startRetentionAtBoot = adminplane.StartFlowReservationRetention

// NewCoreAuthPolicy wires the three server-side auth implementations into
// assembly.AuthPolicy using REAL production funcs, not test stubs.
//
// AuthPolicy.Wrap: composes IdentityMiddleware and ACLMiddleware around
// the protocol mux, preserving the same chain as the deleted in-tree router
// (router.go: auth.IdentityMiddleware(auth.ACLMiddleware(...)(...mux))).
//
// AuthPolicy.Identity: adapts auth.GetIdentity's (DeviceIdentity, bool)
// return to the (lfdi, sfdi string, ok bool) signature core expects.
// Field order: LFDI first, SFDI second. The edev POST path feeds the
// second return (SFDI) into SFDIPrefix. Getting the order wrong here
// would silently misroute short-SFDI guard (#13).
//
// AuthPolicy.SFDIPrefix: wires auth.ExtractSFDIPrefix directly; its
// signature func(string) (string, error) matches core's expectation.
//
// The composition itself moved to sep2server.DefaultAuthPolicy, which is
// the one door an embedder has onto this server's enforcement. This stays
// as the in-tree name so every existing call site and test is unchanged,
// and so there is still exactly one composition behind both.
//
// Exported so tests can verify the policy compiles with real auth types.
func NewCoreAuthPolicy() assembly.AuthPolicy {
	return sep2server.DefaultAuthPolicy()
}

// NewCoreStores converts the server-local *Stores to *assembly.Stores.
// assembly.Stores is a verbatim field-for-field lift of the server's own
// Stores type (same pkg/store and pkg/store/memory field types). The
// conversion is a direct field copy; no allocation
// of inner objects. Exported so tests can call both the adapter and the
// route surface with the same underlying store instances.
func NewCoreStores(s *Stores) *assembly.Stores {
	if s == nil {
		return nil
	}
	return &assembly.Stores{
		EndDevices:               s.EndDevices,
		EndDeviceManagers:        s.EndDeviceManagers,
		EndDeviceIndexes:         s.EndDeviceIndexes,
		Registrations:            s.Registrations,
		RegistrationPolicy:       s.RegistrationPolicy,
		MirrorUsagePoints:        s.MirrorUsagePoints,
		MirrorMeterReadings:      s.MirrorMeterReadings,
		DERs:                     s.DERs,
		DERCapabilities:          s.DERCapabilities,
		DERSettings:              s.DERSettings,
		DERStatuses:              s.DERStatuses,
		DERAvailabilities:        s.DERAvailabilities,
		DERPrograms:              s.DERPrograms,
		DERControls:              s.DERControls,
		DefaultDERControls:       s.DefaultDERControls,
		DERCurves:                s.DERCurves,
		DERControlLifecycles:     s.DERControlLifecycles,
		FSAs:                     s.FSAs,
		AdminFSAs:                s.AdminFSAs,
		Subscriptions:            s.Subscriptions,
		UsagePoints:              s.UsagePoints,
		MeterReadings:            s.MeterReadings,
		Readings:                 s.Readings,
		ReadingTypes:             s.ReadingTypes,
		Configurations:           s.Configurations,
		DeviceStatuses:           s.DeviceStatuses,
		LogEvents:                s.LogEvents,
		PowerStatuses:            s.PowerStatuses,
		MessagingPrograms:        s.MessagingPrograms,
		TextMessages:             s.TextMessages,
		FlowReservationRequests:  s.FlowReservationRequests,
		FlowReservationResponses: adminplane.FlowReservationResponses(s),
		ResponseSets:             s.ResponseSets,
		Responses:                s.Responses,

		FlowReservationResponseLifecycles: s.FlowReservationResponseLifecycles,
		CommitmentLedger:                  s.CommitmentLedger,
		FlowReservationQueue:              s.FlowReservationQueue,
		DERControlIssuer:                  s.DERControlIssuer,

		Edition2023: s.Sep2Edition == handler.Edition2023,
	}
}
