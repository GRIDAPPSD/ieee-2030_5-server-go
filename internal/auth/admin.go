package auth

import (
	"crypto/subtle"
	"log"
	"net"
	"net/http"
	"strings"

	"github.com/GRIDAPPSD/ieee-2030_5-go/internal/certs"
)

// forwardedHeaders lists the proxy-injected headers that, when present on a
// request, indicate the request was routed through a reverse proxy (e.g.
// Caddy). The IEEE-132 loopback bypass declines for any such request so the
// existing Bearer/cookie/mTLS auth chain runs against operator traffic.
var forwardedHeaders = []string{
	"X-Forwarded-For",
	"X-Forwarded-Host",
	"X-Forwarded-Proto",
	"Forwarded",
}

// AdminTicketCookieName is the cookie name used by the browser login flow
// (IEEE-095). The cookie carries a TicketStore ticket; the middleware redeems
// the ticket on each request and re-issues a fresh ticket cookie so multi-
// request page navigation works under the one-time-use semantics.
const AdminTicketCookieName = "admin_ticket"

// AdminAuthMiddleware returns middleware that checks for admin authorization.
// Five paths are supported (checked in order):
//
//  0. Loopback bypass (IEEE-132): the request originated from a loopback
//     address (127.0.0.0/8 or ::1) AND no reverse-proxy forwarded header is
//     present. This is the local-developer ergonomic path: `make run` on
//     localhost has no working credentials by default, and Caddy in front
//     injects X-Forwarded-* so this bypass declines automatically and the
//     normal auth chain runs against operator traffic.
//  1. mTLS: client cert with admin policy OID (1.3.6.1.4.1.40732.2.5)
//  2. Bearer token: Authorization header matches adminKey
//  3. Auth ticket: ?ticket= query param validated against the TicketStore
//     (short-lived, one-time-use — for browser SSE/EventSource clients)
//  4. Cookie ticket: admin_ticket cookie redeemed via TicketStore, then a
//     fresh ticket cookie is re-set on the response (browser login flow,
//     IEEE-095).
//
// If adminKey is empty, Bearer auth is disabled (mTLS only).
// If tickets is nil, ticket auth is disabled.
func AdminAuthMiddleware(adminKey string, tickets *TicketStore) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Path 0: loopback bypass (IEEE-132). Declines automatically
			// when ANY proxy-forwarded header is present so Caddy-fronted
			// deployments still run the full auth chain.
			if isLoopbackRemote(r) && !hasForwardedHeader(r) {
				log.Printf("admin: loopback bypass admitted %s %s", r.Method, r.URL.Path)
				next.ServeHTTP(w, r)
				return
			}

			// Path A: mTLS with admin OID
			if r.TLS != nil && len(r.TLS.PeerCertificates) > 0 {
				cert := r.TLS.PeerCertificates[0]
				if certs.HasPolicyOID(cert, certs.OIDPolicyAdmin) {
					next.ServeHTTP(w, r)
					return
				}
			}

			// Path B: Bearer token
			if adminKey != "" {
				authHeader := r.Header.Get("Authorization")
				if strings.HasPrefix(authHeader, "Bearer ") {
					token := authHeader[7:]
					if token != "" && constantTimeEqual(token, adminKey) {
						next.ServeHTTP(w, r)
						return
					}
				}
			}

			// Path C: Short-lived auth ticket (for SSE/EventSource)
			if tickets != nil {
				if ticket := r.URL.Query().Get("ticket"); ticket != "" {
					if tickets.Redeem(ticket) {
						next.ServeHTTP(w, r)
						return
					}
				}
			}

			// Path D: Cookie ticket (browser login flow, IEEE-095). Same
			// one-time-use semantics as Path C, but on success we re-issue
			// a fresh ticket and re-set the cookie so page navigation works.
			if tickets != nil {
				if c, err := r.Cookie(AdminTicketCookieName); err == nil && c.Value != "" {
					if tickets.Redeem(c.Value) {
						if fresh, ierr := tickets.Issue(); ierr == nil {
							http.SetCookie(w, NewAdminTicketCookie(fresh))
						} else {
							// Cookie absent on next request will force a
							// 401 → redirect to /login. Log so operators
							// can diagnose a degraded ticket store.
							log.Printf("auth: reissue admin ticket cookie: %v", ierr)
						}
						next.ServeHTTP(w, r)
						return
					}
				}
			}

			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"admin authentication required"}`))
		})
	}
}

// NewAdminTicketCookie returns a Cookie carrying the given ticket value,
// configured with the security flags required for the browser login flow
// (HttpOnly, Secure, SameSite=Strict, Path=/). Centralizing the cookie
// construction keeps the login handler and the middleware re-issue path in
// agreement on the security posture.
func NewAdminTicketCookie(ticket string) *http.Cookie {
	return &http.Cookie{
		Name:     AdminTicketCookieName,
		Value:    ticket,
		Path:     "/",
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteStrictMode,
	}
}

func constantTimeEqual(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

// isLoopbackRemote reports whether r.RemoteAddr is a loopback address (IPv4
// 127.0.0.0/8 or IPv6 ::1). httptest.NewRequest sets RemoteAddr to
// "192.0.2.1:1234" by default, so callers that exercise the loopback branch
// in tests must set RemoteAddr explicitly.
func isLoopbackRemote(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		// RemoteAddr might be a bare IP (e.g. unix socket synthetics);
		// fall back to parsing the whole field.
		host = r.RemoteAddr
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	return ip.IsLoopback()
}

// hasForwardedHeader reports whether the request carries any reverse-proxy
// forwarded header (X-Forwarded-For, X-Forwarded-Host, X-Forwarded-Proto,
// or RFC 7239 Forwarded). An empty header value counts as not present so a
// proxy that strips the header can't accidentally enable the bypass.
func hasForwardedHeader(r *http.Request) bool {
	for _, name := range forwardedHeaders {
		if r.Header.Get(name) != "" {
			return true
		}
	}
	return false
}
