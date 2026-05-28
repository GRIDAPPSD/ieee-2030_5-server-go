package tls

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net"
	"net/http"
	"os"

	gotls "github.com/GRIDAPPSD/ieee-2030_5-go/internal/tls/gotls"
)

type ccmStateKey struct{}

// NewCCMServerConfig creates a gotls.Config with CCM-8 as primary cipher
// and GCM as fallback for compatibility.
//
// Equivalent to NewCCMServerConfigWithExtraCAs with no extra roots.
func NewCCMServerConfig(certFile, keyFile, caFile string) (*gotls.Config, error) {
	return NewCCMServerConfigWithExtraCAs(certFile, keyFile, caFile, nil)
}

// NewCCMServerConfigWithExtraCAs is like NewCCMServerConfig but appends
// additional client-CA roots from extraCAFiles into the ClientCAs pool.
// See NewServerTLSConfigWithExtraCAs (config.go) for the multi-root
// rationale and slice semantics.
func NewCCMServerConfigWithExtraCAs(certFile, keyFile, caFile string, extraCAFiles []string) (*gotls.Config, error) {
	certPEM, err := os.ReadFile(certFile)
	if err != nil {
		return nil, fmt.Errorf("read cert: %w", err)
	}
	keyPEM, err := os.ReadFile(keyFile)
	if err != nil {
		return nil, fmt.Errorf("read key: %w", err)
	}

	cert, err := gotls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return nil, fmt.Errorf("parse cert: %w", err)
	}

	caPool, err := LoadClientCAs(caFile, extraCAFiles)
	if err != nil {
		return nil, fmt.Errorf("load client CAs: %w", err)
	}

	return &gotls.Config{
		Certificates: []gotls.Certificate{cert},
		ClientCAs:    caPool,
		// IEEE 2030.5 §6.11 / CSIP §6.2 device certs carry a critical
		// HardwareModuleName SAN that stdlib x509 leaves in
		// UnhandledCriticalExtensions, which would cause RequireAndVerify
		// to fail closed at handshake. RequireAnyClientCert is intentional,
		// not a weakening: the full chain walk (signature, expiry, basic
		// constraints, key usage, trust anchor) runs in VerifyPeerCertificate
		// below via VerifyPeerCertWithHardwareModuleSAN, after the HMN OID
		// is acknowledged. See internal/tls/verify.go and tests
		// TestVerifyRejectsCertSignedByDifferentCA, TestMutualTLSHandshake.
		ClientAuth: gotls.RequireAnyClientCert,
		VerifyPeerCertificate: func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
			return VerifyPeerCertWithHardwareModuleSAN(rawCerts, caPool)
		},
		MinVersion: gotls.VersionTLS12,
		MaxVersion: gotls.VersionTLS12,
		CipherSuites: []uint16{
			gotls.TLS_ECDHE_ECDSA_WITH_AES_128_CCM_8,
			0xC02B, // TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256 (fallback)
		},
		CurvePreferences: []gotls.CurveID{gotls.CurveP256},
	}, nil
}

// SetupCCMServer configures an http.Server to work with gotls listeners.
// It uses ConnContext to inject the gotls connection state into each request's
// context, and a middleware can then populate r.TLS from it.
func SetupCCMServer(srv *http.Server) {
	srv.ConnContext = func(ctx context.Context, c net.Conn) context.Context {
		if gc, ok := c.(*gotls.Conn); ok {
			return context.WithValue(ctx, ccmStateKey{}, gc)
		}
		return ctx
	}
}

// CCMIdentityMiddleware extracts peer certificates from a gotls connection
// and populates r.TLS so standard identity middleware works.
// Must wrap handlers BEFORE the identity middleware.
func CCMIdentityMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.TLS != nil {
			// Already populated (crypto/tls path)
			next.ServeHTTP(w, r)
			return
		}

		gc, ok := r.Context().Value(ccmStateKey{}).(*gotls.Conn)
		if !ok {
			next.ServeHTTP(w, r)
			return
		}

		state := gc.ConnectionState()
		r.TLS = &tls.ConnectionState{
			Version:           state.Version,
			HandshakeComplete: state.HandshakeComplete,
			CipherSuite:       state.CipherSuite,
			ServerName:        state.ServerName,
		}

		for _, pc := range state.PeerCertificates {
			r.TLS.PeerCertificates = append(r.TLS.PeerCertificates, pc)
		}

		next.ServeHTTP(w, r)
	})
}
