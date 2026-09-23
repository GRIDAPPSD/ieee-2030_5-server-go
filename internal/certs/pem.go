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
// cert is nil only when the certificate itself could not be read or
// parsed; keyErr is nil exactly when key is non-nil and usable. A key that
// parses but does not match the certificate's public key is returned the
// same as an absent key (#638 fix round 1, MEDIUM 2): LoadCA's plain
// "read both files" contract would otherwise pair a certificate rotated
// under SEP2_SERVING_CA or SEP2_DEVICE_CA with the old shared key, load
// "clean", and fail only at the first mint with an opaque x509 error.
func LoadCAPair(certFile, keyFile string) (cert *x509.Certificate, certPEM []byte, key *ecdsa.PrivateKey, keyErr error) {
	certPEMBytes, err := os.ReadFile(certFile)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("read CA cert: %w", err)
	}
	c, err := ParseCertificatePEM(certPEMBytes)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("parse CA cert: %w", err)
	}

	keyPEMBytes, err := os.ReadFile(keyFile)
	if err != nil {
		return c, certPEMBytes, nil, fmt.Errorf("read CA key: %w", err)
	}
	k, err := ParseKeyPEM(keyPEMBytes)
	if err != nil {
		return c, certPEMBytes, nil, fmt.Errorf("parse CA key: %w", err)
	}
	certPub, ok := c.PublicKey.(*ecdsa.PublicKey)
	if !ok {
		return c, certPEMBytes, nil, fmt.Errorf("CA certificate public key is not ECDSA")
	}
	if !certPub.Equal(&k.PublicKey) {
		return c, certPEMBytes, nil, fmt.Errorf("CA certificate and key are not a matched pair")
	}
	return c, certPEMBytes, k, nil
}
