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

	adminRouter, _ := server.BuildAdminRouter("test-admin-key", nil, stores, "GCM", nil, nil, nil)

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

	adminRouter, _ := server.BuildAdminRouter("test-admin-key", nil, stores, "GCM", nil, nil, nil)

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

// TestAdminListenerDashboardStillServedAtRoot pins that mounting the
// shell at "/ui/" left the existing dashboard's "GET /" untouched: the
// dashboard is not rewritten or removed by this change.
func TestAdminListenerDashboardStillServedAtRoot(t *testing.T) {
	stores := newTestStores()

	adminRouter, _ := server.BuildAdminRouter("test-admin-key", nil, stores, "GCM", nil, nil, nil)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "127.0.0.1:54321"
	rec := httptest.NewRecorder()
	adminRouter.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET / status = %d, want 200; body = %q", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), `<div id="app">`) {
		t.Fatalf("GET / served the admin UI shell instead of the dashboard")
	}
	if !strings.Contains(rec.Body.String(), "Connected Devices") {
		t.Fatalf("GET / body does not look like the dashboard page; body = %q", rec.Body.String())
	}
}
