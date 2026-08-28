package auth_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/auth"
)

// A refused admin request has two kinds of consumer and they need different
// answers. A browser navigating to a page can only act on a redirect; the
// script, stylesheet and icon that page pulls, and every fetch() the SPA makes,
// need the status instead. Handing an HTML login page to a <script> is how a
// shell renders blank with no server-side signal.
//
// httptest.NewRequest leaves RemoteAddr at 192.0.2.1:1234, which is not
// loopback, so these requests traverse the full auth chain rather than the Path
// 0 bypass. refusalShapeGuard is the control on that.

const refusalJSONBody = `{"error":"admin authentication required"}`

// refusedResponse drives the real middleware with no credential and returns
// what the consumer receives.
func refusedResponse(t *testing.T, method, target string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, target, nil)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	if isLoopbackAddr(req.RemoteAddr) {
		t.Fatalf("fixture RemoteAddr %q is loopback: the request would take the Path 0 bypass and assert nothing about a refusal", req.RemoteAddr)
	}
	rec := httptest.NewRecorder()
	handler := auth.AdminAuthMiddleware("the-key", nil, nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("%s %s reached the protected handler with no credential", r.Method, r.URL.Path)
	}))
	handler.ServeHTTP(rec, req)
	return rec
}

// TestRefusedNavigationRedirectsToLogin covers the consumer that can act on a
// redirect: a top-level document request, both as current browsers shape it
// (Sec-Fetch-Dest) and as an older one does (Accept alone).
func TestRefusedNavigationRedirectsToLogin(t *testing.T) {
	for _, tc := range []struct {
		name    string
		target  string
		headers map[string]string
	}{
		{
			name:   "bare root, current browser navigation",
			target: "/",
			headers: map[string]string{
				"Sec-Fetch-Dest": "document",
				"Sec-Fetch-Mode": "navigate",
				"Accept":         "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8",
			},
		},
		{
			name:    "SPA mount, current browser navigation",
			target:  "/ui/",
			headers: map[string]string{"Sec-Fetch-Dest": "document"},
		},
		{
			name:    "client side route, hard reload",
			target:  "/ui/devices",
			headers: map[string]string{"Sec-Fetch-Dest": "document"},
		},
		{
			name:    "no Sec-Fetch-Dest, Accept names HTML",
			target:  "/",
			headers: map[string]string{"Accept": "text/html,*/*;q=0.8"},
		},
		{
			name:    "no Sec-Fetch-Dest, Accept names XHTML",
			target:  "/",
			headers: map[string]string{"Accept": "application/xhtml+xml"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := refusedResponse(t, http.MethodGet, tc.target, tc.headers)

			if rec.Code != http.StatusSeeOther {
				t.Fatalf("GET %s refused: status = %d, want 303; body = %q", tc.target, rec.Code, rec.Body.String())
			}
			if got := rec.Header().Get("Location"); got != auth.AdminLoginPath {
				t.Errorf("GET %s refused: Location = %q, want %q", tc.target, got, auth.AdminLoginPath)
			}
			if got := rec.Header().Get("WWW-Authenticate"); got != "" {
				t.Errorf("GET %s refused: WWW-Authenticate = %q; no current browser prompts for a scheme named here", tc.target, got)
			}
		})
	}
}

// TestRefusedSubresourceKeepsStatus covers every consumer that reads the status:
// the shell's own subresources, the SPA's JSON reads, the SSE stream, and a
// non-GET submission.
func TestRefusedSubresourceKeepsStatus(t *testing.T) {
	for _, tc := range []struct {
		name    string
		method  string
		target  string
		headers map[string]string
	}{
		{
			name:   "script tag pulling the bundle",
			method: http.MethodGet,
			target: "/ui/assets/index-CwIJSNHN.js",
			headers: map[string]string{
				"Sec-Fetch-Dest": "script",
				"Sec-Fetch-Mode": "cors",
				"Accept":         "*/*",
			},
		},
		{
			name:    "stylesheet link",
			method:  http.MethodGet,
			target:  "/ui/assets/index-BQ0KgbDE.css",
			headers: map[string]string{"Sec-Fetch-Dest": "style", "Accept": "text/css,*/*;q=0.1"},
		},
		{
			name:    "favicon",
			method:  http.MethodGet,
			target:  "/ui/favicon.svg",
			headers: map[string]string{"Sec-Fetch-Dest": "image", "Accept": "image/svg+xml,*/*;q=0.8"},
		},
		{
			name:    "SPA session probe",
			method:  http.MethodGet,
			target:  "/dashboard/data",
			headers: map[string]string{"Sec-Fetch-Dest": "empty", "Accept": "application/json"},
		},
		{
			name:    "EventSource stream",
			method:  http.MethodGet,
			target:  "/dashboard/events",
			headers: map[string]string{"Sec-Fetch-Dest": "empty", "Accept": "text/event-stream"},
		},
		{
			name:    "JSON API read",
			method:  http.MethodGet,
			target:  "/api/topology",
			headers: map[string]string{"Accept": "application/json"},
		},
		{
			name:    "a POST submission",
			method:  http.MethodPost,
			target:  "/api/devices",
			headers: map[string]string{"Sec-Fetch-Dest": "empty", "Accept": "text/html"},
		},
		{
			name:    "no headers at all",
			method:  http.MethodGet,
			target:  "/ui/",
			headers: nil,
		},
		{
			name:    "HTML refused by q=0",
			method:  http.MethodGet,
			target:  "/",
			headers: map[string]string{"Accept": "text/html;q=0,application/json"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := refusedResponse(t, tc.method, tc.target, tc.headers)

			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("%s %s refused: status = %d, want 401; body = %q", tc.method, tc.target, rec.Code, rec.Body.String())
			}
			if got := rec.Body.String(); got != refusalJSONBody {
				t.Errorf("%s %s refused: body = %q, want %q", tc.method, tc.target, got, refusalJSONBody)
			}
			if got := rec.Header().Get("Location"); got != "" {
				t.Errorf("%s %s refused: Location = %q; this consumer parses the body as its own content type, so an HTML redirect is not readable to it", tc.method, tc.target, got)
			}
			if got := rec.Header().Get("WWW-Authenticate"); got != "" {
				t.Errorf("%s %s refused: WWW-Authenticate = %q, want none", tc.method, tc.target, got)
			}
		})
	}
}

// TestRefusalShapeGuardCatchesTheBypass keeps the two tests above honest: if a
// fixture ever arrives from a loopback address, Path 0 admits it and every
// assertion about a refusal is vacuous.
func TestRefusalShapeGuardCatchesTheBypass(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "127.0.0.1:1234"
	if !isLoopbackAddr(req.RemoteAddr) {
		t.Fatalf("isLoopbackAddr(%q) = false; the guard in refusedResponse cannot fire", req.RemoteAddr)
	}

	rec := httptest.NewRecorder()
	admitted := false
	handler := auth.AdminAuthMiddleware("the-key", nil, nil)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		admitted = true
	}))
	handler.ServeHTTP(rec, req)
	if !admitted {
		t.Fatal("a loopback request with no credential was refused; the Path 0 bypass this guard exists for is gone, and the guard is now dead weight")
	}
}
