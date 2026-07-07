package server_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/config"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/handler"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/server"
)

// TestProtocolListenerDoesNotMountCertAPI guards Leon's CRITICAL on
// PR #246: the loopback bypass in AdminAuthMiddleware (Path 0) admits
// any request that arrives over loopback with no proxy headers. The
// SEP2 protocol listener (cfg.Addr, default :443) accepts ANY
// self-signed client cert via tls.RequireAnyClientCert, so a
// co-resident process could mint a throwaway cert, hit
// https://127.0.0.1:443/api/certs/server with no XFF, and Path 0 would
// admit. The fix: /api/certs/* is mounted ONLY on the admin listener's
// router (BuildAdminRouter), never on the protocol listener's router
// (BuildProtocolRouter). This test pins that routing decision.
func TestProtocolListenerDoesNotMountCertAPI(t *testing.T) {
	svc := newScopeTestCertService(t)
	cfg := &config.Config{AdminKey: "test-admin-key"}
	stores := newTestStores()

	router, _ := server.BuildProtocolRouter(cfg, stores, svc, "", "", nil)

	cases := []struct {
		name   string
		method string
		path   string
		body   string
	}{
		{"GET /api/certs/ca", http.MethodGet, "/api/certs/ca", ""},
		{"POST /api/certs/server", http.MethodPost, "/api/certs/server", `{"hosts":["127.0.0.1"]}`},
		{"POST /api/certs/device", http.MethodPost, "/api/certs/device", `{"deviceType":"generic"}`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var body *strings.Reader
			if tc.body != "" {
				body = strings.NewReader(tc.body)
			} else {
				body = strings.NewReader("")
			}
			// RemoteAddr is loopback with no proxy headers — this is
			// exactly the Path 0 bypass shape. If the protocol-listener
			// router still mounted /api/certs/*, the cert handler would
			// fire and we'd see 200/400. We require 404 (mux miss).
			req := httptest.NewRequest(tc.method, tc.path, body)
			req.RemoteAddr = "127.0.0.1:54321"
			rec := httptest.NewRecorder()

			router.ServeHTTP(rec, req)

			if rec.Code != http.StatusNotFound {
				t.Fatalf("%s %s on protocol listener: status = %d, want 404 (cert API must not be mounted on protocol listener); body = %q",
					tc.method, tc.path, rec.Code, rec.Body.String())
			}
		})
	}
}

// TestAdminListenerStillMountsCertAPI confirms the legitimate path —
// the admin listener's router DOES mount /api/certs/* and serves them
// behind AdminAuthMiddleware. We don't authenticate here; we only need
// to prove the routes exist on the admin mux (a 401 from the auth
// layer is sufficient — it means the route was matched and handed to
// AdminAuthMiddleware).
func TestAdminListenerStillMountsCertAPI(t *testing.T) {
	svc := newScopeTestCertService(t)
	stores := newTestStores()

	adminRouter, _ := server.BuildAdminRouter("test-admin-key", svc, stores, "", nil, nil)

	cases := []struct {
		name   string
		method string
		path   string
	}{
		{"GET /api/certs/ca", http.MethodGet, "/api/certs/ca"},
		{"POST /api/certs/server", http.MethodPost, "/api/certs/server"},
		{"POST /api/certs/device", http.MethodPost, "/api/certs/device"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, tc.path, strings.NewReader(""))
			// Force a non-loopback RemoteAddr so Path 0 (loopback bypass)
			// does not admit — we want to see the auth layer reject,
			// which proves the route exists and is guarded.
			req.RemoteAddr = "10.0.0.5:1234"
			rec := httptest.NewRecorder()

			adminRouter.ServeHTTP(rec, req)

			if rec.Code == http.StatusNotFound {
				t.Fatalf("%s %s on admin listener: status = 404 (route missing); cert API must remain mounted on admin listener",
					tc.method, tc.path)
			}
			if rec.Code != http.StatusUnauthorized {
				t.Logf("%s %s admin listener: status = %d (expected 401 from AdminAuthMiddleware; non-404 is acceptable as long as the route exists)",
					tc.method, tc.path, rec.Code)
			}
		})
	}
}

// newScopeTestCertService builds a minimal AdminCertService with a
// freshly-generated self-signed CA. Kept local to this test file to
// avoid coupling to internal/handler test helpers (which live in a
// different test package).
func newScopeTestCertService(t *testing.T) *handler.AdminCertService {
	t.Helper()
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate CA key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "scope-test CA"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatalf("create CA cert: %v", err)
	}
	caCert, err := x509.ParseCertificate(caDER)
	if err != nil {
		t.Fatalf("parse CA cert: %v", err)
	}
	caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER})
	return handler.NewAdminCertService(caCert, caKey, caPEM)
}
