package server

import (
	"net/http"
)

// sensitiveAdminPatterns is the certificate and traffic-capture route
// families (#579, #631), plus the one route that mints a credential those
// families' own recheck accepts (#579 HIGH-1). The certificate routes mint
// and return key material; the traffic routes return captured Authorization
// and Cookie header bytes verbatim; POST /auth/ticket mints the one-time
// ticket that credentialAdmits' Path C redeems, so admitting its issuance
// through the bypass alone let a caller mint its own way past the other two
// families in a second request. Kept as a map literal, not derived from the
// router at init time, so a new /api/certs or /api/traffic pattern added
// later fails the coverage test in admin_sensitive_routes_test.go instead of
// silently joining an already-protected family; a new credential-minting
// route needs the same manual addition here, since nothing about its name
// marks it as a member the way the two path prefixes do.
var sensitiveAdminPatterns = map[string]struct{}{
	"GET /api/certs/ca":           {},
	"POST /api/certs/server":      {},
	"POST /api/certs/device":      {},
	"GET /api/certs/device-types": {},
	"POST /api/certs/info":        {},
	"GET /api/traffic/":           {},
	"POST /auth/ticket":           {},
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
