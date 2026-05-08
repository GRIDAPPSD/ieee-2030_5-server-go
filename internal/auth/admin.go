package auth

import (
	"crypto/subtle"
	"net/http"
	"strings"

	"github.com/GRIDAPPSD/ieee-2030_5-go/pkg/certs"
)

// AdminAuthMiddleware returns middleware that checks for admin
// authorization. Three paths are tried in precedence order:
//
//  1. Path A — mTLS with admin policy OID. The admin listener is
//     configured with tls.VerifyClientCertIfGiven and a ClientCAs
//     pool, so any client cert that is presented has been validated
//     against the CA. If the validated leaf cert carries the admin
//     policy OID (1.3.6.1.4.1.40732.2.5), the request is authorized
//     without inspecting any header.
//  2. Path B — Bearer token. If Path A did not authorize (no cert,
//     or cert without admin OID), the Authorization header is checked
//     against adminKey using a constant-time compare. Disabled when
//     adminKey is empty.
//  3. Path C — Short-lived auth ticket. ?ticket= query param redeemed
//     against the TicketStore (one-time-use, for browser SSE/
//     EventSource clients that cannot set Authorization headers).
//     Disabled when tickets is nil.
//
// If none of the three authorize, the middleware writes a 401 with a
// JSON error body.
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
