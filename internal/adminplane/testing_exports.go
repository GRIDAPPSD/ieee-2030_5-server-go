package adminplane

import (
	"net/http"

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

// AdminBodyTypes is the admin write-route content-type table.
var AdminBodyTypes = adminBodyTypes

// SensitiveAdminPatterns is the certificate and traffic-capture route
// families (#579, #631).
var SensitiveAdminPatterns = sensitiveAdminPatterns

// NonSensitiveAdminWrites is the admin write-route allowlist (#579).
var NonSensitiveAdminWrites = nonSensitiveAdminWrites

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
