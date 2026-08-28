package server_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/config"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/server"
)

// TestProtocolListenerDoesNotMountAdminUIShell mirrors
// TestProtocolListenerDoesNotMountCertAPI's shape: the SEP2 protocol
// listener's router must not serve the admin UI shell or its assets at
// all. RemoteAddr is loopback with no proxy headers (the Path 0 bypass
// shape); if the shell were mounted here, it would serve regardless of
// auth.
func TestProtocolListenerDoesNotMountAdminUIShell(t *testing.T) {
	cfg := &config.Config{}
	stores := newTestStores()

	router, patterns := server.BuildProtocolRouter(cfg, stores, nil, "test-sfdi", "test-lfdi", nil)

	cases := []string{"/ui/", "/ui/assets/index.js", "/ui/favicon.svg"}
	for _, p := range cases {
		t.Run(p, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, p, nil)
			req.RemoteAddr = "127.0.0.1:54321"
			rec := httptest.NewRecorder()

			router.ServeHTTP(rec, req)

			if rec.Code != http.StatusNotFound {
				t.Fatalf("GET %s on protocol listener: status = %d, want 404 (the admin UI shell must not be mounted on the protocol listener); body = %q",
					p, rec.Code, rec.Body.String())
			}
			if strings.Contains(rec.Body.String(), `<div id="app">`) {
				t.Fatalf("GET %s on protocol listener returned the SPA shell body", p)
			}
		})
	}

	for _, pattern := range patterns {
		if strings.Contains(pattern, "/ui") {
			t.Fatalf("protocol router pattern list contains an admin UI pattern: %q", pattern)
		}
	}
}

// TestAdminListenerMountsAdminUIShell confirms the legitimate path: the
// admin listener's router serves the shell at /ui/, and a hard reload of
// an unknown client side route under /ui/ still returns the shell.
func TestAdminListenerMountsAdminUIShell(t *testing.T) {
	stores := newTestStores()

	adminRouter, _ := server.BuildAdminRouter("test-admin-key", nil, stores, "GCM", nil, nil, nil, false)

	req := httptest.NewRequest(http.MethodGet, "/ui/", nil)
	req.RemoteAddr = "127.0.0.1:54321"
	rec := httptest.NewRecorder()
	adminRouter.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /ui/ on admin listener: status = %d, want 200; body = %q", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `<div id="app">`) {
		t.Fatalf("GET /ui/ body does not look like the built index.html (missing #app mount point); body = %q", rec.Body.String())
	}

	reloadReq := httptest.NewRequest(http.MethodGet, "/ui/some-unknown-client-route", nil)
	reloadReq.RemoteAddr = "127.0.0.1:54321"
	reloadRec := httptest.NewRecorder()
	adminRouter.ServeHTTP(reloadRec, reloadReq)

	if reloadRec.Code != http.StatusOK {
		t.Fatalf("GET /ui/some-unknown-client-route: status = %d, want 200; body = %q", reloadRec.Code, reloadRec.Body.String())
	}
	if reloadRec.Body.String() != rec.Body.String() {
		t.Fatalf("GET /ui/some-unknown-client-route body does not match GET /ui/ body: fallback must serve the same index.html")
	}
}

// TestAdminListenerRealUnmatchedAPIPathIsNotShadowedBySPA is the
// integration-level pin for the boundary the /ui/api/ guard in
// spaHandler cannot prove by itself: a real /api/* request that no
// handler matches falls through to the dashboard's catch-all "GET /"
// (which 404s on any path but "/"), never to the SPA mounted at /ui/.
func TestAdminListenerRealUnmatchedAPIPathIsNotShadowedBySPA(t *testing.T) {
	stores := newTestStores()

	adminRouter, _ := server.BuildAdminRouter("test-admin-key", nil, stores, "GCM", nil, nil, nil, false)

	req := httptest.NewRequest(http.MethodGet, "/api/does-not-exist", nil)
	req.RemoteAddr = "127.0.0.1:54321"
	rec := httptest.NewRecorder()
	adminRouter.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("GET /api/does-not-exist status = %d, want 404; body = %q", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), `<div id="app">`) {
		t.Fatalf("GET /api/does-not-exist body looks like the SPA shell; the real /api surface must never fall to the /ui/ handler: body = %q", rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "Connected Devices") {
		t.Fatalf("GET /api/does-not-exist body looks like the dashboard page; body = %q", rec.Body.String())
	}
}

