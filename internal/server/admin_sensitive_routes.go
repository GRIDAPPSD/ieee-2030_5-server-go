package server

import (
	"net/http"
)

// sensitiveAdminPatterns is the certificate and traffic-capture route
// families (#579, #631): every pattern BuildAdminRouter mounts under
// "/api/certs" or "/api/traffic". The certificate routes mint and return key
// material; the traffic routes return captured Authorization and Cookie
// header bytes verbatim. Both refuse a request admitted only by the #246
// loopback bypass, even though AdminAuthMiddleware already lets it through
// for every other admin route. Kept as a map literal, not derived from the
// router at init time, so a new /api/certs or /api/traffic pattern added
// later fails the coverage test in admin_sensitive_routes_test.go instead of
// silently joining an already-protected family.
var sensitiveAdminPatterns = map[string]struct{}{
	"GET /api/certs/ca":           {},
	"POST /api/certs/server":      {},
	"POST /api/certs/device":      {},
	"GET /api/certs/device-types": {},
	"POST /api/certs/info":        {},
	"GET /api/traffic/":           {},
}

// requireCredentialForSensitiveRoutes routes a sensitiveAdminPatterns
// request through guard (auth.RequireRealCredential) before next, and every
// other pattern straight to next unchanged. Wiring guard here, rather than
// unconditionally around the whole authed mux, means guard's bypass recheck
// (which can redeem a one-time ticket, #579 #631) runs at most once per
// request and only for the two families that need it. It also runs before
// requireAdminBodyTypes: a credential requirement outranks a content-type
// check, so a route guard refuses never reaches its handler's decoding
// logic. mux is read only for the pattern match, the same net/http lookup
// requireAdminBodyTypes performs; wiring both against the same authed mux
// keeps the two checks agreeing on which pattern matched a given request.
func requireCredentialForSensitiveRoutes(mux *recordingMux, guard func(http.Handler) http.Handler, next http.Handler) http.Handler {
	guarded := guard(next)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, pattern := mux.mux.Handler(r)
		if _, sensitive := sensitiveAdminPatterns[pattern]; sensitive {
			guarded.ServeHTTP(w, r)
			return
		}
		next.ServeHTTP(w, r)
	})
}
