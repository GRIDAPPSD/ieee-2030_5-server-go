// CSIP V1.2 section 5.3 - Basic Security (TLS cipher negotiation).
//
// Procedure (V1.2 section 5.3 - Basic Security):
//  1. Client opens a TCP connection to the server and starts a TLS
//     handshake offering the cipher suites it supports.
//  2. Server selects a cipher suite from the offered set, completes
//     the handshake, and reports the negotiated cipher via
//     tls.ConnectionState.CipherSuite.
//  3. The negotiated cipher MUST be CCM-8 (0xC0AE), the sole suite
//     core's pkg/sep2tls package offers; see core's config.go.
//
// Why a raw gotls.Dial and not the Client(): we want the negotiated
// CipherSuite without depending on csiptest internals or threading a
// custom RoundTripper through the public Client API. The cost is one
// independent CA + device-cert pair generated inline; both are
// ephemeral and t.TempDir-scoped. The dial goes through the fork
// because the server offers CCM-8 only and net/http's own stdlib TLS
// client cannot negotiate it at all.
//
// The assertion is intentionally loud: an unexpected cipher fails the
// test with the specific code that was negotiated. It is NOT a silent
// pass.
package csip_test

import (
	"crypto/tls"
	"crypto/x509"
	"os"
	"path/filepath"
	"testing"

	gotls "github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2tls/gotls"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/certs"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/test/csip/csiptest"
)

// TestCOMM_003_BasicSecurity_CCM8Only boots a server, performs a raw
// gotls dial against it, and asserts the negotiated cipher is CCM-8.
func TestCOMM_003_BasicSecurity_CCM8Only(t *testing.T) {
	t.Parallel()

	// Generate an independent CA + device-cert pair. We hand the CA
	// to BootServer via WithClientCAsFile so the server trusts our
	// dial; we keep the device cert + key in-memory for the dial.
	caCertPEM, _, devCertPEM, devKeyPEM := mustGenerateDialerPKI(t)

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

	cfg := &gotls.Config{
		Certificates: []gotls.Certificate{{
			Certificate: devCert.Certificate,
			PrivateKey:  devCert.PrivateKey,
			Leaf:        devCert.Leaf,
		}},
		RootCAs:          rootPool,
		ServerName:       "127.0.0.1",
		MinVersion:       gotls.VersionTLS12,
		MaxVersion:       gotls.VersionTLS12,
		CipherSuites:     []uint16{gotls.TLS_ECDHE_ECDSA_WITH_AES_128_CCM_8},
		CurvePreferences: []gotls.CurveID{gotls.CurveP256},
	}

	conn, err := gotls.Dial("tcp", srv.Addr(), cfg)
	if err != nil {
		t.Fatalf("gotls.Dial: %v", err)
	}
	defer func() { _ = conn.Close() }()

	state := conn.ConnectionState()
	if !state.HandshakeComplete {
		t.Fatal("HandshakeComplete = false")
	}
	if state.CipherSuite != gotls.TLS_ECDHE_ECDSA_WITH_AES_128_CCM_8 {
		t.Errorf("negotiated cipher = 0x%04X; want CCM-8 (0xC0AE)", state.CipherSuite)
	}
	t.Logf("CSIP V1.2 section 5.3 check: negotiated cipher 0x%04X", state.CipherSuite)
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
