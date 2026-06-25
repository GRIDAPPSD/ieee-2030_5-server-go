// assembly_seam.go builds the inputs to assembly.BuildProtocolRouter from
// the server's concrete types and selects between the in-tree router and
// the core router at boot time.
//
// Phase 1 (IEEESRV-001): the core router is constructed and testable
// but is NOT the live router unless SEP2_USE_CORE_ROUTER=1 is set in
// the environment. The in-tree BuildProtocolRouter remains the default.
// Phase 2 (IEEESRV-002) will flip the default once equivalence is
// proven in production and the duplicate router internals are deleted.
package server

import (
	"context"
	"log"
	"net/http"
	"os"
	"reflect"

	"github.com/GRIDAPPSD/ieee-2030_5-go/internal/auth"
	"github.com/GRIDAPPSD/ieee-2030_5-go/internal/config"
	"github.com/GRIDAPPSD/ieee-2030_5-go/internal/handler"
	"gitlab.pnnl.gov/arista/ieee-2030_5/ieee-2030_5-core/pkg/sep2srv/assembly"
)

// CoreRouterEnabled reports whether the SEP2_USE_CORE_ROUTER environment
// variable is set to a truthy value ("1", "true", "yes"). The default is
// false: the in-tree BuildProtocolRouter remains live until Phase 2.
// Exported so the toggle test can verify the mapping table via t.Setenv.
func CoreRouterEnabled() bool {
	v := os.Getenv("SEP2_USE_CORE_ROUTER")
	return v == "1" || v == "true" || v == "yes"
}

// NewCoreRouterConfig maps the five scalar fields from *config.Config to
// assembly.RouterConfig. Core never imports internal/config; the server
// supplies these scalars explicitly. Exported so the equivalence test
// (and Phase 2 wiring) can use it directly.
func NewCoreRouterConfig(cfg *config.Config) assembly.RouterConfig {
	return assembly.RouterConfig{
		TZOffset:    cfg.TZOffset,
		DSTOffset:   cfg.DSTOffset,
		DSTStart:    cfg.DSTStart,
		DSTEnd:      cfg.DSTEnd,
		TimeQuality: cfg.TimeQuality,
	}
}

// NewCoreAuthPolicy wires the three server-side auth implementations into
// assembly.AuthPolicy using REAL production funcs, not test stubs.
//
// AuthPolicy.Wrap: composes IdentityMiddleware and ACLMiddleware around
// the protocol mux, preserving the same chain as the in-tree router
// (router.go: auth.IdentityMiddleware(auth.ACLMiddleware(...)(...mux))).
//
// AuthPolicy.Identity: adapts auth.GetIdentity's (DeviceIdentity, bool)
// return to the (lfdi, sfdi string, ok bool) signature core expects.
// Field order: LFDI first, SFDI second. The edev POST path feeds the
// second return (SFDI) into SFDIPrefix. Getting the order wrong here
// would silently misroute short-SFDI guard (IEEE-014).
//
// AuthPolicy.SFDIPrefix: wires auth.ExtractSFDIPrefix directly; its
// signature func(string) (string, error) matches core's expectation.
//
// Exported so the equivalence test can verify the policy compiles with
// real auth types (not just that the toggle path builds).
func NewCoreAuthPolicy() assembly.AuthPolicy {
	return assembly.AuthPolicy{
		Wrap: func(next http.Handler) http.Handler {
			return auth.IdentityMiddleware(auth.ACLMiddleware(auth.DefaultACLRules())(next))
		},
		Identity: func(ctx context.Context) (lfdi, sfdi string, ok bool) {
			id, ok := auth.GetIdentity(ctx)
			return id.LFDI, id.SFDI, ok
		},
		SFDIPrefix: auth.ExtractSFDIPrefix,
	}
}

