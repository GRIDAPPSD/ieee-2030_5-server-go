package sep2tls

import (
	"crypto/x509"
	"fmt"
	"os"

	gotls "github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2tls/gotls"
)

// NewCCMClientConfig builds a *gotls.Config for an outbound IEEE 2030.5
// client, offering CCM-8 only (IEEE 2030.5-2018 clause 6.7), the same
// suite NewCCMServerConfig requires of an inbound peer. Core does not
// offer GCM anywhere: a server that cannot speak CCM-8 is refused, not
// downgraded to it.
//
// The returned config is a *gotls.Config, not a *tls.Config, and it MUST be
// dialed with the fork: gotls.Dial, gotls.DialWithDialer, or
// (&gotls.Dialer{Config: cfg}).DialContext wired as an
// http.Transport.DialTLSContext hook. Stdlib crypto/tls.Dial and
// http.Transport.TLSClientConfig cannot negotiate CCM-8 at all
// (golang/go#27484), so this config is silently useless to either.
//
// When wired as DialTLSContext, net/http never populates resp.TLS for the
// connection it returns: net/http's transport only extracts connection
// state from a *crypto/tls.Conn (see net/http's getConn), and a
// *gotls.Conn does not satisfy that check. A caller that needs the
// negotiated version, cipher suite, or the server's peer certificates must
// keep its own reference to the *gotls.Conn (gotls.Dialer.DialContext
// already returns one) and read ConnectionState() from it directly; resp.TLS
// will be nil.
func NewCCMClientConfig(certFile, keyFile, caFile string) (*gotls.Config, error) {
	certPEM, err := os.ReadFile(certFile)
	if err != nil {
		return nil, fmt.Errorf("read cert: %w", err)
	}
	keyPEM, err := os.ReadFile(keyFile)
	if err != nil {
		return nil, fmt.Errorf("read key: %w", err)
	}
	caCertPEM, err := os.ReadFile(caFile)
	if err != nil {
		return nil, fmt.Errorf("read CA cert: %w", err)
	}

	cert, err := gotls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return nil, fmt.Errorf("parse cert: %w", err)
	}

	caPool := x509.NewCertPool()
	if !caPool.AppendCertsFromPEM(caCertPEM) {
		return nil, fmt.Errorf("failed to parse CA certificate")
	}

	return newCCMClientConfigFromMaterial(cert, caPool), nil
}

// NewCCMClientConfigFromPEM is NewCCMClientConfig from PEM byte slices
// instead of file paths. Useful for testing.
//
// Same gotls.Dial / DialTLSContext / resp.TLS obligation as
// NewCCMClientConfig: see its doc comment.
func NewCCMClientConfigFromPEM(certPEM, keyPEM, caPEM []byte) (*gotls.Config, error) {
	cert, err := gotls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return nil, fmt.Errorf("parse client cert: %w", err)
	}

	caPool := x509.NewCertPool()
	if !caPool.AppendCertsFromPEM(caPEM) {
		return nil, fmt.Errorf("failed to parse CA certificate")
	}

	return newCCMClientConfigFromMaterial(cert, caPool), nil
}

// newCCMClientConfigFromMaterial builds the shared *gotls.Config body for
// NewCCMClientConfig and NewCCMClientConfigFromPEM. Unlike the server side,
// a client config sets no ClientAuth or VerifyPeerCertificate: the peer
// being verified here is the IEEE 2030.5 server, which CSIP does not
// require to carry the HardwareModuleName SAN that device certs do, so
// gotls's normal chain-and-hostname verification against RootCAs applies
// unmodified.
func newCCMClientConfigFromMaterial(cert gotls.Certificate, caPool *x509.CertPool) *gotls.Config {
	return &gotls.Config{
		Certificates: []gotls.Certificate{cert},
		RootCAs:      caPool,
		// Same cap and reasoning as the server side (ccmserver.go): IEEE
		// 2030.5-2018 clauses 6.1 and 6.4 specify TLS 1.2, and IEEE
		// 2030.5-2023 leaves 1.3 optional rather than mandatory. Capping
		// the client the same way the server is capped keeps the two
		// configs' negotiated ceiling uniform and means a compliant
		// mandatory-profile server is never offered a version it must
		// refuse.
		MinVersion: gotls.VersionTLS12,
		MaxVersion: gotls.VersionTLS12,
		CipherSuites: []uint16{
			gotls.TLS_ECDHE_ECDSA_WITH_AES_128_CCM_8,
		},
		CurvePreferences: []gotls.CurveID{gotls.CurveP256},
	}
}
