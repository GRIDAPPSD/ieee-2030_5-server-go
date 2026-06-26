// assembly_seam.go builds the inputs to assembly.BuildProtocolRouter from
// the server's concrete types and provides the thin BuildProtocolRouter
// adapter that wraps them into a single call.
//
// Phase 1 (IEEESRV-001): the core router was constructed and testable
// behind a SEP2_USE_CORE_ROUTER env toggle. The in-tree BuildProtocolRouter
// remained the boot default.
//
// Phase 2 (IEEESRV-002): core is now the ONLY protocol router. The toggle
// (CoreRouterEnabled / SEP2_USE_CORE_ROUTER) and the SelectRouter indirection
// are deleted. BuildProtocolRouter below is a thin adapter that converts the
// server's concrete types and delegates unconditionally to
// assembly.BuildProtocolRouter.
package server

import (
	"context"
	"net/http"
	"reflect"

	"github.com/GRIDAPPSD/ieee-2030_5-go/internal/auth"
	"github.com/GRIDAPPSD/ieee-2030_5-go/internal/config"
	"github.com/GRIDAPPSD/ieee-2030_5-go/internal/handler"
	"gitlab.pnnl.gov/arista/ieee-2030_5/ieee-2030_5-core/pkg/sep2"
	"gitlab.pnnl.gov/arista/ieee-2030_5/ieee-2030_5-core/pkg/sep2srv/assembly"
)

// BuildProtocolRouter constructs the SEP2 protocol router via
// assembly.BuildProtocolRouter using the server-side seam adapters
// (NewCoreRouterConfig, NewCoreStores, NewCoreAuthPolicy, adaptNotifier).
//
// The svc parameter is intentionally ignored: certificate management lives
// on the admin listener only (PR #246 Leon CRITICAL) and is never forwarded
// to the protocol router. Call sites that previously passed svc may continue
// to do so; it is dropped before core sees it.
//
// Replaces the in-tree BuildProtocolRouter deleted in Phase 2 (IEEESRV-002).
// Core's assembly.BuildProtocolRouter is now the sole protocol router; there
// is no longer a toggle or an in-tree alternative.
func BuildProtocolRouter(cfg *config.Config, stores *Stores, _ *handler.AdminCertService, serverSFDI, serverLFDI string, notifier handler.ResourceNotifier) (http.Handler, []string) {
	return assembly.BuildProtocolRouter(
		NewCoreRouterConfig(cfg),
		NewCoreStores(stores),
		NewCoreAuthPolicy(),
		serverSFDI, serverLFDI,
		adaptNotifier(notifier),
	)
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
// the protocol mux, preserving the same chain as the deleted in-tree router
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
// Exported so tests can verify the policy compiles with real auth types.
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
// of inner objects. Exported so tests can call both the adapter and the
// route surface with the same underlying store instances.
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

// notifyRemover mirrors the unexported interface core uses to detect and
// extract NotifyRemoved from the notifier. Defined here at the consumer
// per the interface-at-consumer discipline. core's *subscription.Manager
// satisfies it.
type notifyRemover interface {
	NotifyRemoved(ctx context.Context, sub sep2.Subscription) error
}

// notifierAdapter wraps handler.ResourceNotifier so its value satisfies
// assembly.ResourceNotifier (which is coreedev.ResourceNotifier). Both
// interfaces have the identical Notify method set:
//
//	Notify(ctx context.Context, resourceHref string, status uint8)
//
// The adapter also forwards NotifyRemoved when the inner notifier supports
// it. Core's subscription DELETE handler calls NotifyRemoved (via type
// assertion) to dispatch the final Removed Notification (CSIP V1.2 §11.6);
// without this forwarding the notification is silently dropped.
//
// The adapter is safe when notifier is nil (the adapter is nil in that
// case, not a non-nil interface wrapping a nil concrete value).
type notifierAdapter struct{ inner handler.ResourceNotifier }

func (a *notifierAdapter) Notify(ctx context.Context, resourceHref string, status uint8) {
	// inner is guaranteed non-nil by adaptNotifier; a nil handler.ResourceNotifier
	// produces a nil *notifierAdapter, not a non-nil adapter wrapping nil.
	a.inner.Notify(ctx, resourceHref, status)
}

// NotifyRemoved forwards to the inner notifier when it satisfies the
// notifyRemover interface (i.e. the inner is *coresub.Manager or any
// other concrete type that implements the method). Returns nil when the
// inner does not implement NotifyRemoved; this is safe for core which
// treats a nil return as "no final notification."
func (a *notifierAdapter) NotifyRemoved(ctx context.Context, sub sep2.Subscription) error {
	if nr, ok := a.inner.(notifyRemover); ok {
		return nr.NotifyRemoved(ctx, sub)
	}
	return nil
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
