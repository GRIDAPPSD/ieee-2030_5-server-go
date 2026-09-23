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

// #579, #631: RequireRealCredential is the gate the certificate and
// traffic-capture routes wrap around AdminAuthMiddleware. These tests drive
// the two chained together, the same order admin_router.go wires them in.

func chainedHandler(adminKey string, tickets *auth.TicketStore, sessions *auth.SessionStore, reached *bool) http.Handler {
	inner := http.HandlerFunc(func(http.ResponseWriter, *http.Request) { *reached = true })
	return auth.AdminAuthMiddleware(adminKey, tickets, sessions)(auth.RequireRealCredential(adminKey, tickets, sessions)(inner))
}

// TestRequireRealCredentialRefusesBypassOnly is acceptance criterion 1: a
// request admitted only by the loopback bypass is refused, with the same
// refusal shape as an ordinary unauthenticated request (no route-existence
// disclosure).
func TestRequireRealCredentialRefusesBypassOnly(t *testing.T) {
	tickets := auth.NewTicketStore(30 * time.Second)
	sessions := auth.NewSessionStore(testSessionIdle, testSessionAbsolute)
	var reached bool
	handler := chainedHandler("test-key", tickets, sessions, &reached)

	req := httptest.NewRequest(http.MethodGet, "/api/certs/ca", nil)
	req.RemoteAddr = "127.0.0.1:54321"
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if reached {
		t.Fatal("bypass-only request reached the sensitive handler")
	}
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401; body = %q", w.Code, w.Body.String())
	}
	if got, want := w.Body.String(), `{"error":"admin authentication required"}`; got != want {
		t.Errorf("body = %q, want %q", got, want)
	}
}

// TestRequireRealCredentialAdmitsValidBearerFromLoopback is acceptance
// criterion 2: "a valid credential from a loopback address still succeeds".
// Path 0 already admitted this request without looking at the header;
// RequireRealCredential is what actually checks it.
func TestRequireRealCredentialAdmitsValidBearerFromLoopback(t *testing.T) {
	tickets := auth.NewTicketStore(30 * time.Second)
	sessions := auth.NewSessionStore(testSessionIdle, testSessionAbsolute)
	var reached bool
	handler := chainedHandler("test-key", tickets, sessions, &reached)

	req := httptest.NewRequest(http.MethodGet, "/api/certs/ca", nil)
	req.RemoteAddr = "127.0.0.1:54321"
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if !reached {
		t.Fatalf("valid bearer from loopback was refused: status = %d, body = %q", w.Code, w.Body.String())
	}
}

// TestRequireRealCredentialAdmitsValidMTLSFromLoopback is the mTLS half of
// criterion 2, the other credential kind #579 names explicitly.
func TestRequireRealCredentialAdmitsValidMTLSFromLoopback(t *testing.T) {
	tickets := auth.NewTicketStore(30 * time.Second)
	sessions := auth.NewSessionStore(testSessionIdle, testSessionAbsolute)
	var reached bool
	handler := chainedHandler("test-key", tickets, sessions, &reached)

	req := httptest.NewRequest(http.MethodGet, "/api/certs/ca", nil)
	req.RemoteAddr = "127.0.0.1:54321"
	req.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{generateAdminCert(t)}}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if !reached {
		t.Fatalf("valid mTLS admin cert from loopback was refused: status = %d, body = %q", w.Code, w.Body.String())
	}
}

// TestRequireRealCredentialConsumesTicketExactlyOnce guards the design's
// sharpest edge: Path 0 never touches the query-param ticket, so
// RequireRealCredential's own recheck must be the sole redemption. A double
// redemption would make the ticket single-use in name only; a request that
// never redeems it would leave a stale, guessable ticket alive.
func TestRequireRealCredentialConsumesTicketExactlyOnce(t *testing.T) {
	tickets := auth.NewTicketStore(30 * time.Second)
	sessions := auth.NewSessionStore(testSessionIdle, testSessionAbsolute)
	ticket, err := tickets.Issue()
	if err != nil {
		t.Fatal(err)
	}

	var reached bool
	handler := chainedHandler("test-key", tickets, sessions, &reached)

	req := httptest.NewRequest(http.MethodGet, "/api/traffic/stream?ticket="+ticket, nil)
	req.RemoteAddr = "127.0.0.1:54321"
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if !reached {
		t.Fatalf("valid ticket from loopback was refused: status = %d, body = %q", w.Code, w.Body.String())
	}
	if tickets.Len() != 0 {
		t.Errorf("ticket store has %d entries after redemption, want 0", tickets.Len())
	}
	if tickets.Redeem(ticket) {
		t.Error("the ticket redeemed a second time: it must be single-use")
	}
}

// TestRequireRealCredentialRefusesWrongBearerFromLoopback is the control
// showing the refusal test above can actually fail: a WRONG credential must
// still be refused, not merely "any credential-shaped header".
func TestRequireRealCredentialRefusesWrongBearerFromLoopback(t *testing.T) {
	tickets := auth.NewTicketStore(30 * time.Second)
	sessions := auth.NewSessionStore(testSessionIdle, testSessionAbsolute)
	var reached bool
	handler := chainedHandler("test-key", tickets, sessions, &reached)

	req := httptest.NewRequest(http.MethodGet, "/api/certs/ca", nil)
	req.RemoteAddr = "127.0.0.1:54321"
	req.Header.Set("Authorization", "Bearer totally-wrong-key")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if reached {
		t.Fatal("wrong bearer from loopback reached the sensitive handler")
	}
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401; body = %q", w.Code, w.Body.String())
	}
}

// TestRequireRealCredentialPassesThroughNonBypassAdmission is the case where
// AdmittedByLoopbackBypassOnly is false because AdminAuthMiddleware itself
// already validated a credential (non-loopback address): RequireRealCredential
// must not re-run credentialAdmits, or a one-time ticket presented this way
// would be silently consumed twice.
func TestRequireRealCredentialPassesThroughNonBypassAdmission(t *testing.T) {
	tickets := auth.NewTicketStore(30 * time.Second)
	sessions := auth.NewSessionStore(testSessionIdle, testSessionAbsolute)
	ticket, err := tickets.Issue()
	if err != nil {
		t.Fatal(err)
	}

	var reached bool
	handler := chainedHandler("test-key", tickets, sessions, &reached)

	// No RemoteAddr override: httptest's default (192.0.2.1) is non-loopback,
	// so AdminAuthMiddleware's own credentialAdmits call redeems the ticket.
	req := httptest.NewRequest(http.MethodGet, "/api/traffic/stream?ticket="+ticket, nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if !reached {
		t.Fatalf("valid ticket from a non-loopback address was refused: status = %d, body = %q", w.Code, w.Body.String())
	}
	if tickets.Len() != 0 {
		t.Errorf("ticket store has %d entries after redemption, want 0", tickets.Len())
	}
}
