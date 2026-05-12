// IEEE-068 Enphase server runtime profile — smoke test.
//
// Proves the end-to-end runtime story works against the real Enphase
// factory PKI vendored under testdata/csip-pki/enphase/. Boots an
// in-process server in CCM mode with the Enphase root appended to
// ClientCAs, dials it with the Enphase device chain + key as the client
// identity, asserts the mTLS handshake completes, and walks GET /dcap.
//
// Unlike handshake_test.go (SunSpec V1.2 fixture, env-gated, t.Skip when
// missing), the Enphase materials are committed to testdata/ as test
// vendoring. This test runs unconditionally and is the on-disk proof
// that `make run-enphase` would handshake against the live device.
//
// CSIP §6.11 conformance: the Enphase leaf is NOT §6.11-compliant
// (no HardwareModuleName SAN, no Key Usage, no Basic Constraints,
// non-empty Subject CN=enphase). The server must run in non-strict
// cert verification mode — the default. SEP2_CSIP_STRICT=true (gated on
// IEEE-020) is incompatible with this device and would reject the
// handshake. This test therefore stays in the lax-mode test bucket and
// is documented as such in IEEE-068's PR description.

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

// enphasePKIRel is the path (relative to test/csip/) at which the
// vendored Enphase PKI lives. testdata/csip-pki/enphase/ is committed
// to the source repo per IEEE-068.
const enphasePKIRel = "../../testdata/csip-pki/enphase"

// TestEnphaseHandshake exercises the IEEE-068 cert-trust + handshake
// path against the real Enphase factory PKI.
func TestEnphaseHandshake(t *testing.T) {
	// Resolve the vendored PKI paths up-front so a clean-clone failure
	// surfaces here rather than mid-handshake.
	chainPath := filepath.Join(enphasePKIRel, "Enph_cert_chain.pem")
	keyPath := filepath.Join(enphasePKIRel, "Enph_key.pem")
	rootPath := filepath.Join(enphasePKIRel, "Enph_root.pem")
	for _, p := range []string{chainPath, keyPath, rootPath} {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("vendored Enphase PKI missing at %s: %v", p, err)
		}
	}

	clientCertPEM, err := os.ReadFile(chainPath)
	if err != nil {
		t.Fatalf("read Enphase chain: %v", err)
	}
	clientKeyPEM, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatalf("read Enphase key: %v", err)
	}
	clientCert, err := tls.X509KeyPair(clientCertPEM, clientKeyPEM)
	if err != nil {
		t.Fatalf("parse Enphase chain+key: %v", err)
	}

	// Boot the spec server in CCM mode with the Enphase root in
	// ClientCAs and the Enphase leaf as the presented client identity.
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
		t.Fatalf("tls.Dial (Enphase chain → server): %v", err)
	}
	state := rawConn.ConnectionState()
	if !state.HandshakeComplete {
		_ = rawConn.Close()
		t.Fatal("HandshakeComplete = false")
	}
	t.Logf("Enphase mTLS handshake OK: version=0x%04x cipher=0x%04x (%s) peerCerts=%d",
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

	// Minimum link set — same baseline COMM-002 asserts, mirrored here
	// so a regression that drops links under the Enphase trust path
	// fails this test too.
	if dcap.EndDeviceListLink == nil || dcap.EndDeviceListLink.Href == "" {
		t.Errorf("EndDeviceListLink.Href is empty")
	}
	if dcap.TimeLink == nil || dcap.TimeLink.Href == "" {
		t.Errorf("TimeLink.Href is empty")
	}
}
