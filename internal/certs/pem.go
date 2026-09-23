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
