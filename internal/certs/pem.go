package certs

import (
	"bytes"
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

// ParseCertificateChainPEM parses every CERTIFICATE block in pemBytes, in
// file order (#638 fix round 3 item 4): a server certificate file may hold
// the leaf followed by the intermediates that sign it, the same layout the
// TLS handshake path and the shared library's peer verifier both already
// expect (vendor/.../pkg/sep2tls/verify.go). A block of another PEM type
// (a private key sharing the file) is skipped rather than rejected.
func ParseCertificateChainPEM(pemBytes []byte) ([]*x509.Certificate, error) {
	var chain []*x509.Certificate
	rest := pemBytes
	for {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			break
		}
		if block.Type != "CERTIFICATE" {
			continue
		}
		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("parse certificate %d: %w", len(chain), err)
		}
		chain = append(chain, cert)
	}
	if len(chain) == 0 {
		return nil, fmt.Errorf("no certificate found")
	}
	return chain, nil
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

// certificateBlockTypes are the PEM block types FilterCertificatePEM treats
// as a certificate. LoadCA does not check block.Type before calling
// x509.ParseCertificate, so a CA file that loads and signs correctly may
// still carry the legacy OpenSSL label instead of the modern one; the filter
// accepts both rather than silently emptying a CA that is in active use.
var certificateBlockTypes = map[string]bool{
	"CERTIFICATE":      true,
	"X509 CERTIFICATE": true,
}

// pemBeginMarker is the line prefix pem.Decode itself anchors on (see
// pemStart in encoding/pem): a block's own encoded form starts at the last
// such marker before the matching END line, not at the start of whatever
// span pem.Decode was asked to search.
var pemBeginMarker = []byte("-----BEGIN ")

// blockSourceStart returns the offset within consumed (the bytes pem.Decode
// walked to produce one block, any leading skipped content included) where
// that block's own "-----BEGIN " line starts. It mirrors pem.Decode's "last
// BEGIN line before the END line" rule so the returned offset always lands
// on the same BEGIN pem.Decode itself matched, never on a marker belonging
// to a skipped block that precedes it.
func blockSourceStart(consumed []byte) int {
	limit := len(consumed)
	for {
		i := bytes.LastIndex(consumed[:limit], pemBeginMarker)
		if i < 0 {
			return 0
		}
		if i == 0 || consumed[i-1] == '\n' {
			return i
		}
		limit = i
	}
}

// FilterCertificatePEM returns the concatenated certificate blocks found in
// pemBytes (see certificateBlockTypes), dropping every other PEM block (most
// often a private key) and any non-PEM content, whether it precedes the
// first block, sits between two blocks, or trails the last one. Each
// block's own source bytes are copied rather than re-encoded, so a file
// holding only certificate blocks comes back unchanged byte for byte,
// whatever its line wrapping.
func FilterCertificatePEM(pemBytes []byte) []byte {
	var out []byte
	rest := pemBytes
	for {
		start := rest
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			break
		}
		if certificateBlockTypes[block.Type] {
			consumed := start[:len(start)-len(rest)]
			out = append(out, consumed[blockSourceStart(consumed):]...)
		}
	}
	return out
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
