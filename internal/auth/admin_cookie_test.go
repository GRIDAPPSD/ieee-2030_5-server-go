package auth_test

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-go/internal/auth"
)

// IEEE-095: cookie-based ticket auth (Path D in AdminAuthMiddleware).
//
// The login form sets an HttpOnly + Secure + SameSite=Strict cookie named
// admin_ticket whose value is a TicketStore ticket. Each request through the
// middleware redeems the cookie (one-time-use, same as the query-param path)
// and on success the middleware MUST re-issue a fresh ticket and re-set the
// cookie so multi-request page navigation under one-time-use semantics still
// works for a browser session.

func TestAdminAuthCookieTicketValid(t *testing.T) {
	tickets := auth.NewTicketStore(30 * time.Second)
	ticket, err := tickets.Issue()
	if err != nil {
		t.Fatal(err)
	}

	handler := auth.AdminAuthMiddleware("test-key", tickets)(okHandler())
	req := httptest.NewRequest(http.MethodGet, "/api/devices", nil)
	req.AddCookie(&http.Cookie{Name: auth.AdminTicketCookieName, Value: ticket})

	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("valid cookie ticket should be authorized, got %d", w.Code)
	}
}

func TestAdminAuthCookieTicketReissuesFreshCookie(t *testing.T) {
	tickets := auth.NewTicketStore(30 * time.Second)
	ticket, err := tickets.Issue()
	if err != nil {
		t.Fatal(err)
	}

	handler := auth.AdminAuthMiddleware("test-key", tickets)(okHandler())
	req := httptest.NewRequest(http.MethodGet, "/api/devices", nil)
	req.AddCookie(&http.Cookie{Name: auth.AdminTicketCookieName, Value: ticket})

	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	// The response must include a fresh Set-Cookie with a new ticket value.
	cookies := w.Result().Cookies()
	var found *http.Cookie
	for _, c := range cookies {
		if c.Name == auth.AdminTicketCookieName {
			found = c
			break
		}
	}
	if found == nil {
		t.Fatalf("expected response Set-Cookie %q, got %v", auth.AdminTicketCookieName, cookies)
	}
	if found.Value == "" || found.Value == ticket {
		t.Errorf("expected re-issued cookie with new value, got %q (original=%q)", found.Value, ticket)
	}
	if !found.HttpOnly {
		t.Error("re-issued cookie must be HttpOnly")
	}
	if found.SameSite != http.SameSiteStrictMode {
		t.Errorf("re-issued cookie SameSite=Strict, got %v", found.SameSite)
	}
}

func TestAdminAuthCookieTicketInvalid(t *testing.T) {
	tickets := auth.NewTicketStore(30 * time.Second)
	handler := auth.AdminAuthMiddleware("test-key", tickets)(okHandler())
	req := httptest.NewRequest(http.MethodGet, "/api/devices", nil)
	req.AddCookie(&http.Cookie{Name: auth.AdminTicketCookieName, Value: "tampered-value"})

	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("invalid cookie ticket should be rejected, got %d", w.Code)
	}
}

func TestAdminAuthCookieTicketExpired(t *testing.T) {
	// 1ms TTL — ticket expires before redemption attempt.
	tickets := auth.NewTicketStore(1 * time.Millisecond)
	ticket, err := tickets.Issue()
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(5 * time.Millisecond)

	handler := auth.AdminAuthMiddleware("test-key", tickets)(okHandler())
	req := httptest.NewRequest(http.MethodGet, "/api/devices", nil)
	req.AddCookie(&http.Cookie{Name: auth.AdminTicketCookieName, Value: ticket})

	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expired cookie ticket should be rejected, got %d", w.Code)
	}
}

func TestAdminAuthCookieTicketEmpty(t *testing.T) {
	tickets := auth.NewTicketStore(30 * time.Second)
	handler := auth.AdminAuthMiddleware("test-key", tickets)(okHandler())
	req := httptest.NewRequest(http.MethodGet, "/api/devices", nil)
	req.AddCookie(&http.Cookie{Name: auth.AdminTicketCookieName, Value: ""})

	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("empty cookie ticket should be rejected, got %d", w.Code)
	}
}
