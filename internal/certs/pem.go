package certs

import (
	"crypto/ecdsa"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"os"
)

// ParseCertificatePEM parses a PEM-encoded certificate.
func ParseCertificatePEM(pemBytes []byte) (*x509.Certificate, error) {
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		return nil, fmt.Errorf("no PEM data found")
	}
	return x509.ParseCertificate(block.Bytes)
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

// LoadCA loads a CA certificate and private key from PEM files.
func LoadCA(certFile, keyFile string) (*x509.Certificate, *ecdsa.PrivateKey, error) {
	certPEM, err := os.ReadFile(certFile)
	if err != nil {
		return nil, nil, fmt.Errorf("read CA cert: %w", err)
	}
	keyPEM, err := os.ReadFile(keyFile)
	if err != nil {
		return nil, nil, fmt.Errorf("read CA key: %w", err)
	}

	cert, err := ParseCertificatePEM(certPEM)
	if err != nil {
		return nil, nil, fmt.Errorf("parse CA cert: %w", err)
	}
	key, err := ParseKeyPEM(keyPEM)
	if err != nil {
		return nil, nil, fmt.Errorf("parse CA key: %w", err)
	}

	return cert, key, nil
}

// CAInfo holds a loaded CA certificate and key pair.
type CAInfo struct {
	Cert *x509.Certificate
	Key  *ecdsa.PrivateKey
}

// LoadCAPair loads a CA certificate independently of its private key (#638
// fix round 1, MEDIUM 1/3): the protocol listener's trust pool and the
// admin GET /api/certs/ca route both need only the certificate, so a
// deployment that keeps a CA's certificate and removes its key - the safer
// posture for a role that should never mint - still gets that certificate
// back rather than being treated as though the CA were missing entirely.
//
// cert is nil exactly when certErr is non-nil: the certificate itself could
// not be read or parsed, and keyErr stays nil since the key was never
// reached. keyErr is nil exactly when key is non-nil and usable; it is set,
// with cert still non-nil, when the certificate loaded but its key did
// not - absent, unparseable, or not the certificate's own key (#638 fix
// round 1, MEDIUM 2). The two errors never both hold, so a caller that
// branches on keyErr alone never reports a missing certificate as a key
// problem (#638 fix round 3 item 6).
func LoadCAPair(certFile, keyFile string) (cert *x509.Certificate, certPEM []byte, key *ecdsa.PrivateKey, certErr, keyErr error) {
	certPEMBytes, err := os.ReadFile(certFile)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("read CA cert: %w", err), nil
	}
	c, err := ParseCertificatePEM(certPEMBytes)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("parse CA cert: %w", err), nil
	}

	keyPEMBytes, err := os.ReadFile(keyFile)
	if err != nil {
		return c, certPEMBytes, nil, nil, fmt.Errorf("read CA key: %w", err)
	}
	k, err := ParseKeyPEM(keyPEMBytes)
	if err != nil {
		return c, certPEMBytes, nil, nil, fmt.Errorf("parse CA key: %w", err)
	}
	certPub, ok := c.PublicKey.(*ecdsa.PublicKey)
	if !ok {
		return c, certPEMBytes, nil, nil, fmt.Errorf("CA certificate public key is not ECDSA")
	}
	if !certPub.Equal(&k.PublicKey) {
		return c, certPEMBytes, nil, nil, fmt.Errorf("CA certificate and key are not a matched pair")
	}
	return c, certPEMBytes, k, nil, nil
}
