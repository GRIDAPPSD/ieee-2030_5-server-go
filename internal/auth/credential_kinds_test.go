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

// The query-param ticket (Path C) and the admin_ticket cookie session (Path
// D) are different credential kinds held in different stores. A ticket value
// reaches durable places a session value never does (browser history,
// autocomplete, an outbound Referer, the access log), so a value harvested
// from any of them must not be usable as a session, and a cookie value must
// not be usable in a URL.

func adminTLSState(cert *x509.Certificate) *tls.ConnectionState {
	return &tls.ConnectionState{PeerCertificates: []*x509.Certificate{cert}}
}

func TestQueryTicketNotAcceptedAsCookieSession(t *testing.T) {
	tickets := auth.NewTicketStore(30 * time.Second)
	sessions := auth.NewSessionStore(testSessionIdle, testSessionAbsolute)

	ticket, err := tickets.Issue()
	if err != nil {
		t.Fatal(err)
	}

	handler := auth.AdminAuthMiddleware("test-key", tickets, sessions)(okHandler())
	req := httptest.NewRequest(http.MethodGet, "/api/devices", nil)
	req.AddCookie(&http.Cookie{Name: auth.AdminTicketCookieName, Value: ticket})
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("a query ticket presented as the %s cookie: status = %d, want 401", auth.AdminTicketCookieName, w.Code)
	}
	if !tickets.Redeem(ticket) {
		t.Error("the refused cookie attempt consumed the query ticket: the two stores are not separate")
	}
	if sessions.Len() != 0 {
		t.Errorf("session count = %d, want 0 (a refused cookie must not mint a session)", sessions.Len())
	}
}

func TestCookieSessionNotAcceptedAsQueryTicket(t *testing.T) {
	tickets := auth.NewTicketStore(30 * time.Second)
	sessions := auth.NewSessionStore(testSessionIdle, testSessionAbsolute)

	id, err := sessions.Issue()
	if err != nil {
		t.Fatal(err)
	}

	handler := auth.AdminAuthMiddleware("test-key", tickets, sessions)(okHandler())
	req := httptest.NewRequest(http.MethodGet, "/api/devices?ticket="+id, nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("a cookie session id presented as ?ticket=: status = %d, want 401", w.Code)
	}
	if !sessions.Validate(id) {
		t.Error("the refused ?ticket= attempt invalidated the cookie session: the two stores are not separate")
	}
	if tickets.Len() != 0 {
		t.Errorf("ticket count = %d, want 0 (a refused query ticket must not mint a ticket)", tickets.Len())
	}
}

// TestQueryTicketRemainsSingleUse guards the half of the split that must NOT
// change: the URL-borne credential is still consumed on presentation.
func TestQueryTicketRemainsSingleUse(t *testing.T) {
	tickets := auth.NewTicketStore(30 * time.Second)
	sessions := auth.NewSessionStore(testSessionIdle, testSessionAbsolute)

	ticket, err := tickets.Issue()
	if err != nil {
		t.Fatal(err)
	}

	handler := auth.AdminAuthMiddleware("test-key", tickets, sessions)(okHandler())
	want := []int{http.StatusOK, http.StatusUnauthorized}
	for i, wantCode := range want {
		req := httptest.NewRequest(http.MethodGet, "/api/devices?ticket="+ticket, nil)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)

		if w.Code != wantCode {
			t.Errorf("query ticket presentation %d of %d: status = %d, want %d", i+1, len(want), w.Code, wantCode)
		}
	}
	if tickets.Len() != 0 {
		t.Errorf("ticket count after a successful redemption = %d, want 0 (the ticket must be consumed)", tickets.Len())
	}
}

// TestAdminAuthAllFiveAdmissionPathsReachable walks every admission path in
// its documented order plus the refusal at the end, so a change that
// silently drops or reorders one shows up here rather than in a browser.
func TestAdminAuthAllFiveAdmissionPathsReachable(t *testing.T) {
	adminCert := generateAdminCert(t)

	cases := []struct {
		name    string
		prepare func(t *testing.T, tickets *auth.TicketStore, sessions *auth.SessionStore) *http.Request
		want    int
	}{
		{
			name: "path 0 loopback bypass",
			prepare: func(_ *testing.T, _ *auth.TicketStore, _ *auth.SessionStore) *http.Request {
				req := httptest.NewRequest(http.MethodGet, "/api/devices", nil)
				req.RemoteAddr = "127.0.0.1:54321"
				return req
			},
			want: http.StatusOK,
		},
		{
			name: "path 0 declines when a forwarded header is present",
			prepare: func(_ *testing.T, _ *auth.TicketStore, _ *auth.SessionStore) *http.Request {
				req := httptest.NewRequest(http.MethodGet, "/api/devices", nil)
				req.RemoteAddr = "127.0.0.1:54321"
				req.Header.Set("X-Forwarded-For", "203.0.113.5")
				return req
			},
			want: http.StatusUnauthorized,
		},
		{
			name: "path A mTLS with the admin policy OID",
			prepare: func(_ *testing.T, _ *auth.TicketStore, _ *auth.SessionStore) *http.Request {
				req := httptest.NewRequest(http.MethodGet, "/api/devices", nil)
				req.TLS = adminTLSState(adminCert)
				return req
			},
			want: http.StatusOK,
		},
		{
			name: "path B bearer token",
			prepare: func(_ *testing.T, _ *auth.TicketStore, _ *auth.SessionStore) *http.Request {
				req := httptest.NewRequest(http.MethodGet, "/api/devices", nil)
				req.Header.Set("Authorization", "Bearer test-key")
				return req
			},
			want: http.StatusOK,
		},
		{
			name: "path C one-time query ticket",
			prepare: func(t *testing.T, tickets *auth.TicketStore, _ *auth.SessionStore) *http.Request {
				ticket, err := tickets.Issue()
				if err != nil {
					t.Fatal(err)
				}
				return httptest.NewRequest(http.MethodGet, "/api/devices?ticket="+ticket, nil)
			},
			want: http.StatusOK,
		},
		{
			name: "path D cookie session",
			prepare: func(t *testing.T, _ *auth.TicketStore, sessions *auth.SessionStore) *http.Request {
				id, err := sessions.Issue()
				if err != nil {
					t.Fatal(err)
				}
				req := httptest.NewRequest(http.MethodGet, "/api/devices", nil)
				req.AddCookie(&http.Cookie{Name: auth.AdminTicketCookieName, Value: id})
				return req
			},
			want: http.StatusOK,
		},
		{
			name: "no path satisfied",
			prepare: func(_ *testing.T, _ *auth.TicketStore, _ *auth.SessionStore) *http.Request {
				return httptest.NewRequest(http.MethodGet, "/api/devices", nil)
			},
			want: http.StatusUnauthorized,
		},
	}

	if got, want := len(cases), 7; got != want {
		t.Fatalf("admission-path table has %d cases, want %d (five paths, the Path 0 decline, and the refusal)", got, want)
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tickets := auth.NewTicketStore(30 * time.Second)
			sessions := auth.NewSessionStore(testSessionIdle, testSessionAbsolute)
			handler := auth.AdminAuthMiddleware("test-key", tickets, sessions)(okHandler())

			req := tc.prepare(t, tickets, sessions)
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, req)

			if w.Code != tc.want {
				t.Errorf("status = %d, want %d; body = %q", w.Code, tc.want, w.Body.String())
			}
		})
	}
}
