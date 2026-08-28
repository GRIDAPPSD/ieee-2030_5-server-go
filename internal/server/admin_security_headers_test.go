package server_test

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/config"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/server"
)

// #413: the admin plane sets no framing, sniff, or referrer response
// headers, so a loopback deployment's dashboard (fully admitted,
// credential-free, by the Path 0 loopback bypass) can be framed and clicked
// from another origin. These headers are asserted on VALUE across every
// admin response shape, read from the client's own Result(), never on mere
// presence: a permissive value is present and useless.

const (
	wantAdminCSP      = "frame-ancestors 'none'"
	wantAdminXFO      = "DENY"
	wantAdminNoSniff  = "nosniff"
	wantAdminReferrer = "no-referrer"
)

func assertAdminSecurityHeaders(t *testing.T, h http.Header, shape string) {
	t.Helper()
	if got := h.Get("Content-Security-Policy"); got != wantAdminCSP {
		t.Errorf("%s: Content-Security-Policy = %q, want %q", shape, got, wantAdminCSP)
	}
	if got := h.Get("X-Frame-Options"); got != wantAdminXFO {
		t.Errorf("%s: X-Frame-Options = %q, want %q", shape, got, wantAdminXFO)
	}
	if got := h.Get("X-Content-Type-Options"); got != wantAdminNoSniff {
		t.Errorf("%s: X-Content-Type-Options = %q, want %q", shape, got, wantAdminNoSniff)
	}
	if got := h.Get("Referrer-Policy"); got != wantAdminReferrer {
		t.Errorf("%s: Referrer-Policy = %q, want %q", shape, got, wantAdminReferrer)
	}
}

// TestAdminSecurityHeadersOnEveryResponseShape covers six response shapes:
// a normal 200, the wrong-key login render, the 421 host-gate refusal, the
// 303 navigation refusal, the 401 API refusal, and an authenticated
// cookie-session 200. All six go through the real router, never the handler
// source, and are read from Result(), the only view a network client ever
// sees.
func TestAdminSecurityHeadersOnEveryResponseShape(t *testing.T) {
	router, _ := newUIRouter(t)
	jsPath, _ := builtAssetPaths(t)
	cookie := loginForCookie(t, router)

	wrongKeyReq := newUIRequest(t, http.MethodPost, "/auth/login", url.Values{"key": []string{"wrong"}}.Encode())

	hostGateReq := newUIRequest(t, http.MethodGet, "/login", "")
	hostGateReq.Host = "evil.example.net"

	navigationReq := newUIRequest(t, http.MethodGet, "/", "")
	navigationReq.Header.Set("Sec-Fetch-Dest", "document")
	navigationReq.Header.Set("Accept", browserNavigationAccept)

	apiRefusalReq := newUIRequest(t, http.MethodGet, jsPath, "")
	apiRefusalReq.Header.Set("Sec-Fetch-Dest", "script")
	apiRefusalReq.Header.Set("Accept", "*/*")

	cookieReq := newUIRequest(t, http.MethodGet, "/ui/", "")
	cookieReq.AddCookie(cookie)

	cases := []struct {
		name     string
		req      *http.Request
		wantCode int
	}{
		{name: "normal 200 (GET /login)", req: newUIRequest(t, http.MethodGet, "/login", ""), wantCode: http.StatusOK},
		{name: "wrong-key login render", req: wrongKeyReq, wantCode: http.StatusOK},
		{name: "421 host-gate refusal", req: hostGateReq, wantCode: http.StatusMisdirectedRequest},
		{name: "303 navigation refusal", req: navigationReq, wantCode: http.StatusSeeOther},
		{name: "401 API refusal", req: apiRefusalReq, wantCode: http.StatusUnauthorized},
		{name: "authenticated cookie-session 200", req: cookieReq, wantCode: http.StatusOK},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, tc.req)
			resp := rec.Result()
			if resp.StatusCode != tc.wantCode {
				t.Fatalf("status = %d, want %d; body = %q", resp.StatusCode, tc.wantCode, rec.Body.String())
			}
			assertAdminSecurityHeaders(t, resp.Header, tc.name)
		})
	}
}

// TestAdminSecurityHeadersOnLoopbackAdmittedRequest covers the exact
// combination the clickjacking finding turns on: RemoteAddr loopback and
// Host 127.0.0.1, which the host allowlist legitimately passes and the Path
// 0 loopback bypass legitimately admits credential-free. That combination
// must still refuse framing; a test that only covers a non-loopback origin
// has not covered the finding (#413).
func TestAdminSecurityHeadersOnLoopbackAdmittedRequest(t *testing.T) {
	router, _ := server.BuildAdminRouter("", nil, newTestStores(), "GCM", nil, nil, server.DefaultAdminAllowedHosts(), false)

	req := httptest.NewRequest(http.MethodGet, "/ui/", nil)
	req.RemoteAddr = "127.0.0.1:54321"
	req.Host = "127.0.0.1"
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	resp := rec.Result()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /ui/ from loopback with Host 127.0.0.1: status = %d, want 200; body = %q", resp.StatusCode, rec.Body.String())
	}
	assertAdminSecurityHeaders(t, resp.Header, "loopback-admitted GET /ui/")
}

// TestProtocolPlaneCarriesNoAdminSecurityHeaders is the scope proof: the
// admin router's security-header wrapper must not reach the device-facing
// protocol listener, whose bytes are a conformance surface (#413).
//
// X-Content-Type-Options is deliberately excluded from this check. A
// certless GET /dcap answers 403 via http.Error, and net/http's own
// http.Error (like http.NotFound) sets nosniff on every response it writes,
// on every mux, independent of this change or of which plane served it. It
// is therefore not a signal that the admin wrapper leaked here; the other
// three headers are set nowhere but that wrapper, so they are the actual
// scope proof.
func TestProtocolPlaneCarriesNoAdminSecurityHeaders(t *testing.T) {
	router, _ := server.BuildProtocolRouter(&config.Config{}, newTestStores(), nil, "test-sfdi", "test-lfdi", nil)

	req := httptest.NewRequest(http.MethodGet, "/dcap", nil)
	req.RemoteAddr = "127.0.0.1:54321"
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	resp := rec.Result()

	for _, header := range []string{"Content-Security-Policy", "X-Frame-Options", "Referrer-Policy"} {
		if got := resp.Header.Get(header); got != "" {
			t.Errorf("protocol GET /dcap: %s = %q, want unset; the admin plane's security headers must not reach the device-facing wire", header, got)
		}
	}
}
