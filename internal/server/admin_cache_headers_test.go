package server_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/auth"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/config"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/server"
)

// The admin plane's answer to one URL depends on the request headers that
// decide whether a refusal is a redirect or a status, and a cookie-authenticated
// GET gets none of the shared-cache suppression RFC 9111 section 3.5 gives an
// Authorization-bearing one. Both halves are asserted on the VALUE: a Vary
// naming the wrong headers is present and useless, and a Cache-Control of
// "no-cache" still permits storage.

const (
	wantAdminVary      = "Sec-Fetch-Dest, Accept"
	wantAdminNoStore   = "no-store"
	protocolPatternsAt = 72
)

// TestAdminRefusalCarriesVary covers both refusal branches: the negotiated
// answer names what it negotiated on.
func TestAdminRefusalCarriesVary(t *testing.T) {
	router, _ := newUIRouter(t)
	jsPath, _ := builtAssetPaths(t)

	for _, tc := range []struct {
		name     string
		path     string
		dest     string
		wantCode int
	}{
		{name: "navigation redirected to login", path: "/", dest: "document", wantCode: http.StatusSeeOther},
		{name: "subresource given the status", path: jsPath, dest: "script", wantCode: http.StatusUnauthorized},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := newUIRequest(t, http.MethodGet, tc.path, "")
			req.Header.Set("Sec-Fetch-Dest", tc.dest)
			req.Header.Set("Accept", browserNavigationAccept)
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)

			if rec.Code != tc.wantCode {
				t.Fatalf("GET %s as %s: status = %d, want %d", tc.path, tc.dest, rec.Code, tc.wantCode)
			}
			if got := rec.Header().Get("Vary"); got != wantAdminVary {
				t.Errorf("GET %s as %s: Vary = %q, want %q; a cache keyed on the wrong headers may replay this answer to the other consumer",
					tc.path, tc.dest, got, wantAdminVary)
			}
			if got := rec.Header().Get("Cache-Control"); got != wantAdminNoStore {
				t.Errorf("GET %s as %s: Cache-Control = %q, want %q", tc.path, tc.dest, got, wantAdminNoStore)
			}
		})
	}
}

// TestAdminVaryNamesEveryNegotiatedHeader pins the value to the headers
// wantsLoginPage actually reads. A header added to that decision without being
// added here is a cache key the intermediary does not know about.
func TestAdminVaryNamesEveryNegotiatedHeader(t *testing.T) {
	if auth.AdminRefusalVary != wantAdminVary {
		t.Fatalf("auth.AdminRefusalVary = %q, want %q", auth.AdminRefusalVary, wantAdminVary)
	}
	for _, header := range []string{"Sec-Fetch-Dest", "Accept"} {
		if !containsHeaderName(auth.AdminRefusalVary, header) {
			t.Errorf("Vary %q does not name %q, which the refusal decision reads", auth.AdminRefusalVary, header)
		}
	}
}

func containsHeaderName(vary, name string) bool {
	for _, part := range splitAndTrim(vary) {
		if equalFoldASCII(part, name) {
			return true
		}
	}
	return false
}

// TestAdminAuthenticatedResponsesAreNotStorable covers the shell, an asset, the
// SSE-adjacent JSON read and the API: an intermediary must store none of them.
func TestAdminAuthenticatedResponsesAreNotStorable(t *testing.T) {
	router, _ := newUIRouter(t)
	cookie := loginForCookie(t, router)
	jsPath, cssPath := builtAssetPaths(t)

	for _, path := range []string{"/", "/ui/", jsPath, cssPath, "/ui/favicon.svg", "/dashboard/data", "/api/topology"} {
		t.Run(path, func(t *testing.T) {
			req := newUIRequest(t, http.MethodGet, path, "")
			req.AddCookie(cookie)
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("GET %s on the session cookie: status = %d, want 200; body = %q", path, rec.Code, rec.Body.String())
			}
			if got := rec.Header().Get("Cache-Control"); got != wantAdminNoStore {
				t.Errorf("GET %s: Cache-Control = %q, want %q; a cookie-authenticated response carries no implicit shared-cache suppression",
					path, got, wantAdminNoStore)
			}
		})
	}
}

// TestAdminPlaneRefusalsBeforeAuthAreNotStorable reaches the two answers the
// host gate produces ahead of the credential chain, plus the public login page.
func TestAdminPlaneRefusalsBeforeAuthAreNotStorable(t *testing.T) {
	router, _ := newUIRouter(t)

	for _, tc := range []struct {
		name     string
		host     string
		wantCode int
	}{
		{name: "disallowed host", host: "evil.example.net", wantCode: http.StatusMisdirectedRequest},
		{name: "allowed host reaches the login page", host: testUIHost, wantCode: http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := newUIRequest(t, http.MethodGet, "/login", "")
			req.Host = tc.host
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)

			if rec.Code != tc.wantCode {
				t.Fatalf("GET /login with Host %q: status = %d, want %d", tc.host, rec.Code, tc.wantCode)
			}
			if got := rec.Header().Get("Cache-Control"); got != wantAdminNoStore {
				t.Errorf("GET /login with Host %q: Cache-Control = %q, want %q", tc.host, got, wantAdminNoStore)
			}
		})
	}
}

// TestProtocolPlaneCarriesNoAdminCacheHeaders is the scope proof. The admin
// directives are added in BuildAdminRouter; the protocol listener's bytes are a
// conformance surface and must be untouched, route set included.
func TestProtocolPlaneCarriesNoAdminCacheHeaders(t *testing.T) {
	router, patterns := server.BuildProtocolRouter(&config.Config{}, newTestStores(), nil, "test-sfdi", "test-lfdi", nil)

	if len(patterns) != protocolPatternsAt {
		t.Errorf("protocol router mounts %d patterns, want %d; this test's header claim covers a route set that has changed",
			len(patterns), protocolPatternsAt)
	}

	for _, path := range []string{"/dcap", "/tm", "/edev", "/sdev", "/mup"} {
		t.Run(path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, path, nil)
			req.RemoteAddr = "127.0.0.1:54321"
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)

			for _, header := range []string{"Cache-Control", "Vary", "Expires"} {
				if got := rec.Header().Get(header); got != "" {
					t.Errorf("protocol GET %s: %s = %q, want unset; the admin plane's cache directives must not reach the device-facing wire",
						path, header, got)
				}
			}
		})
	}
}

// splitAndTrim and equalFoldASCII keep the Vary parse in the test independent of
// the production helper it checks.
func splitAndTrim(csv string) []string {
	out := []string{}
	start := 0
	for i := 0; i <= len(csv); i++ {
		if i == len(csv) || csv[i] == ',' {
			part := csv[start:i]
			for len(part) > 0 && (part[0] == ' ' || part[0] == '\t') {
				part = part[1:]
			}
			for len(part) > 0 && (part[len(part)-1] == ' ' || part[len(part)-1] == '\t') {
				part = part[:len(part)-1]
			}
			if part != "" {
				out = append(out, part)
			}
			start = i + 1
		}
	}
	return out
}

func equalFoldASCII(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := 0; i < len(a); i++ {
		ca, cb := a[i], b[i]
		if 'A' <= ca && ca <= 'Z' {
			ca += 'a' - 'A'
		}
		if 'A' <= cb && cb <= 'Z' {
			cb += 'a' - 'A'
		}
		if ca != cb {
			return false
		}
	}
	return true
}
