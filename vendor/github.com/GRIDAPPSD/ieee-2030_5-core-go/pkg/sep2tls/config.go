package sep2tls

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"
)

// IEEE 2030.5 cipher suite IDs
const (
	// TLS_ECDHE_ECDSA_WITH_AES_128_CCM_8 is the mandatory cipher suite
	// per IEEE 2030.5-2018 section 6.7. Go's crypto/tls does not support
	// CCM mode (see golang/go#27484). We use GCM as a fallback.
	TLS_ECDHE_ECDSA_WITH_AES_128_CCM_8 uint16 = 0xC0AE

	// TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256 is the fallback cipher
	// supported by Go's standard library.
	TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256 = tls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256
)

// NewServerTLSConfig creates a TLS configuration for the IEEE 2030.5 server.
// It enforces mutual TLS with ECDSA P-256 certificates.
//
// Equivalent to NewServerTLSConfigWithExtraCAs with no extra roots.
func NewServerTLSConfig(certFile, keyFile, caFile string) (*tls.Config, error) {
	return NewServerTLSConfigWithExtraCAs(certFile, keyFile, caFile, nil)
}

// NewServerTLSConfigWithExtraCAs is like NewServerTLSConfig but appends
// additional client-CA roots from extraCAFiles into the ClientCAs pool.
// Use this to trust device certs issued under multiple CSIP test roots
// (e.g. SunSpec PKI plus Enphase test PKI) at the same listener.
//
// A nil or empty extraCAFiles slice is the no-op case (matches
// NewServerTLSConfig behavior). Empty strings inside the slice are
// tolerated (treated as no-op), accommodating trailing-comma env values.
func NewServerTLSConfigWithExtraCAs(certFile, keyFile, caFile string, extraCAFiles []string) (*tls.Config, error) {
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return nil, fmt.Errorf("load server cert: %w", err)
	}

	caPool, err := LoadClientCAs(caFile, extraCAFiles)
	if err != nil {
		return nil, fmt.Errorf("load client CAs: %w", err)
	}

	return &tls.Config{
		Certificates: []tls.Certificate{cert},
		ClientCAs:    caPool,
		// IEEE 2030.5 section 6.11 / CSIP section 6.2 device certs carry a critical
		// HardwareModuleName SAN that stdlib x509 leaves in
		// UnhandledCriticalExtensions, which would cause RequireAndVerify
		// to fail closed at handshake. RequireAnyClientCert is intentional,
		// not a weakening: the full chain walk (signature, expiry, basic
		// constraints, key usage, trust anchor) runs in VerifyPeerCertificate
		// below via VerifyPeerCertWithHardwareModuleSAN, after the HMN OID
		// is acknowledged. See pkg/sep2tls/verify.go and tests
		// TestVerifyRejectsCertSignedByDifferentCA, TestMutualTLSHandshake.
		ClientAuth: tls.RequireAnyClientCert,
		VerifyPeerCertificate: func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
			return VerifyPeerCertWithHardwareModuleSAN(rawCerts, caPool)
		},
		// IEEE 2030.5-2018 clauses 6.1 and 6.4 (and IEEE 2030.5-2023) specify
		// TLS 1.2; no server configuration accepts TLS 1.3. No exported
		// field, option, or environment variable raises MaxVersion.
		MinVersion: tls.VersionTLS12,
		MaxVersion: tls.VersionTLS12,
		CipherSuites: []uint16{
			tls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256,
		},
		CurvePreferences: []tls.CurveID{tls.CurveP256},
		// Tickets off: a resumed session skips VerifyPeerCertificate above,
		// bypassing the HardwareModuleName SAN check.
		SessionTicketsDisabled: true,
	}, nil
}

// NewClientTLSConfig creates a TLS configuration for an IEEE 2030.5 client.
func NewClientTLSConfig(certFile, keyFile, caFile string) (*tls.Config, error) {
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return nil, fmt.Errorf("load client cert: %w", err)
	}

	caCertPEM, err := os.ReadFile(caFile)
	if err != nil {
		return nil, fmt.Errorf("read CA cert: %w", err)
	}

	caPool := x509.NewCertPool()
	if !caPool.AppendCertsFromPEM(caCertPEM) {
		return nil, fmt.Errorf("failed to parse CA certificate")
	}

	return &tls.Config{
		Certificates: []tls.Certificate{cert},
		RootCAs:      caPool,
		MinVersion:   tls.VersionTLS12,
		MaxVersion:   tls.VersionTLS12,
		CipherSuites: []uint16{
			tls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256,
		},
		CurvePreferences: []tls.CurveID{tls.CurveP256},
	}, nil
}

// NewServerTLSConfigFromPEM creates a TLS config from PEM byte slices
// instead of file paths. Useful for testing.
func NewServerTLSConfigFromPEM(certPEM, keyPEM, caPEM []byte) (*tls.Config, error) {
	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return nil, fmt.Errorf("parse server cert: %w", err)
	}

	caPool := x509.NewCertPool()
	if !caPool.AppendCertsFromPEM(caPEM) {
		return nil, fmt.Errorf("failed to parse CA certificate")
	}

	return &tls.Config{
		Certificates: []tls.Certificate{cert},
		ClientCAs:    caPool,
		ClientAuth:   tls.RequireAnyClientCert,
		VerifyPeerCertificate: func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
			return VerifyPeerCertWithHardwareModuleSAN(rawCerts, caPool)
		},
		// See the matching comment in NewServerTLSConfigWithExtraCAs above:
		// the TLS 1.2 cap is unconditional.
		MinVersion: tls.VersionTLS12,
		MaxVersion: tls.VersionTLS12,
		CipherSuites: []uint16{
			tls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256,
		},
		CurvePreferences: []tls.CurveID{tls.CurveP256},
		// Tickets off: a resumed session skips VerifyPeerCertificate above,
		// bypassing the HardwareModuleName SAN check.
		SessionTicketsDisabled: true,
	}, nil
}

// NewClientTLSConfigFromPEM creates a client TLS config from PEM byte slices.
func NewClientTLSConfigFromPEM(certPEM, keyPEM, caPEM []byte) (*tls.Config, error) {
	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return nil, fmt.Errorf("parse client cert: %w", err)
	}

	caPool := x509.NewCertPool()
	if !caPool.AppendCertsFromPEM(caPEM) {
		return nil, fmt.Errorf("failed to parse CA certificate")
	}

	return &tls.Config{
		Certificates: []tls.Certificate{cert},
		RootCAs:      caPool,
		MinVersion:   tls.VersionTLS12,
		MaxVersion:   tls.VersionTLS12,
		CipherSuites: []uint16{
			tls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256,
		},
		CurvePreferences: []tls.CurveID{tls.CurveP256},
	}, nil
}
