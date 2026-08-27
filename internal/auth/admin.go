package auth

import (
	"crypto/subtle"
	"log"
	"net"
	"net/http"
	"strings"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/certs"
)

// forwardedHeaders lists the proxy-injected headers that, when present on a
// request, indicate the request was routed through a reverse proxy (e.g.
// Caddy). The #246 loopback bypass declines for any such request so the
// existing Bearer/cookie/mTLS auth chain runs against operator traffic.
var forwardedHeaders = []string{
	"X-Forwarded-For",
	"X-Forwarded-Host",
	"X-Forwarded-Proto",
	"Forwarded",
}

// AdminTicketCookieName is the cookie name used by the browser login flow
// (#159). The cookie carries a SessionStore id, which the middleware
// validates without consuming: one page load authenticates the document and
// every subresource it pulls separately, so a consuming check admits the
// first of them and refuses the rest.
const AdminTicketCookieName = "admin_ticket"

// AdminAuthMiddleware returns middleware that checks for admin authorization.
// Five paths are supported (checked in order):
//
//  0. Loopback bypass (#246): the request originated from a loopback
//     address (127.0.0.0/8 or ::1) AND no reverse-proxy forwarded header is
//     present. This is the local-developer ergonomic path: `make run` on
//     localhost has no working credentials by default, and Caddy in front
//     injects X-Forwarded-* so this bypass declines automatically and the
//     normal auth chain runs against operator traffic.
//  1. mTLS: client cert with admin policy OID (1.3.6.1.4.1.40732.2.5)
//  2. Bearer token: Authorization header matches adminKey
//  3. Auth ticket: ?ticket= query param validated against the TicketStore
//     (short-lived, one-time-use — for browser SSE/EventSource clients)
//  4. Cookie session: admin_ticket cookie validated against the
//     SessionStore without being consumed (browser login flow, #159).
//
// Paths 3 and 4 take separate stores on purpose. A ticket value is URL-borne
// and single-use; a session value is cookie-borne and reusable until it
// expires. Handing both to one store would make the values interchangeable
// and force one lifetime rule onto both.
//
// If adminKey is empty, Bearer auth is disabled (mTLS only).
// If tickets is nil, query-ticket auth is disabled; if sessions is nil,
// cookie auth is disabled.
func AdminAuthMiddleware(adminKey string, tickets *TicketStore, sessions *SessionStore) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Path 0: loopback bypass (#246). Declines automatically
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

			// Path D: Cookie session (browser login flow, #159).
			// Validation slides the idle deadline but does not consume the
			// session, and no cookie is re-set here: a per-request rotation
			// cannot survive the parallel subresource loads of one page.
			if sessions != nil {
				if c, err := r.Cookie(AdminTicketCookieName); err == nil && c.Value != "" {
					if sessions.Validate(c.Value) {
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

// NewAdminTicketCookie returns a Cookie carrying the given session id,
// configured with the security flags required for the browser login flow
// (HttpOnly, Secure, SameSite=Strict, Path=/). Only the login handler sets
// this cookie; the middleware never re-issues it.
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
