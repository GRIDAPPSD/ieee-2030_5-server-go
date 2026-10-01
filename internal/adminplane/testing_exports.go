package adminplane

import (
	"maps"
	"net/http"
	"slices"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/auth"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/commitment"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/flowreservation"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/handler"
)

// The identifiers in this file exist for internal/server's tests. Those tests
// drive Run or the server's own store wiring, so they cannot live in this
// package, and a _test.go export would not reach them.

// AuthedAdminPatterns is the authenticated inner mux's own pattern list: the
// guard's actual domain, distinct from BuildAdminRouter's merged list, which
// also carries the public outer mux's routes (#579 MEDIUM-3).
func AuthedAdminPatterns(adminKey string, svc *handler.AdminCertService, stores *Stores, tlsMode string, tickets *auth.TicketStore, sessions *auth.SessionStore, legacyDashboard bool, trafficHandler http.Handler) []string {
	authed, _ := buildAuthedAdminMux(runConfig(adminKey, svc, stores, tlsMode, tickets, sessions, legacyDashboard, trafficHandler), noPanels())
	return authed.Patterns()
}

// The three guard tables below are returned as copies: the router consults
// the originals on every request, so a caller that could edit them could
// remove a credential requirement from a live server.

// AdminBodyTypes returns a copy of the admin write-route content-type table.
func AdminBodyTypes() map[string][]string {
	out := make(map[string][]string, len(adminBodyTypes))
	for pattern, types := range adminBodyTypes {
		out[pattern] = slices.Clone(types)
	}
	return out
}

// SensitiveAdminPatterns returns a copy of the certificate and
// traffic-capture route families (#579, #631).
func SensitiveAdminPatterns() map[string]struct{} {
	return maps.Clone(sensitiveAdminPatterns)
}

// NonSensitiveAdminWrites returns a copy of the admin write-route allowlist
// (#579).
func NonSensitiveAdminWrites() map[string]struct{} {
	return maps.Clone(nonSensitiveAdminWrites)
}

// NewAdminFlowReservationHandler is newAdminFlowReservationHandler.
func NewAdminFlowReservationHandler(stores *Stores) *handler.AdminFlowReservationHandler {
	return newAdminFlowReservationHandler(stores)
}

// NewAdminDERControlHandler is newAdminDERControlHandler.
func NewAdminDERControlHandler(stores *Stores) *handler.AdminDERControlHandler {
	return newAdminDERControlHandler(stores)
}

// RecoveryWriters is recoveryWriters.
func RecoveryWriters(stores *Stores, notifier flowreservation.Notifier) commitment.Writers {
	return recoveryWriters(stores, notifier)
}

// Persists is persists.
func Persists(s any) bool {
	return persists(s)
}
