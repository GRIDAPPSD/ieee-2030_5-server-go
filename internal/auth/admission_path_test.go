package auth_test

import (
	"crypto/tls"
	"crypto/x509"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/auth"
)

// AdmissionPath names which admission path let a request through, for a
// handler's audit log line. It is read after the full chain the admin
// router wires: AdminAuthMiddleware, then RequireRealCredential.
func TestAdmissionPath(t *testing.T) {
	sessions := auth.NewSessionStore(testSessionIdle, testSessionAbsolute)
	sessionID, err := sessions.Issue()
	if err != nil {
		t.Fatal(err)
	}
	adminCert := generateAdminCert(t)
	const loopback, remote = "127.0.0.1:1234", "192.0.2.10:1234"

	cases := []struct {
		name        string
		remote      string
		recheck     bool
		build       func(r *http.Request, tickets *auth.TicketStore)
		wantPath    string
		wantReached bool
	}{
		{name: "loopback with no credential", remote: loopback, wantPath: auth.AdmissionPathLoopbackBypass, wantReached: true},
		{name: "bearer", remote: remote, build: func(r *http.Request, _ *auth.TicketStore) { r.Header.Set("Authorization", "Bearer test-key") }, wantPath: auth.AdmissionPathBearer, wantReached: true},
		{name: "mtls", remote: remote, build: func(r *http.Request, _ *auth.TicketStore) {
			r.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{adminCert}, VerifiedChains: [][]*x509.Certificate{{adminCert}}}
		}, wantPath: auth.AdmissionPathMTLS, wantReached: true},
		{name: "ticket", remote: remote, build: func(r *http.Request, tickets *auth.TicketStore) {
			ticket, err := tickets.Issue()
			if err != nil {
				t.Fatal(err)
			}
			r.URL.RawQuery = "ticket=" + ticket
		}, wantPath: auth.AdmissionPathTicket, wantReached: true},
		{name: "cookie session", remote: remote, build: func(r *http.Request, _ *auth.TicketStore) {
			r.AddCookie(&http.Cookie{Name: auth.AdminTicketCookieName, Value: sessionID})
		}, wantPath: auth.AdmissionPathCookie, wantReached: true},
		{name: "bearer from loopback rechecked", remote: loopback, recheck: true, build: func(r *http.Request, _ *auth.TicketStore) { r.Header.Set("Authorization", "Bearer test-key") }, wantPath: auth.AdmissionPathBearer, wantReached: true},
		{name: "loopback with no credential rechecked is refused", remote: loopback, recheck: true, wantReached: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tickets := auth.NewTicketStore(30 * time.Second)
			var got string
			reached := false
			var inner http.Handler = http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
				reached = true
				got = auth.AdmissionPath(r)
			})
			if tc.recheck {
				inner = auth.RequireRealCredential("test-key", tickets, sessions)(inner)
			}
			h := auth.AdminAuthMiddleware("test-key", tickets, sessions, true)(inner)
			req := httptest.NewRequest(http.MethodPost, "/api/der/controls", nil)
			req.RemoteAddr = tc.remote
			if tc.build != nil {
				tc.build(req, tickets)
			}
			h.ServeHTTP(httptest.NewRecorder(), req)
			if reached != tc.wantReached {
				t.Fatalf("reached = %v, want %v", reached, tc.wantReached)
			}
			if reached && got != tc.wantPath {
				t.Fatalf("AdmissionPath = %q, want %q", got, tc.wantPath)
			}
		})
	}

	unmarked := httptest.NewRequest(http.MethodGet, "/", nil)
	if got := auth.AdmissionPath(unmarked); got != auth.AdmissionPathNone {
		t.Fatalf("AdmissionPath(unmarked) = %q, want %q", got, auth.AdmissionPathNone)
	}
}
