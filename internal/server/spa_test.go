package server

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestSPAHandlerServesIndexHTMLAtRoot confirms spaHandler itself (not the
// full admin router) serves the built index.html at "/".
func TestSPAHandlerServesIndexHTMLAtRoot(t *testing.T) {
	rec := httptest.NewRecorder()
	spaHandler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("GET / status = %d, want 200; body = %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `<div id="app">`) {
		t.Errorf("GET / body does not look like the built index.html (missing #app mount point); body = %s", rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("GET / Content-Type = %q, want a text/html prefix", ct)
	}
}

// TestSPAHandlerFallsBackToIndexHTMLForUnknownClientRoute asserts an unknown
// non-/api path serves the same index.html body as "/", so a hard reload on a
// client side route still works.
func TestSPAHandlerFallsBackToIndexHTMLForUnknownClientRoute(t *testing.T) {
	rootRec := httptest.NewRecorder()
	spaHandler().ServeHTTP(rootRec, httptest.NewRequest(http.MethodGet, "/", nil))

	fallbackRec := httptest.NewRecorder()
	spaHandler().ServeHTTP(fallbackRec, httptest.NewRequest(http.MethodGet, "/some-client-route", nil))

	if fallbackRec.Code != http.StatusOK {
		t.Fatalf("GET /some-client-route status = %d, want 200; body = %s", fallbackRec.Code, fallbackRec.Body.String())
	}
	if fallbackRec.Body.String() != rootRec.Body.String() {
		t.Errorf("GET /some-client-route body does not match GET / body: fallback must serve the same index.html")
	}
	if ct := fallbackRec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("GET /some-client-route Content-Type = %q, want a text/html prefix", ct)
	}
}

// TestSPAHandlerRefusesUnmatchedUIAPIPath asserts an unmatched path under
// /ui/api/ 404s with a JSON error rather than the SPA shell. The
// assertion is on the body because a 404 status alone does not prove
// index.html was withheld. This exercises the bare handler only; the
// real /api/* surface is pinned separately through the full router in
// spa_plane_scope_test.go.
func TestSPAHandlerRefusesUnmatchedUIAPIPath(t *testing.T) {
	rec := httptest.NewRecorder()
	spaHandler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/does-not-exist", nil))

	if rec.Code != http.StatusNotFound {
		t.Fatalf("GET /api/does-not-exist status = %d, want 404; body = %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), `<div id="app">`) {
		t.Errorf("GET /api/does-not-exist body looks like index.html; the SPA fallback must never shadow /ui/api/: body = %s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"error":"not found"`) {
		t.Errorf("GET /api/does-not-exist body = %q, want a not-found JSON error", rec.Body.String())
	}
}

// TestSPAHandlerServesRealStaticAssetDirectly confirms a real built asset
// is served as itself rather than redirected to index.html: only unknown
// paths fall back to the SPA shell. The filename is read from distFS
// rather than hardcoded, since Vite content-hashes it.
func TestSPAHandlerServesRealStaticAssetDirectly(t *testing.T) {
	entries, err := fs.ReadDir(distFS, "assets")
	if err != nil || len(entries) == 0 {
		t.Fatalf("no built assets found in distFS: %v", err)
	}
	assetPath := "/assets/" + entries[0].Name()

	rec := httptest.NewRecorder()
	spaHandler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, assetPath, nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s status = %d, want 200; body = %s", assetPath, rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), `<div id="app">`) {
		t.Errorf("GET %s body looks like index.html, not the actual asset", assetPath)
	}
}
