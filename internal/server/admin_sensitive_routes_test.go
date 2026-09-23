package server_test

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/auth"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/server"
)

// #579, #631: the certificate routes mint and return key material, and the
// traffic-capture read routes return captured Authorization and Cookie
// header bytes verbatim. Both refuse a request the loopback bypass admitted
// with no credential at all, even though every other admin route accepts it.

const sensitiveRefusalBody = `{"error":"admin authentication required"}`

// captureSlogForSensitiveRoutes duplicates internal/auth's own captureSlog
// helper (the two packages' test binaries cannot share an unexported test
// helper; login_credential_logging_test.go does the same thing for the same
// reason). slog.SetDefault is process-global, so this test file never calls
// t.Parallel().
func captureSlogForSensitiveRoutes(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return &buf
}

func newSensitiveRoutesRouter(t *testing.T) http.Handler {
	t.Helper()
	router, _ := server.BuildAdminRouter(
		"the-key", newScopeTestCertService(t), newTestStores(), "GCM",
		auth.NewTicketStore(30*time.Second), auth.NewSessionStore(30*time.Minute, 8*time.Hour),
		server.DefaultAdminAllowedHosts(), false, http.NotFoundHandler(),
	)
	return router
}

func bypassOnlyRequest(method, target string) *http.Request {
	req := httptest.NewRequest(method, target, strings.NewReader(""))
	req.Host = "127.0.0.1"
	req.RemoteAddr = "127.0.0.1:54321"
	return req
}

func bearerFromLoopbackRequest(method, target, contentType, body string) *http.Request {
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	req.Host = "127.0.0.1"
	req.RemoteAddr = "127.0.0.1:54321"
	req.Header.Set("Authorization", "Bearer the-key")
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	return req
}

// sensitiveRouteCase drives one route through the bypass-only refusal and
// its credentialed control.
type sensitiveRouteCase struct {
	name              string
	method, path      string
	bearerContentType string
	bearerBody        string
	wantReachedStatus int // status with a valid Bearer: evidence the request reached the underlying handler, not this gate
}

func (tc sensitiveRouteCase) run(t *testing.T, router http.Handler) {
	t.Helper()

	t.Run(tc.name+"/bypass only refused", func(t *testing.T) {
		buf := captureSlogForSensitiveRoutes(t)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, bypassOnlyRequest(tc.method, tc.path))

		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("%s %s bypass-only: status = %d, want 401; body = %q", tc.method, tc.path, rec.Code, rec.Body.String())
		}
		if rec.Body.String() != sensitiveRefusalBody {
			t.Errorf("%s %s bypass-only: body = %q, want %q", tc.method, tc.path, rec.Body.String(), sensitiveRefusalBody)
		}
		if !strings.Contains(buf.String(), `"event":"admin_sensitive_route_refused"`) {
			t.Errorf("%s %s bypass-only: no admin_sensitive_route_refused log line; captured = %s", tc.method, tc.path, buf.String())
		}
		if !strings.Contains(buf.String(), `"admission_path":"loopback_bypass"`) {
			t.Errorf("%s %s bypass-only: log line missing admission_path=loopback_bypass; captured = %s", tc.method, tc.path, buf.String())
		}
	})

	t.Run(tc.name+"/valid credential from loopback still succeeds", func(t *testing.T) {
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, bearerFromLoopbackRequest(tc.method, tc.path, tc.bearerContentType, tc.bearerBody))

		if rec.Code != tc.wantReachedStatus {
			t.Fatalf("%s %s with a valid Bearer from loopback: status = %d, want %d (evidence it reached the handler); body = %q",
				tc.method, tc.path, rec.Code, tc.wantReachedStatus, rec.Body.String())
		}
		if rec.Body.String() == sensitiveRefusalBody {
			t.Errorf("%s %s with a valid Bearer from loopback: refused as if uncredentialed", tc.method, tc.path)
		}
	})
}

// TestSensitiveRoutesRefuseBypassAdmission is acceptance criteria 1 and 2 for
// both #579 and #631, across every route in the two families.
func TestSensitiveRoutesRefuseBypassAdmission(t *testing.T) {
	router := newSensitiveRoutesRouter(t)

	cases := []sensitiveRouteCase{
		{name: "GET /api/certs/ca", method: http.MethodGet, path: "/api/certs/ca", wantReachedStatus: http.StatusOK},
		{name: "POST /api/certs/server", method: http.MethodPost, path: "/api/certs/server", bearerContentType: "application/json", bearerBody: "not json", wantReachedStatus: http.StatusBadRequest},
		{name: "POST /api/certs/device", method: http.MethodPost, path: "/api/certs/device", bearerContentType: "application/json", bearerBody: "not json", wantReachedStatus: http.StatusBadRequest},
		{name: "GET /api/certs/device-types", method: http.MethodGet, path: "/api/certs/device-types", wantReachedStatus: http.StatusOK},
		{name: "POST /api/certs/info", method: http.MethodPost, path: "/api/certs/info", bearerContentType: "application/x-pem-file", bearerBody: "not a certificate", wantReachedStatus: http.StatusBadRequest},
		{name: "GET /api/traffic/ (clients sub-route)", method: http.MethodGet, path: "/api/traffic/clients", wantReachedStatus: http.StatusNotFound},
		{name: "GET /api/traffic/ (stats sub-route)", method: http.MethodGet, path: "/api/traffic/stats", wantReachedStatus: http.StatusNotFound},
	}

	for _, tc := range cases {
		tc.run(t, router)
	}
}

