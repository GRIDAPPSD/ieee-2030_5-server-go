package auth_test

import (
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
	cases := []struct {
		name        string
		remote      string
		bearer      string
		recheck     bool
		wantPath    string
		wantReached bool
	}{
		{name: "loopback with no credential", remote: "127.0.0.1:1234", wantPath: auth.AdmissionPathLoopbackBypass, wantReached: true},
		{name: "bearer from a non-loopback address", remote: "192.0.2.10:1234", bearer: "test-key", wantPath: auth.AdmissionPathBearer, wantReached: true},
		{name: "bearer from loopback rechecked", remote: "127.0.0.1:1234", bearer: "test-key", recheck: true, wantPath: auth.AdmissionPathBearer, wantReached: true},
		{name: "loopback with no credential rechecked is refused", remote: "127.0.0.1:1234", recheck: true, wantReached: false},
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
			h := auth.AdminAuthMiddleware("test-key", tickets, sessions)(inner)
			req := httptest.NewRequest(http.MethodPost, "/api/der/controls", nil)
			req.RemoteAddr = tc.remote
			if tc.bearer != "" {
				req.Header.Set("Authorization", "Bearer "+tc.bearer)
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
