package auth_test

import (
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/auth"
)

// #159: cookie-based session auth (Path D in AdminAuthMiddleware).
//
// The login form sets the admin_ticket cookie to a SessionStore id, which
// the middleware validates without consuming and without re-setting. See
// AdminAuthMiddleware for why the two ticket kinds have separate stores.
//
// httptest.NewRequest leaves RemoteAddr at 192.0.2.1:1234, which is not
// loopback, so these tests traverse the full auth chain rather than the Path
// 0 bypass. TestAdminAuthNoCredentialNonLoopbackRefused is the control that
// keeps that claim honest.

const (
	testSessionIdle     = 30 * time.Second
	testSessionAbsolute = 5 * time.Minute
)

func isLoopbackAddr(remoteAddr string) bool {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		host = remoteAddr
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func cookieAuthRequest(value string) *http.Request {
	req := httptest.NewRequest(http.MethodGet, "/api/devices", nil)
	if value != "" {
		req.AddCookie(&http.Cookie{Name: auth.AdminTicketCookieName, Value: value})
	}
	return req
}

// TestAdminAuthNoCredentialNonLoopbackRefused is the control for every test
// in this file. A fixture that has fallen back onto the loopback bypass
// looks exactly like a working credential check, so the same request shape
// with no credential at all must be refused.
func TestAdminAuthNoCredentialNonLoopbackRefused(t *testing.T) {
	sessions := auth.NewSessionStore(testSessionIdle, testSessionAbsolute)
	handler := auth.AdminAuthMiddleware("test-key", nil, sessions)(okHandler())

	req := cookieAuthRequest("")
	if isLoopbackAddr(req.RemoteAddr) {
		t.Fatalf("fixture RemoteAddr %q is loopback: the auth chain would be bypassed and this file would prove nothing", req.RemoteAddr)
	}

	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("no credential from %s: status = %d, want 401", req.RemoteAddr, w.Code)
	}
	if got, want := w.Body.String(), `{"error":"admin authentication required"}`; got != want {
		t.Errorf("no-credential body = %q, want %q", got, want)
	}
}

// TestAdminAuthCookieSessionAdmitsSuccessiveRequests is the primary gate for
// the parallel-subresource defect, in its cheapest deterministic form: the
// browser's second request for the same page carries the same cookie.
func TestAdminAuthCookieSessionAdmitsSuccessiveRequests(t *testing.T) {
	sessions := auth.NewSessionStore(testSessionIdle, testSessionAbsolute)
	id, err := sessions.Issue()
	if err != nil {
		t.Fatal(err)
	}

	handler := auth.AdminAuthMiddleware("test-key", nil, sessions)(okHandler())

	for i := 1; i <= 2; i++ {
		req := cookieAuthRequest(id)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("request %d of 2 with the same cookie session: status = %d, want 200", i, w.Code)
		}
	}
	if sessions.Len() != 1 {
		t.Errorf("session count after 2 validations = %d, want 1 (validation must not consume)", sessions.Len())
	}
}

// TestAdminAuthCookieSessionAdmitsConcurrentRequests is the shape a browser
// actually produces: the shell's script, stylesheet and icon requests arrive
// together on one cookie.
func TestAdminAuthCookieSessionAdmitsConcurrentRequests(t *testing.T) {
	sessions := auth.NewSessionStore(testSessionIdle, testSessionAbsolute)
	id, err := sessions.Issue()
	if err != nil {
		t.Fatal(err)
	}

	handler := auth.AdminAuthMiddleware("test-key", nil, sessions)(okHandler())

	const n = 8
	codes := make([]int, n)
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(i int) {
			defer wg.Done()
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, cookieAuthRequest(id))
			codes[i] = w.Code
		}(i)
	}
	wg.Wait()

	for i, code := range codes {
		if code != http.StatusOK {
			t.Errorf("concurrent request %d of %d: status = %d, want 200", i, n, code)
		}
	}
}

// TestAdminAuthCookieSessionSetsNoCookie pins the removal of the
// per-request re-issue. A rotation that returns silently would reintroduce
// the race where a browser keeps a value the server has already retired.
func TestAdminAuthCookieSessionSetsNoCookie(t *testing.T) {
	sessions := auth.NewSessionStore(testSessionIdle, testSessionAbsolute)
	id, err := sessions.Issue()
	if err != nil {
		t.Fatal(err)
	}

	handler := auth.AdminAuthMiddleware("test-key", nil, sessions)(okHandler())
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, cookieAuthRequest(id))

	if w.Code != http.StatusOK {
		t.Fatalf("valid cookie session: status = %d, want 200", w.Code)
	}
	for _, c := range w.Result().Cookies() {
		if c.Name == auth.AdminTicketCookieName {
			t.Errorf("middleware set %s=%q; only the login handler may set this cookie", c.Name, c.Value)
		}
	}
	if h := w.Header().Values("Set-Cookie"); len(h) != 0 {
		t.Errorf("Set-Cookie headers on an authenticated response = %v, want none", h)
	}
}

func TestAdminAuthCookieSessionValid(t *testing.T) {
	sessions := auth.NewSessionStore(testSessionIdle, testSessionAbsolute)
	id, err := sessions.Issue()
	if err != nil {
		t.Fatal(err)
	}

	handler := auth.AdminAuthMiddleware("test-key", nil, sessions)(okHandler())
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, cookieAuthRequest(id))

	if w.Code != http.StatusOK {
		t.Errorf("valid cookie session should be authorized, got %d", w.Code)
	}
}

func TestAdminAuthCookieSessionRefusals(t *testing.T) {
	cases := []struct {
		name  string
		value string
	}{
		{"tampered value", "tampered-value"},
		{"empty value", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sessions := auth.NewSessionStore(testSessionIdle, testSessionAbsolute)
			handler := auth.AdminAuthMiddleware("test-key", nil, sessions)(okHandler())

			req := httptest.NewRequest(http.MethodGet, "/api/devices", nil)
			req.AddCookie(&http.Cookie{Name: auth.AdminTicketCookieName, Value: tc.value})
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, req)

			if w.Code != http.StatusUnauthorized {
				t.Errorf("%s cookie should be rejected, got %d", tc.name, w.Code)
			}
		})
	}
}

func TestAdminAuthCookieSessionExpired(t *testing.T) {
	sessions := auth.NewSessionStore(1*time.Millisecond, 5*time.Minute)
	id, err := sessions.Issue()
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(5 * time.Millisecond)

	handler := auth.AdminAuthMiddleware("test-key", nil, sessions)(okHandler())
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, cookieAuthRequest(id))

	if w.Code != http.StatusUnauthorized {
		t.Errorf("idle-expired cookie session should be rejected, got %d", w.Code)
	}
}

// TestAdminAuthCookieSessionDisabledWhenNil pins that a nil session store
// disables Path D rather than admitting on it.
func TestAdminAuthCookieSessionDisabledWhenNil(t *testing.T) {
	handler := auth.AdminAuthMiddleware("test-key", nil, nil)(okHandler())
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, cookieAuthRequest("any-value"))

	if w.Code != http.StatusUnauthorized {
		t.Errorf("cookie auth with a nil session store should be rejected, got %d", w.Code)
	}
}
