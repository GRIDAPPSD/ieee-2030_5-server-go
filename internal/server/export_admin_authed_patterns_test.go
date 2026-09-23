package server

import (
	"net/http"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/auth"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/handler"
)

// AuthedAdminPatterns exposes the authenticated inner mux's own pattern
// list to the external test package (#579 MEDIUM-3, test coverage lane):
// the guard's actual domain, distinct from BuildAdminRouter's merged list,
// which also carries the public outer mux's routes. A route registered on
// the outer mux would satisfy the old merged-list comparison without ever
// reaching requireCredentialForSensitiveRoutes; TestSensitiveAdminPatternsMatchRouterFamilies
// compares sensitiveAdminPatterns against this set instead.
func AuthedAdminPatterns(adminKey string, svc *handler.AdminCertService, stores *Stores, tlsMode string, tickets *auth.TicketStore, sessions *auth.SessionStore, legacyDashboard bool, trafficHandler http.Handler) []string {
	authed, _ := buildAuthedAdminMux(adminKey, svc, stores, tlsMode, tickets, sessions, legacyDashboard, trafficHandler)
	return authed.Patterns()
}
