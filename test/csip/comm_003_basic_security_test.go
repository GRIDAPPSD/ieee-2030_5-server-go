// CSIP V1.2 §5.3 — Basic Security (TLS cipher negotiation).
//
// SKELETON: This test runs under lax mode today and accepts CCM-8 (0xC0AE)
// OR GCM (0xC02B / 0xC02C). Once IEEE-020 (CSIP strict mode) lands and
// propagates SEP2_CSIP_STRICT=true through BootServer, this assertion
// tightens to CCM-8 only.
//
// TODO: tighten to CCM-8-only once SEP2_CSIP_STRICT lands (IEEE-020)
//
// Procedure (V1.2 §5.3 — Basic Security):
//   1. Client opens a TCP connection to the server and starts a TLS
//      handshake offering the cipher suites it supports.
//   2. Server selects a cipher suite from the offered set, completes
//      the handshake, and reports the negotiated cipher via
//      tls.ConnectionState.CipherSuite.
//   3. The negotiated cipher MUST be in the CSIP-permitted set.
//
// Under default BootServer() (GCM path, stdlib crypto/tls) the server
// is restricted to TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256 (0xC02B);
// see internal/tls/config.go. Once IEEE-019 / IEEE-020 land, the
// strict-mode BootServer will restrict to CCM-8 only and this test
// gains a strict-mode subtest.
//
// Why a raw tls.Dial and not the Client(): we want the negotiated
// CipherSuite without depending on csiptest internals or threading a
// custom RoundTripper through the public Client API. The cost is one
// independent CA + device-cert pair generated inline; both are
// ephemeral and t.TempDir-scoped.
//
// The assertion is intentionally loud: an unexpected cipher fails the
// test with the specific code that was negotiated. It is NOT a silent
// pass. If the BootServer cipher allow-list drifts to a non-CSIP
// cipher, this test FAILS until either the server is fixed or this
// test's accepted set is consciously widened.
package csip_test

import (
	"crypto/tls"
	"crypto/x509"
	"os"
	"path/filepath"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-go/internal/certs"
	sepTLS "github.com/GRIDAPPSD/ieee-2030_5-go/internal/tls"
	"github.com/GRIDAPPSD/ieee-2030_5-go/test/csip/csiptest"
)

// laxAcceptedCiphers is the lax-mode accepted set per the comment
// above: CCM-8 plus the two ECDHE-ECDSA GCM ciphers stdlib offers.
// IEEE-020 will narrow this to {CCM-8} only.
var laxAcceptedCiphers = map[uint16]string{
	sepTLS.TLS_ECDHE_ECDSA_WITH_AES_128_CCM_8:   "TLS_ECDHE_ECDSA_WITH_AES_128_CCM_8",
	tls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256: "TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256",
	tls.TLS_ECDHE_ECDSA_WITH_AES_256_GCM_SHA384: "TLS_ECDHE_ECDSA_WITH_AES_256_GCM_SHA384",
}

// TestCOMM_003_BasicSecurity_LaxSkeleton boots a server, performs a
// raw TLS dial against it, and asserts the negotiated cipher is in
// the lax-mode accepted set.
func TestCOMM_003_BasicSecurity_LaxSkeleton(t *testing.T) {
	t.Parallel()

	// Generate an independent CA + device-cert pair. We hand the CA
	// to BootServer via WithClientCAsFile so the server trusts our
	// dial; we keep the device cert + key in-memory for the dial.
	caCertPEM, caKeyPEM, devCertPEM, devKeyPEM := mustGenerateDialerPKI(t)

	caFile := filepath.Join(t.TempDir(), "dialer-ca.pem")
	if err := os.WriteFile(caFile, caCertPEM, 0o600); err != nil {
		t.Fatalf("write dialer CA: %v", err)
	}

	devCert, err := tls.X509KeyPair(devCertPEM, devKeyPEM)
	if err != nil {
		t.Fatalf("parse dialer device cert: %v", err)
	}

	srv := csiptest.BootServer(t,
		csiptest.WithClientCAsFile(caFile),
	)

	rootPool := x509.NewCertPool()
	if !rootPool.AppendCertsFromPEM(srv.RootCA) {
		t.Fatal("append server root CA to pool")
	}

	cfg := &tls.Config{
		Certificates: []tls.Certificate{devCert},
		RootCAs:      rootPool,
		ServerName:   "127.0.0.1",
		MinVersion:   tls.VersionTLS12,
		MaxVersion:   tls.VersionTLS12,
	}

	conn, err := tls.Dial("tcp", srv.Addr(), cfg)
	if err != nil {
		t.Fatalf("tls.Dial: %v", err)
	}
	defer func() { _ = conn.Close() }()

	state := conn.ConnectionState()
	if !state.HandshakeComplete {
		t.Fatal("HandshakeComplete = false")
	}

	// TODO: tighten to CCM-8-only once SEP2_CSIP_STRICT lands (IEEE-020)
	if _, ok := laxAcceptedCiphers[state.CipherSuite]; !ok {
		t.Errorf("negotiated cipher = 0x%04X (%s); want one of CCM-8 (0xC0AE), GCM-128 (0xC02B), GCM-256 (0xC02C)",
			state.CipherSuite, tls.CipherSuiteName(state.CipherSuite))
	}
	t.Logf("CSIP V1.2 §5.3 lax check: negotiated cipher 0x%04X (%s)",
		state.CipherSuite, tls.CipherSuiteName(state.CipherSuite))

	// Silence unused: caKeyPEM is retained intentionally so a future
	// strict-mode subtest can sign additional certs against the same
	// CA without re-generating the chain.
	_ = caKeyPEM
}

// mustGenerateDialerPKI returns (caCertPEM, caKeyPEM, deviceCertPEM,
// deviceKeyPEM) for an independent CA-signed device cert. The device
// cert carries the IEEE-2030.5 critical HardwareModuleName SAN so the
// server's verifier hook accepts it. t.Fatal on any failure.
func mustGenerateDialerPKI(t *testing.T) (caCertPEM, caKeyPEM, devCertPEM, devKeyPEM []byte) {
	t.Helper()

	caCertPEM, caKeyPEM, err := certs.GenerateCA(certs.CAOptions{
		CommonName: "CSIP COMM-003 Dialer CA",
		ValidYears: 1,
	})
	if err != nil {
		t.Fatalf("generate dialer CA: %v", err)
	}
	caCert, err := certs.ParseCertificatePEM(caCertPEM)
	if err != nil {
		t.Fatalf("parse dialer CA cert: %v", err)
	}
	caKey, err := certs.ParseKeyPEM(caKeyPEM)
	if err != nil {
		t.Fatalf("parse dialer CA key: %v", err)
	}
	devCertPEM, devKeyPEM, err = certs.GenerateDeviceCert(caCert, caKey, certs.DeviceCertOptions{
		DeviceType:  certs.DeviceTypeGeneric,
		HWSerialNum: "CSIP-COMM-003-DIALER",
	})
	if err != nil {
		t.Fatalf("generate dialer device cert: %v", err)
	}
	return caCertPEM, caKeyPEM, devCertPEM, devKeyPEM
}
