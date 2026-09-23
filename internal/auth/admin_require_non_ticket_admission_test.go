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

// #641: RequireNonTicketAdmission is the ticket-mint route's own extra gate,
// wired after AdminAuthMiddleware and RequireRealCredential have already
// established that a real credential admitted the request. These tests
// drive all three chained together, the order admin_router.go wires the
// ticket route in.

func mintChain(adminKey string, tickets *auth.TicketStore, sessions *auth.SessionStore, reached *bool) http.Handler {
	inner := http.HandlerFunc(func(http.ResponseWriter, *http.Request) { *reached = true })
	return auth.AdminAuthMiddleware(adminKey, tickets, sessions)(
		auth.RequireRealCredential(adminKey, tickets, sessions)(
			auth.RequireNonTicketAdmission(inner),
		),
	)
}

// TestRequireNonTicketAdmissionRefusesTicketOnlyFromNonLoopback is #641's
// core case: AdminAuthMiddleware itself admits the ticket (Path C, the
// caller is not loopback), so RequireRealCredential's recheck never runs -
// RequireNonTicketAdmission is the only thing standing between the ticket
// and its own successor.
func TestRequireNonTicketAdmissionRefusesTicketOnlyFromNonLoopback(t *testing.T) {
	tickets := auth.NewTicketStore(30 * time.Second)
	sessions := auth.NewSessionStore(testSessionIdle, testSessionAbsolute)
	ticket, err := tickets.Issue()
	if err != nil {
		t.Fatal(err)
	}

	var reached bool
	handler := mintChain("test-key", tickets, sessions, &reached)

	req := httptest.NewRequest(http.MethodPost, "/auth/ticket?ticket="+ticket, nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if reached {
		t.Fatal("a ticket-only request minted a successor ticket")
	}
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401; body = %q", w.Code, w.Body.String())
	}
}

// TestRequireNonTicketAdmissionRefusesTicketOnlyFromLoopback is the same
// case reached through RequireRealCredential's bypass-only recheck instead:
// Path 0 admits a loopback caller with no credential consulted, so the
// ticket is found (and consumed) by the recheck, not by AdminAuthMiddleware.
// RequireNonTicketAdmission must still see it as a ticket admission, not as
// an ordinary "some real credential, kind unknown" pass-through.
func TestRequireNonTicketAdmissionRefusesTicketOnlyFromLoopback(t *testing.T) {
	tickets := auth.NewTicketStore(30 * time.Second)
	sessions := auth.NewSessionStore(testSessionIdle, testSessionAbsolute)
	ticket, err := tickets.Issue()
	if err != nil {
		t.Fatal(err)
	}

	var reached bool
	handler := mintChain("test-key", tickets, sessions, &reached)

	req := httptest.NewRequest(http.MethodPost, "/auth/ticket?ticket="+ticket, nil)
	req.RemoteAddr = "127.0.0.1:54321"
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if reached {
		t.Fatal("a ticket-only request minted a successor ticket")
	}
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401; body = %q", w.Code, w.Body.String())
	}
	if tickets.Len() != 0 {
		t.Errorf("ticket store has %d entries after the recheck consumed it, want 0", tickets.Len())
	}
}

// TestRequireNonTicketAdmissionAdmitsEveryOtherCredential is the control:
// every credential kind besides a ticket must still reach the mint handler,
// proving RequireNonTicketAdmission is a targeted refusal and not a general
// one. Loopback RemoteAddr throughout, so mTLS and Bearer are also proven
// through RequireRealCredential's own recheck path, the harder of the two
// AdmittedViaTicket must get right.
func TestRequireNonTicketAdmissionAdmitsEveryOtherCredential(t *testing.T) {
	tickets := auth.NewTicketStore(30 * time.Second)
	sessions := auth.NewSessionStore(testSessionIdle, testSessionAbsolute)
	sessionID, err := sessions.Issue()
	if err != nil {
		t.Fatal(err)
	}
	adminCert := generateAdminCert(t)

	cases := []struct {
		name  string
		build func(*http.Request)
	}{
		{"bearer", func(r *http.Request) { r.Header.Set("Authorization", "Bearer test-key") }},
		{"mtls", func(r *http.Request) {
			r.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{adminCert}}
		}},
		{"cookie session", func(r *http.Request) {
			r.AddCookie(&http.Cookie{Name: auth.AdminTicketCookieName, Value: sessionID})
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var reached bool
			handler := mintChain("test-key", tickets, sessions, &reached)

			req := httptest.NewRequest(http.MethodPost, "/auth/ticket", nil)
			req.RemoteAddr = "127.0.0.1:54321"
			tc.build(req)
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, req)

			if !reached {
				t.Fatalf("%s: refused, status = %d, body = %q", tc.name, w.Code, w.Body.String())
			}
		})
	}
}

// TestTicketAlongsideEachCredentialAdmitsByTheOtherCredential is #641 fix
// round 1, item 3: credentialAdmits checks the ticket last (admin.go), so a
// caller who presents a ticket ALONGSIDE mTLS, a Bearer token, or a cookie
// session is admitted by that other credential and the ticket is left
// unspent, rather than the outcome depending on which of the two the
// credential chain happened to reach first. Before that ordering, a cookie
// plus a ticket was refused here (the ticket admitted, then
// RequireNonTicketAdmission refused it) while Bearer plus a ticket was not,
// so the same caller got a different answer depending only on which
// credential they also happened to be carrying.
func TestTicketAlongsideEachCredentialAdmitsByTheOtherCredential(t *testing.T) {
	tickets := auth.NewTicketStore(30 * time.Second)
	sessions := auth.NewSessionStore(testSessionIdle, testSessionAbsolute)
	sessionID, err := sessions.Issue()
	if err != nil {
		t.Fatal(err)
	}
	adminCert := generateAdminCert(t)

	cases := []struct {
		name  string
		build func(*http.Request)
	}{
		{"bearer", func(r *http.Request) { r.Header.Set("Authorization", "Bearer test-key") }},
		{"mtls", func(r *http.Request) {
			r.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{adminCert}}
		}},
		{"cookie session", func(r *http.Request) {
			r.AddCookie(&http.Cookie{Name: auth.AdminTicketCookieName, Value: sessionID})
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ticket, err := tickets.Issue()
			if err != nil {
				t.Fatal(err)
			}

			var reached bool
			handler := mintChain("test-key", tickets, sessions, &reached)

			req := httptest.NewRequest(http.MethodPost, "/auth/ticket?ticket="+ticket, nil)
			req.RemoteAddr = "127.0.0.1:54321"
			tc.build(req)
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, req)

			if !reached {
				t.Fatalf("%s plus a ticket: refused, status = %d, body = %q; want the other credential to admit", tc.name, w.Code, w.Body.String())
			}
			if w.Code != http.StatusOK {
				t.Fatalf("%s plus a ticket: status = %d, want 200", tc.name, w.Code)
			}
			// The presented ticket must still be spendable: it was not the
			// credential that admitted this request.
			if !tickets.Redeem(ticket) {
				t.Errorf("%s plus a ticket: the presented ticket was consumed even though %s admitted the request", tc.name, tc.name)
			}
		})
	}
}
