package adminplane

import (
	"net/http"
	"strings"
)

// sensitiveAdminPatterns is the certificate and traffic-capture route
// families (#579, #631): the certificate routes mint and return key
// material, the traffic routes return captured Authorization and Cookie
// header bytes verbatim. Kept as a map literal, not derived from the router
// at init time, so a new /api/certs or /api/traffic pattern added later
// fails the coverage test in admin_sensitive_routes_test.go instead of
// silently joining an already-protected family.
var sensitiveAdminPatterns = map[string]struct{}{
	"GET /api/certs/ca":           {},
	"POST /api/certs/server":      {},
	"POST /api/certs/device":      {},
	"GET /api/certs/device-types": {},
	"POST /api/certs/info":        {},
	"GET /api/traffic/":           {},
}

// sensitiveAdminReadPrefixes are read families protected by path prefix
// rather than by an exact pattern entry in sensitiveAdminPatterns: each
// discloses another party's data by default, and each is a family that can
// gain routes over time, so a later GET under the same prefix is covered
// automatically instead of silently joining the unprotected group the way an
// exact-pattern list would leave it. Checked against the request path
// directly, not the resolved mux pattern, so a path with no route registered
// for it at all is covered too: the credential requirement does not wait for
// the route to exist.
//
//   - /api/management-pairs (#440, #677 fix round item 4): GET discloses an
//     aggregator's full managed fleet. The WRITE routes (create, remove,
//     rekey) are unaffected here: they already require a real credential by
//     default, since they are not on nonSensitiveAdminWrites below.
//   - /api/derms/ (#715 fix round 1, HIGH, all four review lanes, then #764):
//     the fleet read discloses every aggregator's managed LFDIs, DER status
//     and measurements, a wider disclosure than /api/management-pairs, which
//     already required a credential. The flow reservation and grant reads
//     disclose a fleet's requested and granted energy and who answered, so
//     the whole family is covered by prefix, not route by route.
//   - /api/ui/panels (#829): an embedder's panel View may disclose
//     anything, and the plane cannot tell what.
var sensitiveAdminReadPrefixes = []string{"/api/management-pairs", "/api/derms/", "/api/ui/panels"}

// isSensitiveAdminReadPath reports whether path falls under one of
// sensitiveAdminReadPrefixes.
func isSensitiveAdminReadPath(path string) bool {
	for _, prefix := range sensitiveAdminReadPrefixes {
		if strings.HasPrefix(path, prefix) {
			return true
		}
	}
	return false
}

// nonSensitiveAdminWrites is the explicit, reviewed allowlist of admin WRITE
// routes that do not need a real credential: FSA and end-device management,
// which create or delete configuration rows and never return key material, a
// captured header, or a ticket. Every other write route on the authed mux
// requires a real credential by default (#579 fix round 2, MEDIUM, coverage
// lane): #579 HIGH-1 found POST /auth/ticket credential-minting and absent
// from sensitiveAdminPatterns, which only ever grew by someone remembering
// to add a new route's exact name to two hand-written lists (the map here
// and the coverage test's own literal). A write route left off THIS list
// gets MORE scrutiny, not less, so a new minting route is refused the
// moment it exists rather than silently joining the low-scrutiny group;
// requireAdminBodyTypes (admin_body_type.go) already fails the same
// direction for content types. GET routes are unaffected and keep the
// sensitiveAdminPatterns allowlist above unchanged.
var nonSensitiveAdminWrites = map[string]struct{}{
	"POST /api/devices":                       {},
	"POST /api/devices/{id}/fsa-assignment":   {},
	"DELETE /api/devices/{id}/fsa-assignment": {},
	"POST /api/fsas":                          {},
	"DELETE /api/fsas/{id}":                   {},
	"POST /api/fsas/{id}/programs":            {},
	"DELETE /api/fsas/{id}/programs":          {},
}

// requireCredentialForSensitiveRoutes routes a sensitiveAdminPatterns
// request, and any write request not exempted by nonSensitiveAdminWrites,
// through guard (auth.RequireRealCredential) before next; every other
// request goes straight to next unchanged. Wiring guard here, rather than
// unconditionally around the whole authed mux, means guard's bypass recheck
// (which can redeem a one-time ticket, #579 #631) runs at most once per
// request and only for the routes that need it. It also runs before
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
		if (r.Method == http.MethodGet || r.Method == http.MethodHead) && isSensitiveAdminReadPath(r.URL.Path) {
			guarded.ServeHTTP(w, r)
			return
		}
		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
			next.ServeHTTP(w, r)
			return
		}
		if pattern == "" {
			// No authed pattern matched this method: an unrouted path, or a
			// method mismatch on a route registered for a different one
			// (net/http answers 405 itself for the latter). Let next decide,
			// the same early return requireAdminBodyTypes makes for the same
			// empty pattern, so a method-mismatched write still gets net/http's
			// own 405 instead of this guard's 401.
			next.ServeHTTP(w, r)
			return
		}
		if _, exempt := nonSensitiveAdminWrites[pattern]; !exempt {
			guarded.ServeHTTP(w, r)
			return
		}
		next.ServeHTTP(w, r)
	})
}
