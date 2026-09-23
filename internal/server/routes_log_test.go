package server_test

import (
	"strings"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/config"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/server"
)

// TestRoutesEnumerationConfirmsCertAPIAdminOnly is the #272
// defense-in-depth backstop for the Leon CRITICAL on PR #246. The
// route-enumeration helpers (BuildProtocolRouter, BuildAdminRouter)
// MUST report /api/certs/* patterns ONLY in the admin route list. A
// future accidental mount on the protocol router would surface here
// AND in the boot log on first run, before any external scanner has
// to find it.
//
// router_certs_scope_test.go pins the routing decision via end-to-end
// HTTP probes (404 on protocol, 401 on admin). This test pins the
// pattern strings themselves so a regression in the pattern list (the
// thing the boot log emits) lands as a targeted failure.
func TestRoutesEnumerationConfirmsCertAPIAdminOnly(t *testing.T) {
	t.Parallel()

	svc := newScopeTestCertService(t)
	cfg := &config.Config{AdminKey: "test-admin-key"}
	stores := newTestStores()

	_, protoRoutes := server.BuildProtocolRouter(cfg, stores, svc, "", "", nil)
	_, adminRoutes := server.BuildAdminRouter("test-admin-key", svc, stores, "", nil, nil, nil, false, nil)

	// Protocol routes MUST NOT contain any /api/certs/* pattern: a hit
	// here is the regression this test exists to prevent. Surface it
	// loudly with the offending pattern echoed back.
	for _, p := range protoRoutes {
		if strings.Contains(p, "/api/certs") {
			t.Errorf("protocol route list contains cert-API pattern %q; /api/certs/* must be admin-only (PR #246 Leon CRITICAL)", p)
		}
	}

	// Admin routes MUST contain the cert-API patterns the admin
	// listener is responsible for. We don't pin every method:pattern
	// shape here (the admin router gains routes over time) - we only
	// pin the load-bearing presence.
	wantAdminContains := []string{
		"/api/certs/ca",
		"/api/certs/server",
		"/api/certs/device",
		"/api/certs/info",
	}
	for _, want := range wantAdminContains {
		found := false
		for _, p := range adminRoutes {
			if strings.Contains(p, want) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("admin route list missing pattern containing %q\n---admin routes---\n%s", want, strings.Join(adminRoutes, "\n"))
		}
	}
}

// TestProtocolRoutesContainsCanonicalSEP2 pins a representative slice
// of the SEP2 protocol patterns so a future helper-signature drift
// (e.g. accidentally swapping a register*Routes site to a non-
// recording mux) surfaces as a missing route in the boot log instead
// of going silent.
func TestProtocolRoutesContainsCanonicalSEP2(t *testing.T) {
	t.Parallel()

	svc := newScopeTestCertService(t)
	cfg := &config.Config{AdminKey: "test-admin-key"}
	stores := newTestStores()

	_, protoRoutes := server.BuildProtocolRouter(cfg, stores, svc, "", "", nil)

	// Two from the static set (HandleDeviceCapability/HandleTime) and
	// two from the register*Routes helpers (EndDevice + Mirror) so a
	// regression in either path lands here.
	wantContains := []string{
		"GET /dcap",
		"GET /tm",
		"GET /edev", // registerEndDeviceRoutes
		"POST /mup", // registerMirrorRoutes
	}
	for _, want := range wantContains {
		found := false
		for _, p := range protoRoutes {
			if p == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("protocol route list missing canonical pattern %q\n---protocol routes---\n%s", want, strings.Join(protoRoutes, "\n"))
		}
	}

	// And the pattern list MUST be sorted (recordingMux.Patterns
	// sorts) so the boot log is diff-stable across runs.
	for i := 1; i < len(protoRoutes); i++ {
		if protoRoutes[i] < protoRoutes[i-1] {
			t.Errorf("protocol route list not sorted: %q precedes %q", protoRoutes[i-1], protoRoutes[i])
		}
	}
}

// TestAdminRoutesContainsLoginAndDashboard pins the public outer mux
// and the dashboard registration so the boot log reflects every
// admin-listener route, including the unauth login surface that
// #270 (bundle B) will later host-allowlist.
func TestAdminRoutesContainsLoginAndDashboard(t *testing.T) {
	t.Parallel()

	svc := newScopeTestCertService(t)
	stores := newTestStores()

	_, adminRoutes := server.BuildAdminRouter("test-admin-key", svc, stores, "", nil, nil, nil, false, nil)

	wantContains := []string{
		"GET /login",       // public outer mux
		"POST /auth/login", // public outer mux
		"GET /",            // dashboard
		"GET /dashboard/data",
	}
	for _, want := range wantContains {
		found := false
		for _, p := range adminRoutes {
			if p == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("admin route list missing canonical pattern %q\n---admin routes---\n%s", want, strings.Join(adminRoutes, "\n"))
		}
	}

	// Sorted for boot-log stability.
	for i := 1; i < len(adminRoutes); i++ {
		if adminRoutes[i] < adminRoutes[i-1] {
			t.Errorf("admin route list not sorted: %q precedes %q", adminRoutes[i-1], adminRoutes[i])
		}
	}
}

// TestRenderRoutesLogShape pins the boot-log block shape - header,
// per-listener sub-header with bind address, indented patterns. A
// future edit that softens the format (e.g. drops the bind address)
// or scrambles the ordering surfaces here.
func TestRenderRoutesLogShape(t *testing.T) {
	t.Parallel()

	out := server.RenderRoutesLog(
		":8443",
		[]string{"GET /dcap", "GET /tm"},
		"127.0.0.1:8444",
		[]string{"GET /login", "POST /auth/login"},
	)

	mustContain := []string{
		"Routes mounted:",
		"  protocol (:8443):",
		"    GET /dcap",
		"    GET /tm",
		"  admin (127.0.0.1:8444):",
		"    GET /login",
		"    POST /auth/login",
	}
	for _, want := range mustContain {
		if !strings.Contains(out, want) {
			t.Errorf("routes log missing fragment %q\n---out---\n%s", want, out)
		}
	}

	// Admin section omitted entirely when the listener is disabled.
	disabled := server.RenderRoutesLog(":8443", []string{"GET /dcap"}, "", nil)
	if strings.Contains(disabled, "admin") {
		t.Errorf("disabled-admin variant must not emit an admin section\n---out---\n%s", disabled)
	}
}
