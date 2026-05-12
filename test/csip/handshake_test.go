// Package csip_test holds the IEEE-2030.5 CSIP conformance harness.
//
// Smoke-level coverage today: one test, TestCSIPHandshakeWithSunSpecDeviceCert,
// proves the cert path + verifier hook + mTLS handshake end-to-end against
// real CSIP §6.11 external materials (SunSpec V1.2 test PKI). It does NOT
// (yet) cover the 25 SunSpec V1.2 conformance requirements — those land
// once the scaffold is in place.
//
// Fixtures are provisioned out of band; see test/csip/README.md. The test
// skips cleanly when fixtures are unavailable so fresh clones never fail.
package csip_test

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"os"
	"path/filepath"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-go/test/csip/csiptest"
)

// Fixture resolution: env first, then default to test/csip/fixtures/sunspec/.
const (
	envCert  = "CSIP_SUNSPEC_CERT"
	envKey   = "CSIP_SUNSPEC_KEY"
	envRoots = "CSIP_SUNSPEC_ROOTS"
)

// TestCSIPHandshakeWithSunSpecDeviceCert exercises the full CSIP-cipher-aware
// mTLS handshake against an in-process spec server running in CCM mode, using
// the SunSpec V1.2 test device cert as the external client.
//
// What this test asserts today:
//   - The server's verifier hook (verifyClientCertWithHardwareModuleSAN)
//     accepts the SunSpec leaf's critical HardwareModuleName SAN.
//   - The chain validates to the SunSpec Test 2030.5 Root (loaded into
//     ClientCAs via roots.pem).
//   - The mTLS handshake completes.
//   - GET /dcap returns HTTP 200 and a parseable DeviceCapability XML body.
//
// What this test DOES NOT assert (deferred):
//   - Negotiated cipher suite. The test logs it but does not require CCM-8.
//     The stdlib http.Client used here cannot offer CCM-8; tightening this
//     assertion is gated on IEEE-019 (move the client onto vendored gotls)
//     and IEEE-020 (SEP2_CSIP_STRICT=true server mode that drops the GCM
//     fallback). When both land, this test (or a sibling) asserts CCM-8.
//
// This test consumes csiptest.BootServer (IEEE-058) for the server boot
// and csiptest.Client.GetDeviceCapability (IEEE-056) for the application
// fetch. Together they prove both helpers are wired into a real
// integration test, not just defined in isolation.
func TestCSIPHandshakeWithSunSpecDeviceCert(t *testing.T) {
	certPath, keyPath, rootsPath, ok := resolveFixtures()
	if !ok {
		t.Skip("CSIP fixtures not provisioned; see test/csip/README.md")
	}

	// Load the SunSpec device cert + key as a tls.Certificate. The
	// helper presents this on every request via WithClientCert; the
	// raw-dial probe below builds its own client config from the same
	// material.
	clientCertPEM, err := os.ReadFile(certPath)
	if err != nil {
		t.Fatalf("read SunSpec cert: %v", err)
	}
	clientKeyPEM, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatalf("read SunSpec key: %v", err)
	}
	clientCert, err := tls.X509KeyPair(clientCertPEM, clientKeyPEM)
	if err != nil {
		t.Fatalf("parse SunSpec cert+key: %v", err)
	}

	// Boot the spec server in CCM mode with SunSpec roots in ClientCAs
	// and the SunSpec leaf as the client identity. csiptest owns the
	// listener, http.Server, and shutdown — all via t.Cleanup.
	srv := csiptest.BootServer(t,
		csiptest.WithCCMMode(),
		csiptest.WithClientCert(clientCert),
		csiptest.WithClientCAsFile(rootsPath),
	)

	// Raw-dial probe so we log cipher negotiation independently from
	// the HTTP layer. The probe builds its own client TLS config from
	// the server's published ephemeral CA + the same SunSpec cert
	// presented by the booted Client.
	rootPool := x509.NewCertPool()
	if !rootPool.AppendCertsFromPEM(srv.RootCA) {
		t.Fatal("append helper-supplied root CA to pool")
	}
	probeCfg := &tls.Config{
		Certificates: []tls.Certificate{clientCert},
		RootCAs:      rootPool,
		ServerName:   "127.0.0.1",
		MinVersion:   tls.VersionTLS12,
		MaxVersion:   tls.VersionTLS12,
	}
	rawConn, err := tls.Dial("tcp", srv.Addr(), probeCfg)
	if err != nil {
		t.Fatalf("tls.Dial: %v", err)
	}
	state := rawConn.ConnectionState()
	if !state.HandshakeComplete {
		_ = rawConn.Close()
		t.Fatal("HandshakeComplete = false")
	}
	t.Logf("TLS handshake OK: version=0x%04x cipher=0x%04x (%s) peerCerts=%d",
		state.Version, state.CipherSuite, tls.CipherSuiteName(state.CipherSuite), len(state.PeerCertificates))
	_ = rawConn.Close()

	// Application fetch via the csiptest Client. This proves the
	// chained-GET helper (IEEE-056) and BootServer (IEEE-058) compose
	// — future Phase 3 tests use Client.WalkLink to chain further
	// (dcap → /edev → /edev/0/rg, etc.).
	dcap, err := srv.Client().GetDeviceCapability(context.Background())
	if err != nil {
		t.Fatalf("GetDeviceCapability: %v", err)
	}
	if dcap.Href != "/dcap" {
		t.Errorf("DeviceCapability.Href = %q, want /dcap", dcap.Href)
	}
}

// resolveFixtures returns (cert, key, roots, ok). ok is false when none of
// the three paths resolve to an existing file. Env vars override the default
// test/csip/fixtures/sunspec/ paths, evaluated per-variable.
func resolveFixtures() (certPath, keyPath, rootsPath string, ok bool) {
	cert := fixturePath(envCert, "cert.pem")
	key := fixturePath(envKey, "key.pem")
	roots := fixturePath(envRoots, "roots.pem")
	for _, p := range []string{cert, key, roots} {
		if _, err := os.Stat(p); err != nil {
			return "", "", "", false
		}
	}
	return cert, key, roots, true
}

func fixturePath(env, leaf string) string {
	if v := os.Getenv(env); v != "" {
		return v
	}
	return filepath.Join("fixtures", "sunspec", leaf)
}
