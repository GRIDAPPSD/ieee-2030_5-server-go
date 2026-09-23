package sep2cert

import (
	"crypto/ecdsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
)

// ErrLoadCATooManyOptions is returned when a caller passes more than one
// ValidateCAOptions value to LoadCA. The variadic form exists so a caller
// with no opinion on the clock can omit the argument; it never silently
// drops a second value the caller may have intended to apply instead of
// the first.
var ErrLoadCATooManyOptions = errors.New("LoadCA accepts at most one ValidateCAOptions value")

// ParseCertificatePEM parses a PEM-encoded certificate.
func ParseCertificatePEM(pemBytes []byte) (*x509.Certificate, error) {
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		return nil, fmt.Errorf("no PEM data found")
	}
	return x509.ParseCertificate(block.Bytes)
}

// CertificateDER extracts the raw DER bytes from a PEM-encoded certificate.
// It is pure and IO-free: it touches no filesystem and no network, only
// encoding/pem. The block must decode and must be of type "CERTIFICATE";
// any other outcome is an error, never a partial or best-guess result
// (fail closed).
//
// Invariant: for a certificate cert parsed from certPEM, the DER bytes
// returned here are the exact preimage of that certificate's identity.
// Concretely: sha256(CertificateDER(certPEM)) equals the certificate
// fingerprint computed over cert.Raw (see sep2tls.Fingerprint). The first
// 20 bytes of that hash, hex-encoded uppercase, equal the certificate's
// LFDI per spec section 6.3.4 (see sep2tls.LFDI), and the fingerprint's
// 36-bit left truncation is the preimage of the certificate's SFDI per
// spec section 6.3.3 (see sep2tls.SFDI).
//
// Consequence: a file containing exactly these DER bytes (a ".x509" file,
// with no PEM headers or private key material) is the form a
// file-hashing IEEE 2030.5 client hashes to compute its own device
// identity. Provisioning a client with DER, not PEM, makes the
// client-computed LFDI and SFDI equal the identity the server serves for
// that same certificate, by construction, with the private key excluded
// from the hash.
//
// Like pem.Decode and the sibling ParseCertificatePEM, this function uses
// only the first PEM block in certPEM and silently ignores any trailing
// blocks or data after it, so a cert-plus-key concatenation returns the
// cert block.
func CertificateDER(certPEM []byte) ([]byte, error) {
	block, _ := pem.Decode(certPEM)
	if block == nil {
		return nil, fmt.Errorf("no PEM data found")
	}
	if block.Type != "CERTIFICATE" {
		return nil, fmt.Errorf("PEM block type is %q, want %q", block.Type, "CERTIFICATE")
	}
	return block.Bytes, nil
}

// ParseKeyPEM parses a PEM-encoded PKCS8 private key.
func ParseKeyPEM(pemBytes []byte) (*ecdsa.PrivateKey, error) {
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		return nil, fmt.Errorf("no PEM data found")
	}
	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse PKCS8 key: %w", err)
	}
	ecKey, ok := key.(*ecdsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("key is not ECDSA")
	}
	return ecKey, nil
}

// LoadCA loads a CA certificate and private key from PEM files and
// validates that the pair can serve as an issuing CA. With no opts, it
// applies ValidateCA's strict default options (time.Now(), no
// self-signed requirement, no minimum-remaining margin). Passing exactly
// one opts value overrides them; the variadic form keeps a caller with no
// opinion on the clock, most commonly a test, source-compatible. Passing
// more than one is refused with ErrLoadCATooManyOptions rather than
// silently applying the first and dropping the rest. A refusal names both
// file paths alongside the validation failure.
func LoadCA(certFile, keyFile string, opts ...ValidateCAOptions) (*x509.Certificate, *ecdsa.PrivateKey, error) {
	if len(opts) > 1 {
		return nil, nil, ErrLoadCATooManyOptions
	}

	certPEM, err := os.ReadFile(certFile)
	if err != nil {
		return nil, nil, fmt.Errorf("read CA cert: %w", err)
	}
	keyPEM, err := os.ReadFile(keyFile)
	if err != nil {
		return nil, nil, fmt.Errorf("read CA key: %w", err)
	}

	var opt ValidateCAOptions
	if len(opts) == 1 {
		opt = opts[0]
	}
	cert, key, err := ParseCAPair(certPEM, keyPEM, opt)
	if err != nil {
		return nil, nil, fmt.Errorf("%s / %s: %w", certFile, keyFile, err)
	}

	return cert, key, nil
}

// CAInfo holds a loaded CA certificate and key pair.
type CAInfo struct {
	Cert *x509.Certificate
	Key  *ecdsa.PrivateKey
}
