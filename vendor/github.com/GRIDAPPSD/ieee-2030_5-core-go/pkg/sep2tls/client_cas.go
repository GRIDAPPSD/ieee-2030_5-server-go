package sep2tls

import (
	"crypto/x509"
	"errors"
	"fmt"
	"os"
)

// LoadClientCAs builds an *x509.CertPool seeded with the primary CA PEM
// file and any number of additional PEM files appended in order. Every
// listed file must be readable and parseable; the function returns a
// wrapped error citing the offending path on the first failure.
//
// Empty extraPaths is the no-op case: the returned pool contains only the
// primary CA. A nil or zero-length extraPaths slice does NOT error.
//
// Multi-root semantics: every CA in the resulting pool is a trust anchor
// for incoming client certs at handshake. Use this to let the server
// authenticate device certs issued under multiple, independent CSIP test
// roots (e.g. SunSpec PKI plus Enphase test PKI) without choosing between
// them at boot time. The pool is additive — no per-CA scoping.
//
// Caller owns the returned pool. The function does not retain references
// to its inputs.
func LoadClientCAs(primaryCAFile string, extraPaths []string) (*x509.CertPool, error) {
	if primaryCAFile == "" {
		return nil, errors.New("LoadClientCAs: primary CA file is required")
	}

	pool := x509.NewCertPool()
	if err := appendPEMFile(pool, primaryCAFile); err != nil {
		return nil, fmt.Errorf("primary CA: %w", err)
	}

	for _, p := range extraPaths {
		if p == "" {
			// Tolerate empty strings (e.g. trailing comma in env). The
			// env-parsing layer already trims, but defending here keeps
			// the contract crisp.
			continue
		}
		if err := appendPEMFile(pool, p); err != nil {
			return nil, fmt.Errorf("extra client CA %q: %w", p, err)
		}
	}

	return pool, nil
}

// appendPEMFile reads a PEM file from disk and appends every CERTIFICATE
// block it contains to the pool. Returns a wrapped error if the file
// cannot be read or contains no parseable certificates.
func appendPEMFile(pool *x509.CertPool, path string) error {
	pem, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read: %w", err)
	}
	if !pool.AppendCertsFromPEM(pem) {
		return fmt.Errorf("parse: no certificates found in %s", path)
	}
	return nil
}
