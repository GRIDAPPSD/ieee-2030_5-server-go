package tls

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
func NewServerTLSConfig(certFile, keyFile, caFile string) (*tls.Config, error) {
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return nil, fmt.Errorf("load server cert: %w", err)
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
		ClientCAs:    caPool,
		// IEEE 2030.5 / CSIP device certs carry a critical HardwareModuleName
		// SAN that stdlib x509 leaves in UnhandledCriticalExtensions. Switch
		// to RequireAnyClientCert + manual verification so we can acknowledge
		// that OID before chain validation.
		ClientAuth: tls.RequireAnyClientCert,
		VerifyPeerCertificate: func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
			return VerifyPeerCertWithHardwareModuleSAN(rawCerts, caPool)
		},
		MinVersion: tls.VersionTLS12,
		MaxVersion: tls.VersionTLS12,
		CipherSuites: []uint16{
			tls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256,
		},
		CurvePreferences: []tls.CurveID{tls.CurveP256},
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
		MinVersion: tls.VersionTLS12,
		MaxVersion: tls.VersionTLS12,
		CipherSuites: []uint16{
			tls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256,
		},
		CurvePreferences: []tls.CurveID{tls.CurveP256},
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