// TestAdminListenerDashboardFlagServesBothBranches pins the rollback
// path: GET / answers with the embedded admin UI by default, and with the
// pre-Svelte string-constant dashboard when the legacy flag is set. Both
// branches are exercised in one test because either assertion alone would
// pass against a handler that ignores the flag and always serves the
// other page.
//
// The two bodies are also asserted to be different pages, not just
// non-empty: "#app is present" and "Connected Devices is present" would
// both hold for a single page that happened to contain both strings.
func TestAdminListenerDashboardFlagServesBothBranches(t *testing.T) {
	stores := newTestStores()

	getRoot := func(t *testing.T, legacy bool) string {
		t.Helper()
		adminRouter, _ := server.BuildAdminRouter("test-admin-key", nil, stores, "GCM", nil, nil, nil, legacy)

		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.RemoteAddr = "127.0.0.1:54321"
		rec := httptest.NewRecorder()
		adminRouter.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("GET / (legacyDashboard=%v) status = %d, want 200; body = %q", legacy, rec.Code, rec.Body.String())
		}
		return rec.Body.String()
	}

	t.Run("flag off serves the embedded admin UI", func(t *testing.T) {
		body := getRoot(t, false)

		if !strings.Contains(body, `<div id="app">`) {
			t.Fatalf("GET / did not serve the SPA index (missing #app mount point); body = %q", body)
		}
		if strings.Contains(body, "Connected Devices") {
			t.Fatalf("GET / served the legacy string-constant dashboard with the flag off; body = %q", body)
		}
	})

	t.Run("flag on serves the legacy string-constant dashboard", func(t *testing.T) {
		body := getRoot(t, true)

		if !strings.Contains(body, "Connected Devices") {
			t.Fatalf("GET / did not serve the legacy dashboard with the flag on; body = %q", body)
		}
		if strings.Contains(body, `<div id="app">`) {
			t.Fatalf("GET / served the SPA index with the legacy flag on; body = %q", body)
		}
	})

	t.Run("the legacy page is still the string constant in this binary", func(t *testing.T) {
		// Guards the rollback being a rollback: the flag must reach the
		// dashboard_html.go content itself. The inline handler attribute is
		// the discriminator, since the SPA index.html carries no inline
		// script at all and renders every panel client side.
		body := getRoot(t, true)
		if !strings.Contains(body, `onclick="sendControl()"`) {
			t.Fatalf("legacy body does not look like the pre-Svelte dashboard; body = %q", body)
		}
	})
}

// TestAdminListenerSPAIsReachableAtBothRoots asserts the SPA is served at
// "/" and at "/ui/" with the same bytes, so the operator reaches the admin
// UI at the address they typed and a bookmark of either path works.
func TestAdminListenerSPAIsReachableAtBothRoots(t *testing.T) {
	stores := newTestStores()
	adminRouter, _ := server.BuildAdminRouter("test-admin-key", nil, stores, "GCM", nil, nil, nil, false)

	get := func(path string) string {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.RemoteAddr = "127.0.0.1:54321"
		rec := httptest.NewRecorder()
		adminRouter.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s status = %d, want 200; body = %q", path, rec.Code, rec.Body.String())
		}
		return rec.Body.String()
	}

	if root, ui := get("/"), get("/ui/"); root != ui {
		t.Fatalf("GET / and GET /ui/ served different bodies:\n/ = %q\n/ui/ = %q", root, ui)
	}
}
