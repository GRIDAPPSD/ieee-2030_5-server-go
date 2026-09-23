package server_test

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net"
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
//
// #579 MEDIUM-2 (test coverage lane): the previous version of this test
// built its request with the same bypassOnlyRequest helper, method and path
// as the table's first case in TestSensitiveRoutesRefuseBypassAdmission, run
// through the same synthetic router built by newSensitiveRoutesRouter. It
// asserted the identical thing twice and could not fail unless that case
// did. This version stands up a real server.Run, the path production takes,
// with config.AdminAllowNonLoopback opted in and the admin listener bound to
// 0.0.0.0 (exposureBootCfg): an uncredentialed request over that real
// listener, from loopback address as a relaying proxy would present it,
// covers the criterion through the full assembly (startAdminServer's own
// wiring of BuildAdminRouter into the real *http.Server) rather than through
// a second copy of the in-memory router the other tests already exercise.
func TestSensitiveRoutesStillRefuseUnderNonLoopbackExposure(t *testing.T) {
	_, adminPort, err := net.SplitHostPort(mustProbePort(t))
	if err != nil {
		t.Fatalf("split probe port: %v", err)
	}
	loopbackProbe := net.JoinHostPort("127.0.0.1", adminPort)

	cfg, c := exposureBootCfg(t, adminPort, true)

	ctx, cancel := context.WithCancel(context.Background())
	runErrCh := make(chan error, 1)
	go func() { runErrCh <- server.Run(ctx, cfg, c.svc) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-runErrCh:
		case <-time.After(5 * time.Second):
			t.Error("server.Run did not exit within 5s after cancel")
		}
	})

	// Wait for the admin listener to come up, exactly as
	// TestAdminListenerRefusesNonLoopbackBindWithoutOptIn's opt-in case does:
	// any response is proof-of-life, the status is asserted separately below.
	client := &http.Client{Timeout: 500 * time.Millisecond}
	var resp *http.Response
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		resp, err = client.Get("http://" + loopbackProbe + "/api/certs/ca")
		if err == nil {
			break
		}
		select {
		case runErr := <-runErrCh:
			t.Fatalf("server.Run exited during boot: %v", runErr)
		default:
		}
		time.Sleep(25 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("admin listener never became ready on %s: %v", loopbackProbe, err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read response body: %v", err)
	}
	if resp.StatusCode != http.StatusUnauthorized || string(body) != sensitiveRefusalBody {
		t.Fatalf("GET /api/certs/ca, no credential, over a real opt-in non-loopback bind: status = %d body = %q, want 401 %q",
			resp.StatusCode, body, sensitiveRefusalBody)
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

// TestEveryDefaultProtectedAdminWriteRefusesBypassOnly is the class
// TestSensitiveAdminPatternsMatchRouterFamilies cannot cover: a credential-
// minting write route (#579 HIGH-1 was POST /auth/ticket) sits under no
// shared path prefix, so it cannot be derived by matching "/api/certs" or
// "/api/traffic" the way that test derives its two families. This test
// derives the OTHER class instead - every authed write route the router
// reports, minus the small nonSensitiveAdminWrites exemption list, minus
// whatever is already in SensitiveAdminPatterns - and drives a real
// bypass-only request at each one. A new write route left off
// nonSensitiveAdminWrites joins this derived set automatically and must
// refuse here, with no name to remember to add anywhere (#579 fix round 2,
// MEDIUM, coverage lane).
func TestEveryDefaultProtectedAdminWriteRefusesBypassOnly(t *testing.T) {
	patterns := server.AuthedAdminPatterns(
		"the-key", newScopeTestCertService(t), newTestStores(), "GCM",
		auth.NewTicketStore(30*time.Second), auth.NewSessionStore(30*time.Minute, 8*time.Hour),
		false, http.NotFoundHandler(),
	)

	var defaultProtected []string
	for _, p := range patterns {
		method, _, ok := strings.Cut(p, " ")
		if !ok {
			t.Fatalf("pattern %q names no method", p)
		}
		switch method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
			continue
		}
		if _, sensitive := server.SensitiveAdminPatterns[p]; sensitive {
			continue
		}
		if _, exempt := server.NonSensitiveAdminWrites[p]; exempt {
			continue
		}
		defaultProtected = append(defaultProtected, p)
	}
	// Control: the derivation itself must find something to check, or every
	// assertion below passes vacuously (#579 HIGH-1 is exactly this class).
	if len(defaultProtected) == 0 {
		t.Fatalf("no default-protected admin write route found; POST /auth/ticket, at least, should be one")
	}

	router := newSensitiveRoutesRouter(t)
	for _, pattern := range defaultProtected {
		method, path, _ := strings.Cut(pattern, " ")
		target := strings.ReplaceAll(path, "{id}", "x")
		t.Run(pattern, func(t *testing.T) {
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, bypassOnlyRequest(method, target))
			if rec.Code != http.StatusUnauthorized || rec.Body.String() != sensitiveRefusalBody {
				t.Fatalf("%s bypass-only: status = %d body = %q, want 401 %q", pattern, rec.Code, rec.Body.String(), sensitiveRefusalBody)
			}
		})
	}
}