// NewCoreStores converts the server-local *Stores to *assembly.Stores.
// assembly.Stores is a verbatim field-for-field lift of the server's own
// Stores type (same pkg/store and pkg/store/memory field types, confirmed
// in IEEECORE-001). The conversion is a direct field copy; no allocation
// of inner objects. Exported so the equivalence test can call both
// routers with the same underlying store instances.
func NewCoreStores(s *Stores) *assembly.Stores {
	if s == nil {
		return nil
	}
	return &assembly.Stores{
		EndDevices:               s.EndDevices,
		Registrations:            s.Registrations,
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
		FlowReservationResponses: s.FlowReservationResponses,
		ResponseSets:             s.ResponseSets,
		Responses:                s.Responses,
	}
}

// notifierAdapter wraps handler.ResourceNotifier so its value satisfies
// assembly.ResourceNotifier (which is coreedev.ResourceNotifier). Both
// interfaces have the identical method set:
//
//	Notify(ctx context.Context, resourceHref string, status uint8)
//
// The adapter avoids a runtime type assertion and is safe when notifier
// is nil (the adapter is nil in that case, not a non-nil interface wrapping
// a nil concrete value).
type notifierAdapter struct{ inner handler.ResourceNotifier }

func (a *notifierAdapter) Notify(ctx context.Context, resourceHref string, status uint8) {
	// inner is guaranteed non-nil by adaptNotifier; a nil handler.ResourceNotifier
	// produces a nil *notifierAdapter, not a non-nil adapter wrapping nil.
	a.inner.Notify(ctx, resourceHref, status)
}

// adaptNotifier wraps a handler.ResourceNotifier as an
// assembly.ResourceNotifier. Returns nil when n is nil so
// assembly.BuildProtocolRouter can skip fan-out safely (nil notifier
// is the documented "disable notification" sentinel).
//
// A typed nil (e.g. a nil *subscription.Manager stored in the interface) would
// pass the n == nil guard and panic when Notify is dispatched. The reflect
// check below rejects that case. The Kind guard is required: reflect.Value.IsNil
// panics on non-nilable kinds (struct, int, etc.), so we only call it for the
// six nilable kinds.
func adaptNotifier(n handler.ResourceNotifier) assembly.ResourceNotifier {
	if n == nil {
		return nil
	}
	v := reflect.ValueOf(n)
	switch v.Kind() {
	case reflect.Ptr, reflect.Interface, reflect.Map, reflect.Slice, reflect.Chan, reflect.Func:
		if v.IsNil() {
			return nil
		}
	}
	return &notifierAdapter{inner: n}
}

// SelectRouter is the toggle seam: it chooses between the core router and
// the in-tree router based on SEP2_USE_CORE_ROUTER. Both paths return
// (http.Handler, []string) so the call site in server.go is unchanged.
// The in-tree router is the default (SEP2_USE_CORE_ROUTER not set or
// set to any value other than "1", "true", or "yes").
// Exported so the toggle test can call it under both env states.
func SelectRouter(
	cfg *config.Config,
	stores *Stores,
	svc *handler.AdminCertService,
	serverSFDI, serverLFDI string,
	notifier handler.ResourceNotifier,
) (http.Handler, []string) {
	// Read the raw env value once, derive the bool from it, and log the raw
	// value so a fat-fingered flag ("True", "YES", etc.) is immediately visible
	// in the startup log rather than silently falling back to the in-tree router.
	rawEnv := os.Getenv("SEP2_USE_CORE_ROUTER")
	coreEnabled := CoreRouterEnabled()
	log.Printf("assembly: SEP2_USE_CORE_ROUTER=%q, core router=%v", rawEnv, coreEnabled)
	if coreEnabled {
		// svc (*handler.AdminCertService) is intentionally not forwarded: the
		// core router provides its own admin-cert routes and does not use the
		// in-tree AdminCertService. This is by design, not an oversight.
		return assembly.BuildProtocolRouter(
			NewCoreRouterConfig(cfg),
			NewCoreStores(stores),
			NewCoreAuthPolicy(),
			serverSFDI, serverLFDI,
			adaptNotifier(notifier),
		)
	}
	return BuildProtocolRouter(cfg, stores, svc, serverSFDI, serverLFDI, notifier)
}
