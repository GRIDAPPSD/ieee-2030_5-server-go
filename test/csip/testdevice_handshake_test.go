// IEEE-068 CSIP device server runtime profile: smoke test.
//
// Proves the end-to-end runtime story works against self-minted test PKI
// under testdata/csip-pki/testdevice/. Boots an in-process server in CCM
// mode with the test root CA appended to ClientCAs, dials it with the
// test device chain and key as the client identity, asserts the mTLS
// handshake completes, and walks GET /dcap.
//
// The test device cert is CSIP §6.11-compliant: it carries a critical
// HardwareModuleName SAN, an empty Subject, proper Key Usage, and Basic
// Constraints. The server runs in its default (non-strict) mode, which
// accepts both compliant and non-compliant device certs. Strict mode
// (SEP2_CSIP_STRICT=true, gated on IEEE-020) would also accept this cert.
//
// This test runs unconditionally: the PKI material is committed to
// testdata/ and has no external dependency.

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

// testdevicePKIRel is the path (relative to test/csip/) at which the
// self-minted test device PKI lives.
const testdevicePKIRel = "../../testdata/csip-pki/testdevice"

// TestDeviceHandshake exercises the IEEE-068 cert-trust + handshake path
// against the self-minted test device PKI.
func TestDeviceHandshake(t *testing.T) {
	// Resolve the PKI paths up-front so a clean-clone failure surfaces
	// here rather than mid-handshake.
	chainPath := filepath.Join(testdevicePKIRel, "device_chain.pem")
	keyPath := filepath.Join(testdevicePKIRel, "device_key.pem")
	rootPath := filepath.Join(testdevicePKIRel, "root_ca.pem")
	for _, p := range []string{chainPath, keyPath, rootPath} {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("test device PKI missing at %s: %v", p, err)
		}
	}

	clientCertPEM, err := os.ReadFile(chainPath)
	if err != nil {
		t.Fatalf("read device chain: %v", err)
	}
	clientKeyPEM, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatalf("read device key: %v", err)
	}
	clientCert, err := tls.X509KeyPair(clientCertPEM, clientKeyPEM)
	if err != nil {
		t.Fatalf("parse device chain+key: %v", err)
	}

	// Boot the spec server in CCM mode with the test root CA in
	// ClientCAs and the test device leaf as the presented client identity.
	srv := csiptest.BootServer(t,
		csiptest.WithCCMMode(),
		csiptest.WithClientCert(clientCert),
		csiptest.WithClientCAsFile(rootPath),
	)

	// Raw-dial probe so we can log the negotiated cipher and prove the
	// mTLS handshake completed independent of the HTTP layer.
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
		t.Fatalf("tls.Dial (test device chain to server): %v", err)
	}
	state := rawConn.ConnectionState()
	if !state.HandshakeComplete {
		_ = rawConn.Close()
		t.Fatal("HandshakeComplete = false")
	}
	t.Logf("test device mTLS handshake OK: version=0x%04x cipher=0x%04x (%s) peerCerts=%d",
		state.Version, state.CipherSuite, tls.CipherSuiteName(state.CipherSuite), len(state.PeerCertificates))
	_ = rawConn.Close()

	// Application fetch: /dcap must return a parseable DeviceCapability.
	dcap, err := srv.Client().GetDeviceCapability(context.Background())
	if err != nil {
		t.Fatalf("GetDeviceCapability: %v", err)
	}
	if dcap.Href != "/dcap" {
		t.Errorf("DeviceCapability.Href = %q, want %q", dcap.Href, "/dcap")
	}

	// Minimum link set: EndDeviceListLink and TimeLink must be populated.
	// This mirrors the baseline COMM-002 check and catches regressions that
	// drop required links under the test-device trust path.
	if dcap.EndDeviceListLink == nil || dcap.EndDeviceListLink.Href == "" {
		t.Errorf("EndDeviceListLink.Href is empty")
	}
	if dcap.TimeLink == nil || dcap.TimeLink.Href == "" {
		t.Errorf("TimeLink.Href is empty")
	}
}
