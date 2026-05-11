package tls

import (
	"crypto/x509"
	"errors"
	"fmt"

	"github.com/GRIDAPPSD/ieee-2030_5-go/internal/certs"
)

// verifyClientCertWithHardwareModuleSAN performs full chain verification of
// a presented client certificate chain against the configured CA pool.
//
// Before calling x509.Verify it acknowledges the SubjectAlternativeName
// extension when that SAN carries an RFC 4108 HardwareModuleName otherName
// (IEEE 2030.5 §6.11 / CSIP §6.2). RFC 5280 §4.2.1.6 requires the SAN to be
// marked critical when the Subject is empty, and Go's x509 parser does not
// understand the HardwareModuleName otherName form, so the parser leaves the
// SAN OID under UnhandledCriticalExtensions and stdlib Verify would reject
// the chain. This hook removes only that single OID — and only when the SAN
// actually parses as a HardwareModuleName — leaving every other unhandled
// critical extension intact so Verify still fails closed.
//
// Contract: this hook is acknowledge-only. It does NOT enforce CSIP cert
// profile requirements (HardwareModuleName presence, indefinite notAfter,
// specific keyUsage flag combinations, etc.). Those are enforced at cert
// generation time in internal/certs, not at handshake time. A well-signed
// cert with no SAN at all is accepted here. If stricter CSIP enforcement at
// the handshake is needed later, file a separate ticket.
func verifyClientCertWithHardwareModuleSAN(rawCerts [][]byte, roots *x509.CertPool) error {
	if len(rawCerts) == 0 {
		return errors.New("verify: no client certificate presented")
	}

	chain := make([]*x509.Certificate, 0, len(rawCerts))
	for i, raw := range rawCerts {
		cert, err := x509.ParseCertificate(raw)
		if err != nil {
			return fmt.Errorf("verify: parse cert %d: %w", i, err)
		}
		chain = append(chain, cert)
	}

	leaf := chain[0]
	clearKnownCriticalSAN(leaf)

	intermediates := x509.NewCertPool()
	for _, c := range chain[1:] {
		clearKnownCriticalSAN(c)
		intermediates.AddCert(c)
	}

	opts := x509.VerifyOptions{
		Roots:         roots,
		Intermediates: intermediates,
		KeyUsages:     []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	if _, err := leaf.Verify(opts); err != nil {
		return fmt.Errorf("verify client cert: %w", err)
	}
	return nil
}

// clearKnownCriticalSAN removes the SubjectAlternativeName OID from
// cert.UnhandledCriticalExtensions if and only if the cert's SAN carries a
// well-formed RFC 4108 HardwareModuleName otherName. All other unhandled
// critical OIDs — including a SAN whose otherName uses a different OID or
// whose inner bytes don't parse — are left in place so x509.Verify fails
// closed.
//
// Note: this function mutates cert.UnhandledCriticalExtensions in place.
// Callers must not share *cert across goroutines or cache it; in the current
// production path each handshake parses fresh certs from rawCerts, so the
// mutation is safe.
func clearKnownCriticalSAN(cert *x509.Certificate) {
	if len(cert.UnhandledCriticalExtensions) == 0 {
		return
	}
	_, hasHMN := certs.ExtractHardwareModuleName(cert)
	filtered := cert.UnhandledCriticalExtensions[:0]
	for _, oid := range cert.UnhandledCriticalExtensions {
		if oid.Equal(certs.OIDSubjectAltName) && hasHMN {
			continue
		}
		filtered = append(filtered, oid)
	}
	cert.UnhandledCriticalExtensions = filtered
}
