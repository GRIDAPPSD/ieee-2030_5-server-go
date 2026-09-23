package auth_test

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/auth"
)

// #579, #631: RequireCredentialForSensitiveRoutes (internal/server) refuses
// the certificate and traffic-capture route families when Path 0 (the #246
// loopback bypass) short-circuited before any credential was consulted.
// AdmittedByLoopbackBypassOnly is the marker that decision reads.
//
// Path 0 runs FIRST and unconditionally admits any loopback, non-forwarded
// request: it does not inspect an Authorization header, a TLS peer cert, a
// ticket, or a cookie, so it wins even when the request also carries a
// correct credential. The marker therefore tracks "did Path 0 admit this
// request", not "did a credential ultimately validate" - RequireRealCredential
// (admin.go) re-checks the four credentialed paths itself when the marker is
// set, which is the part of the design that actually satisfies "a valid
// credential from a loopback address still succeeds" (#579's own wording).
func TestAdmittedByLoopbackBypassOnlyTracksPathZeroAdmission(t *testing.T) {
	adminCert := generateAdminCert(t)

	cases := []struct {
		name    string
		prepare func(t *testing.T, tickets *auth.TicketStore, sessions *auth.SessionStore) *http.Request
		want    bool
	}{
		{
			name: "loopback, no credential",
			prepare: func(_ *testing.T, _ *auth.TicketStore, _ *auth.SessionStore) *http.Request {
				req := httptest.NewRequest(http.MethodGet, "/api/certs/ca", nil)
				req.RemoteAddr = "127.0.0.1:54321"
				return req
			},
			want: true,
		},
		{
			// Path 0 wins regardless of an accompanying credential: this is
			// the control proving the marker means "Path 0 admitted", not
			// "no credential was present".
			name: "loopback, correct bearer also present: path 0 still wins",
			prepare: func(_ *testing.T, _ *auth.TicketStore, _ *auth.SessionStore) *http.Request {
				req := httptest.NewRequest(http.MethodGet, "/api/certs/ca", nil)
				req.RemoteAddr = "127.0.0.1:54321"
				req.Header.Set("Authorization", "Bearer test-key")
				return req
			},
			want: true,
		},
		{
			name: "loopback with a forwarded header: path 0 declines, bearer admits instead",
			prepare: func(_ *testing.T, _ *auth.TicketStore, _ *auth.SessionStore) *http.Request {
				req := httptest.NewRequest(http.MethodGet, "/api/certs/ca", nil)
				req.RemoteAddr = "127.0.0.1:54321"
				req.Header.Set("X-Forwarded-For", "203.0.113.5")
				req.Header.Set("Authorization", "Bearer test-key")
				return req
			},
			want: false,
		},
		{
			name: "non-loopback, mTLS admin cert",
			prepare: func(_ *testing.T, _ *auth.TicketStore, _ *auth.SessionStore) *http.Request {
				req := httptest.NewRequest(http.MethodGet, "/api/certs/ca", nil)
				req.TLS = adminTLSState(adminCert)
				return req
			},
			want: false,
		},
		{
			name: "non-loopback, bearer",
			prepare: func(_ *testing.T, _ *auth.TicketStore, _ *auth.SessionStore) *http.Request {
				req := httptest.NewRequest(http.MethodGet, "/api/certs/ca", nil)
				req.Header.Set("Authorization", "Bearer test-key")
				return req
			},
			want: false,
		},
		{
			name: "non-loopback, one-time query ticket",
			prepare: func(t *testing.T, tickets *auth.TicketStore, _ *auth.SessionStore) *http.Request {
				ticket, err := tickets.Issue()
				if err != nil {
					t.Fatal(err)
				}
				return httptest.NewRequest(http.MethodGet, "/api/certs/ca?ticket="+ticket, nil)
			},
			want: false,
		},
		{
			name: "non-loopback, cookie session",
			prepare: func(t *testing.T, _ *auth.TicketStore, sessions *auth.SessionStore) *http.Request {
				id, err := sessions.Issue()
				if err != nil {
					t.Fatal(err)
				}
				req := httptest.NewRequest(http.MethodGet, "/api/certs/ca", nil)
				req.AddCookie(&http.Cookie{Name: auth.AdminTicketCookieName, Value: id})
				return req
			},
			want: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tickets := auth.NewTicketStore(30 * time.Second)
			sessions := auth.NewSessionStore(testSessionIdle, testSessionAbsolute)

			var got, reached bool
			handler := auth.AdminAuthMiddleware("test-key", tickets, sessions)(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
				reached = true
				got = auth.AdmittedByLoopbackBypassOnly(r)
			}))

			req := tc.prepare(t, tickets, sessions)
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, req)

			if !reached {
				t.Fatalf("request never reached the handler: status = %d, body = %q", w.Code, w.Body.String())
			}
			if got != tc.want {
				t.Errorf("AdmittedByLoopbackBypassOnly = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestAdmittedByLoopbackBypassOnlyDefaultsToRecheck is #579 MEDIUM-3's fix: a
// bare *http.Request built without ever passing through AdminAuthMiddleware
// must read as needing the recheck, the same as an explicit bypass marker,
// so RequireRealCredential fails closed on a request it cannot place rather
// than admitting it unchecked. This does not weaken the table test above:
// every "want: false" case there is a request AdminAuthMiddleware itself
// marked admissionOutcomeCredential, which is the one value that still
// reports false.
func TestAdmittedByLoopbackBypassOnlyDefaultsToRecheck(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/certs/ca", nil)
	if !auth.AdmittedByLoopbackBypassOnly(req) {
		t.Fatal("a request that never passed through AdminAuthMiddleware reports not-bypass-only: it would skip the recheck")
	}
}
