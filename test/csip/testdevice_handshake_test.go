// #75 CSIP device server runtime profile: smoke test.
//
// Proves the end-to-end runtime story works against self-minted test PKI
// under testdata/csip-pki/testdevice/. Boots an in-process server in CCM
// mode with the test root CA appended to ClientCAs, dials it with the
// test device chain and key as the client identity, asserts the mTLS
// handshake completes, and walks GET /dcap.
//
// The test device cert is CSIP section 6.11-compliant: it carries a critical
// HardwareModuleName SAN, an empty Subject, proper Key Usage, and Basic
// Constraints. The server runs in its default (non-strict) mode, which
// accepts both compliant and non-compliant device certs. Strict mode
// (SEP2_CSIP_STRICT=true, gated on #22) would also accept this cert.
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

	sepTLS "github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2tls"
	gotls "github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2tls/gotls"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/test/csip/csiptest"
)

// testdevicePKIRel is the path (relative to test/csip/) at which the
// self-minted test device PKI lives.
const testdevicePKIRel = "../../testdata/csip-pki/testdevice"

// testdeviceLFDI and testdeviceSFDI are the IEEE 2030.5 device identity
// values derived from the committed device_chain.pem leaf cert. They are
// pinned here so any PKI regeneration (which changes the key and therefore
// the hash) causes an explicit test failure and a forced README/fixture
// update rather than a silent drift.
const (
	testdeviceLFDI = "93E795AE91F5F493813E3B39C8BC49F06FCA6258"
	testdeviceSFDI = "397028461857"
)

// TestDeviceHandshake exercises the #75 cert-trust + handshake path
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

	// Derive and assert device identity from the committed leaf cert.
	// This pins the LFDI/SFDI so any unintended key regeneration is caught
	// here rather than surfacing as a subtler test drift downstream.
	if len(clientCert.Certificate) == 0 {
		t.Fatal("device cert chain is empty after parse")
	}
	leafDER := clientCert.Certificate[0]
	leaf, err := x509.ParseCertificate(leafDER)
	if err != nil {
		t.Fatalf("parse leaf cert DER: %v", err)
	}
	gotLFDI := sepTLS.LFDI(leaf)
	gotSFDI := sepTLS.SFDI(leaf)
	if gotLFDI != testdeviceLFDI {
		t.Errorf("device LFDI = %q, want %q (was the PKI regenerated without updating the fixture?)", gotLFDI, testdeviceLFDI)
	}
	if gotSFDI != testdeviceSFDI {
		t.Errorf("device SFDI = %q, want %q (was the PKI regenerated without updating the fixture?)", gotSFDI, testdeviceSFDI)
	}

	// Boot the spec server with the test root CA in ClientCAs and the
	// test device leaf as the presented client identity. The server
	// offers CCM-8 only.
	srv := csiptest.BootServer(t,
		csiptest.WithClientCert(clientCert),
		csiptest.WithClientCAsFile(rootPath),
	)

	// Raw-dial probe so we can assert the negotiated cipher and prove the
	// mTLS handshake completed independent of the HTTP layer. Dials
	// through the fork since the server offers CCM-8 only.
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
		t.Fatalf("gotls.Dial (test device chain to server): %v", err)
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
	t.Logf("test device mTLS handshake OK: version=0x%04x cipher=0x%04x peerCerts=%d LFDI=%s SFDI=%s",
		state.Version, state.CipherSuite,
		len(state.PeerCertificates), gotLFDI, gotSFDI)
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
