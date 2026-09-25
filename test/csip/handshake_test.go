// This file (handshake_test.go) is the SunSpec-PKI external-cert smoke:
// it proves the cert path + verifier hook + mTLS handshake end-to-end
// against real CSIP section 6.11 external materials (SunSpec V1.2 test PKI),
// in CCM-8 cipher mode. Its fixtures are provisioned out of band (see
// test/csip/README.md); the test t.Skip's cleanly when they are missing
// so fresh clones never fail. CSIP_SUNSPEC_REQUIRED turns that skip into a
// failure; see fixture_gate_test.go.
//
// The CSIP-named conformance counterparts for V1.2 section 5.2 Out-of-Band
// Discovery and V1.2 section 5.3 Basic Security live alongside this file:
//
//   - comm_002_oob_discovery_test.go (#62) - V1.2 section 5.2, runs
//     unconditionally against an ephemeral PKI booted by
//     csiptest.BootServer. Satisfies the COMM-002 line item in the
//     Phase 3 V1.2 coverage matrix.
//   - comm_003_basic_security_test.go (#62) - V1.2 section 5.3,
//     asserts CCM-8, the only suite the spec server offers.
//
// This SunSpec smoke is kept on top of those two because it is the
// only test in the package that exercises the real external CSIP test
// PKI - a different signal from the ephemeral-PKI conformance tests.
package csip_test

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"os"
	"testing"

	gotls "github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2tls/gotls"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/test/csip/csiptest"
)

// TestCSIPHandshakeWithSunSpecDeviceCert exercises the full CSIP-cipher-aware
// mTLS handshake against an in-process spec server, using the SunSpec V1.2
// test device cert as the external client. The server offers CCM-8 only
// (core's sep2tls package has no other cipher suite), so this asserts the
// negotiated suite rather than only logging it.
//
// What this test asserts today:
//   - The SunSpec leaf carries a critical, otherName-only
//     HardwareModuleName SAN, per IEEE 2030.5 6.11 / CSIP 6.2. That is
//     this test's own requirement, not the handshake's: the server hook
//     sep2tls.VerifyPeerCertWithHardwareModuleSAN acknowledges such a SAN
//     when one is present but never requires one, so a leaf with no SAN
//     at all still completes every other step below.
//   - The chain validates to the SunSpec Test 2030.5 Root (loaded into
//     ClientCAs via roots.pem).
//   - The mTLS handshake completes and negotiates CCM-8.
//   - GET /dcap returns HTTP 200 and a parseable DeviceCapability XML body.
//
// This test consumes csiptest.BootServer (#53) for the server boot
// and csiptest.Client.GetDeviceCapability (#51) for the application
// fetch. Together they prove both helpers are wired into a real
// integration test, not just defined in isolation.
func TestCSIPHandshakeWithSunSpecDeviceCert(t *testing.T) {
	certPath, keyPath, rootsPath := mustResolveFixtures(t)

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
	leaf, err := x509.ParseCertificate(clientCert.Certificate[0])
	if err != nil {
		t.Fatalf("parse SunSpec leaf: %v", err)
	}

	// Checked here rather than left to the handshake: the hook below
	// acknowledges a HardwareModuleName SAN without requiring one, so
	// nothing further in this test would notice its absence.
	if err := csipDeviceCertSAN(leaf); err != nil {
		t.Fatalf("SunSpec leaf at %s: %v", certPath, err)
	}

	// Boot the spec server with SunSpec roots in ClientCAs and the
	// SunSpec leaf as the client identity. csiptest owns the listener,
	// http.Server, and shutdown - all via t.Cleanup.
	srv := csiptest.BootServer(t,
		csiptest.WithClientCert(clientCert),
		csiptest.WithClientCAsFile(rootsPath),
	)

	// Raw-dial probe so we assert cipher negotiation independently from
	// the HTTP layer. The probe builds its own client TLS config from
	// the server's published ephemeral CA + the same SunSpec cert
	// presented by the booted Client, and dials through the fork since
	// the server offers CCM-8 only.
	rootPool := x509.NewCertPool()
	if !rootPool.AppendCertsFromPEM(srv.RootCA) {
		t.Fatal("append helper-supplied root CA to pool")
	}
	probeCfg := &gotls.Config{
		Certificates: []gotls.Certificate{{
			Certificate: clientCert.Certificate,
			PrivateKey:  clientCert.PrivateKey,
			Leaf:        clientCert.Leaf,
		}},
		RootCAs:          rootPool,
		ServerName:       "127.0.0.1",
		MinVersion:       gotls.VersionTLS12,
		MaxVersion:       gotls.VersionTLS12,
		CipherSuites:     []uint16{gotls.TLS_ECDHE_ECDSA_WITH_AES_128_CCM_8},
		CurvePreferences: []gotls.CurveID{gotls.CurveP256},
	}
	rawConn, err := gotls.Dial("tcp", srv.Addr(), probeCfg)
	if err != nil {
		t.Fatalf("gotls.Dial: %v", err)
	}
	state := rawConn.ConnectionState()
	if !state.HandshakeComplete {
		_ = rawConn.Close()
		t.Fatal("HandshakeComplete = false")
	}
	if state.CipherSuite != gotls.TLS_ECDHE_ECDSA_WITH_AES_128_CCM_8 {
		_ = rawConn.Close()
		t.Fatalf("negotiated cipher = 0x%04x, want CCM-8 (0x%04x)", state.CipherSuite, gotls.TLS_ECDHE_ECDSA_WITH_AES_128_CCM_8)
	}
	t.Logf("TLS handshake OK: version=0x%04x cipher=0x%04x peerCerts=%d",
		state.Version, state.CipherSuite, len(state.PeerCertificates))
	_ = rawConn.Close()

	// Application fetch via the csiptest Client. This proves the
	// chained-GET helper (#51) and BootServer (#53) compose
	// - future Phase 3 tests use Client.WalkLink to chain further
	// (dcap -> /edev -> /edev/0/rg, etc.).
	dcap, err := srv.Client().GetDeviceCapability(context.Background())
	if err != nil {
		t.Fatalf("GetDeviceCapability: %v", err)
	}
	if dcap.Href != "/dcap" {
		t.Errorf("DeviceCapability.Href = %q, want /dcap", dcap.Href)
	}
}