// TestSensitiveRoutesStillRefuseUnderNonLoopbackExposure is acceptance
// criterion 4: the rule holds on an opt-in non-loopback bind too.
// BuildAdminRouter takes no bind-address parameter: the request shape that
// actually distinguishes exposure posture is whether the request looks
// loopback-local, and docs/admin.md's own warning names the failure mode
// that produces exactly this shape under a non-loopback bind - "stock nginx
// does not [inject X-Forwarded-For], and without those headers every
// relayed request looks loopback-local and is admitted with no credential
// at all". This request is that shape: RemoteAddr 127.0.0.1, as a relaying
// proxy would present it, with no forwarded header. The credential
// requirement on these two families holds regardless of why the request
// looks loopback.
func TestSensitiveRoutesStillRefuseUnderNonLoopbackExposure(t *testing.T) {
	router := newSensitiveRoutesRouter(t)

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, bypassOnlyRequest(http.MethodGet, "/api/certs/ca"))

	if rec.Code != http.StatusUnauthorized || rec.Body.String() != sensitiveRefusalBody {
		t.Fatalf("GET /api/certs/ca under a misconfigured-proxy non-loopback exposure: status = %d body = %q, want 401 %q",
			rec.Code, rec.Body.String(), sensitiveRefusalBody)
	}
}

// TestSensitiveAdminPatternsMatchRouterFamilies establishes the two families
// from the guard's own domain (every "/api/certs" and "/api/traffic" pattern
// on the AUTHENTICATED mux, not BuildAdminRouter's merged list), not from the
// handful of route names #579 and #631 happen to quote. A new route under
// either prefix that is not added to sensitiveAdminPatterns fails here
// instead of silently joining an unprotected family.
//
// #579 MEDIUM-3 (test coverage lane): the merged list also carries the
// public outer mux's routes, so a pattern that satisfies this comparison
// without ever reaching requireCredentialForSensitiveRoutes (a route
// registered on outer instead of authed) used to pass unnoticed.
// AuthedAdminPatterns reads only the mux requireCredentialForSensitiveRoutes
// actually matches against, closing that gap; the runtime check in
// TestEverySensitiveAdminPatternRefusesBypassOnly below closes the other
// half by driving a real request for every map entry instead of comparing
// two static lists.
func TestSensitiveAdminPatternsMatchRouterFamilies(t *testing.T) {
	patterns := server.AuthedAdminPatterns(
		"the-key", newScopeTestCertService(t), newTestStores(), "GCM",
		auth.NewTicketStore(30*time.Second), auth.NewSessionStore(30*time.Minute, 8*time.Hour),
		false, http.NotFoundHandler(),
	)

	var want []string
	for _, p := range patterns {
		_, path, ok := strings.Cut(p, " ")
		if !ok {
			t.Fatalf("pattern %q names no method", p)
		}
		if strings.HasPrefix(path, "/api/certs") || strings.HasPrefix(path, "/api/traffic") {
			want = append(want, p)
		}
	}
	// #579 HIGH-1: the ticket-mint route is not under either path prefix, so
	// it is not derivable from the router's pattern list the way the two
	// families are; it is asserted explicitly instead.
	want = append(want, "POST /auth/ticket")
	sort.Strings(want)

	var got []string
	for p := range server.SensitiveAdminPatterns {
		got = append(got, p)
	}
	sort.Strings(got)

	if !slices.Equal(got, want) {
		t.Fatalf("sensitiveAdminPatterns = %q (%d), want every /api/certs and /api/traffic route from the router = %q (%d)",
			got, len(got), want, len(want))
	}
}

// TestEverySensitiveAdminPatternRefusesBypassOnly drives a real bypass-only
// request for every entry in sensitiveAdminPatterns, rather than comparing
// two static pattern lists (#579 MEDIUM-3, test coverage lane). A pattern
// added to the map that the guard cannot actually reach - mounted on the
// outer mux instead of the authed one - stayed green under the old
// membership-only test, since the membership check never drove a request.
// This closes it: any such route now answers with whatever the outer mux
// gives an unauthenticated request, which is not the refusal this test
// requires.
func TestEverySensitiveAdminPatternRefusesBypassOnly(t *testing.T) {
	router := newSensitiveRoutesRouter(t)

	for pattern := range server.SensitiveAdminPatterns {
		method, path, ok := strings.Cut(pattern, " ")
		if !ok {
			t.Fatalf("pattern %q names no method", pattern)
		}
		t.Run(pattern, func(t *testing.T) {
			buf := captureSlogForSensitiveRoutes(t)
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, bypassOnlyRequest(method, path))

			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("%s bypass-only: status = %d, want 401; body = %q", pattern, rec.Code, rec.Body.String())
			}
			if rec.Body.String() != sensitiveRefusalBody {
				t.Errorf("%s bypass-only: body = %q, want %q", pattern, rec.Body.String(), sensitiveRefusalBody)
			}
			if !strings.Contains(buf.String(), `"event":"admin_sensitive_route_refused"`) {
				t.Errorf("%s bypass-only: no admin_sensitive_route_refused log line; captured = %s", pattern, buf.String())
			}
		})
	}
}
