package auth

import (
	"crypto/subtle"
	"net/http"
	"strings"

	"github.com/GRIDAPPSD/ieee-2030_5-go/internal/certs"
)

// AdminAuthMiddleware returns middleware that checks for admin authorization.
// Three paths are supported (checked in order):
//
//  1. mTLS: client cert with admin policy OID (1.3.6.1.4.1.40732.2.5)
//  2. Bearer token: Authorization header matches adminKey
//  3. Auth ticket: ?ticket= query param validated against the TicketStore
//     (short-lived, one-time-use — for browser SSE/EventSource clients)
//
// If adminKey is empty, Bearer auth is disabled (mTLS only).
// If tickets is nil, ticket auth is disabled.
func AdminAuthMiddleware(adminKey string, tickets *TicketStore) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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

			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"admin authentication required"}`))
		})
	}
}

func constantTimeEqual(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}
