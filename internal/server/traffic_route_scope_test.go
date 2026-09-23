package server_test

// #628 fix round 1 item 5: the coverage lane's LOW 1 (capture-off route
// absence has nothing asserting it) and the code-quality/coverage lanes'
// refutation of the PR's own note that a POST to a traffic path falls
// through to a plain 404 (it does not: net/http's ServeMux answers 405
// itself, since GET is registered on the same pattern). Both are unit-level
// BuildAdminRouter checks, needing no full server boot.

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/auth"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/server"
)

const trafficMountPattern = "GET /api/traffic/"

// TestTrafficRoutePatternPresentOnlyWithHandler pins the pattern list side
// of capture-off route absence: BuildAdminRouter's returned pattern list
// carries "GET /api/traffic/" exactly when trafficHandler is non-nil, never
// when it is nil (capture off).
//
// Mutant (admin_router.go): change `if trafficHandler != nil {` to `if true {`.
// This test goes RED on the nil case: authed.Handle registers the pattern
// with a nil StripPrefix target, so it appears in the pattern list with
// capture off.
func TestTrafficRoutePatternPresentOnlyWithHandler(t *testing.T) {
	buildRouter := func(trafficHandler http.Handler) []string {
		_, patterns := server.BuildAdminRouter(
			"the-key", newScopeTestCertService(t), newTestStores(), "GCM",
			auth.NewTicketStore(30*time.Second), auth.NewSessionStore(30*time.Minute, 8*time.Hour),
			server.DefaultAdminAllowedHosts(), false, trafficHandler,
		)
		return patterns
	}

	countPattern := func(patterns []string) int {
		n := 0
		for _, p := range patterns {
			if p == trafficMountPattern {
				n++
			}
		}
		return n
	}

	if got := countPattern(buildRouter(http.NotFoundHandler())); got != 1 {
		t.Errorf("pattern list has %d entries for %q with capture on, want 1", got, trafficMountPattern)
	}
	if got := countPattern(buildRouter(nil)); got != 0 {
		t.Errorf("pattern list has %d entries for %q with capture off (nil trafficHandler), want 0", got, trafficMountPattern)
	}
}

// TestTrafficRouteAbsentWithCaptureOff is the HTTP side of the same
// property: a GET to a traffic path with capture off (nil trafficHandler)
// answers net/http's own "page not found" 404 - the mux miss signal a route
// that was never mounted produces - not a handler that happens to run and
// 404 for its own reasons.
//
// Mutant: same `if true {` as above. This test goes RED: the request now
// reaches http.StripPrefix wrapping a nil handler and panics instead of
// returning the mux-miss 404 this test expects.
func TestTrafficRouteAbsentWithCaptureOff(t *testing.T) {
	router, _ := server.BuildAdminRouter(
		"the-key", newScopeTestCertService(t), newTestStores(), "GCM",
		auth.NewTicketStore(30*time.Second), auth.NewSessionStore(30*time.Minute, 8*time.Hour),
		server.DefaultAdminAllowedHosts(), false, nil,
	)

	req := httptest.NewRequest(http.MethodGet, "/api/traffic/clients", nil)
	req.Host = "127.0.0.1"
	req.RemoteAddr = "127.0.0.1:54321"
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("GET /api/traffic/clients with capture off: status = %d, want 404; body = %q", rec.Code, rec.Body.String())
	}
}

// TestTrafficRouteRefusesNonGETMethod is #628 fix round 1 item 5's other
// half (P6): the PR description said a POST to a traffic path falls through
// to a plain 404. Both the code-quality and coverage lanes measured 405
// with Allow: GET, HEAD instead, through the built router with a valid
// Bearer; this pins the same behavior at the unit level, unauthenticated
// (the loopback bypass), for two non-GET methods.
//
// Mutant (admin_body_type.go): drop requireAdminBodyTypes's
// `if pattern == "" { mux.ServeHTTP(w, r); return }` early return. A
// method-mismatched request reaches mux.mux.Handler with an empty pattern
// (net/http answers 405 itself for a matched path with no matched method),
// so falling through looks it up in adminBodyTypes, finds nothing declared,
// and answers 415 instead of 405: both subtests go RED.
func TestTrafficRouteRefusesNonGETMethod(t *testing.T) {
	router, _ := server.BuildAdminRouter(
		"the-key", newScopeTestCertService(t), newTestStores(), "GCM",
		auth.NewTicketStore(30*time.Second), auth.NewSessionStore(30*time.Minute, 8*time.Hour),
		server.DefaultAdminAllowedHosts(), false, http.NotFoundHandler(),
	)

	for _, method := range []string{http.MethodPost, http.MethodDelete} {
		t.Run(method, func(t *testing.T) {
			req := httptest.NewRequest(method, "/api/traffic/clients", nil)
			req.Host = "127.0.0.1"
			req.RemoteAddr = "127.0.0.1:54321"
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)

			if rec.Code != http.StatusMethodNotAllowed {
				t.Fatalf("%s /api/traffic/clients: status = %d, want 405; body = %q", method, rec.Code, rec.Body.String())
			}
			if allow := rec.Header().Get("Allow"); allow == "" {
				t.Errorf("%s /api/traffic/clients: no Allow header on the 405", method)
			}
		})
	}
}
