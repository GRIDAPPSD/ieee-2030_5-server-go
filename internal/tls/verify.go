package tls

import (
	"crypto/x509"
	"errors"
	"fmt"

	"github.com/GRIDAPPSD/ieee-2030_5-go/internal/certs"
)

// VerifyPeerCertWithHardwareModuleSAN performs full chain verification of a
// presented peer certificate chain against the configured CA pool, tolerating
// the IEEE 2030.5 §6.11 / CSIP §6.2 critical HardwareModuleName SAN that the
// stdlib x509 parser leaves in UnhandledCriticalExtensions.
//
// Peer-neutral: usable as the VerifyPeerCertificate hook on either a TLS
// server (verifying a client cert chain) or a TLS client (verifying a server
// cert chain). The chain walk uses KeyUsages=[ExtKeyUsageClientAuth] —
// retained from the original server-side helper for behavior parity. CSIP
// server certs (internal/certs GenerateServerCert) carry both ServerAuth and
// ClientAuth ExtKeyUsage, so this constraint is satisfied for our own
// servers. Outside callers that need a ServerAuth-only profile can layer an
// additional check on top.
//
// CSIP enforcement scope (read before refactoring):
//
// This hook is acknowledge-only — it does NOT enforce CSIP / IEEE 2030.5
// cert profile requirements at the TLS handshake. In particular it does
// NOT require the leaf to carry a HardwareModuleName SAN, does NOT check
// HardwareModuleName format, does NOT require an indefinite notAfter, and
// does NOT validate the IEEE 2030.5 policy-OID set. Those are enforced at
// cert generation time in internal/certs.
//
// What this hook DOES do, in order:
//
//  1. Parse every certificate in the presented chain.
//  2. Acknowledge the SubjectAlternativeName extension iff the leaf (or
//     intermediate) SAN is a well-formed GeneralNames SEQUENCE in which
//     EVERY otherName entry is a well-formed RFC 4108 HardwareModuleName.
//     A SAN that mixes HMN with foreign otherName OIDs, malformed HMN
//     inner bytes, or any non-otherName GeneralName form is left
//     unacknowledged. Only the SAN OID is removed from
//     UnhandledCriticalExtensions; every other unhandled critical
//     extension stays so stdlib Verify still fails closed.
//  3. Call leaf.Verify(opts) with the configured Roots, Intermediates, and
//     KeyUsages=[ExtKeyUsageClientAuth]. This is the standard chain walk:
//     signature, expiry, basic constraints, key usage, and trust anchor
//     are all enforced by the stdlib.
//
// Why the acknowledge step is needed: RFC 5280 §4.2.1.6 requires the SAN
// to be marked critical when the Subject is empty (which CSIP device certs
// are), and Go's x509 parser does not understand the HardwareModuleName
// otherName form, so the parser lists the SAN OID in
// UnhandledCriticalExtensions and stdlib Verify would otherwise reject the
// chain.
//
// A well-signed cert with no SAN at all is accepted here — there is no
// unhandled critical extension to trip. Tightening the verifier to also
// require SAN presence at handshake time is a separate concern; file a
// ticket if needed.
func VerifyPeerCertWithHardwareModuleSAN(rawCerts [][]byte, roots *x509.CertPool) error {
	if len(rawCerts) == 0 {
		return errors.New("verify: no peer certificate presented")
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
		return fmt.Errorf("verify peer cert: %w", err)
	}
	return nil
}

// clearKnownCriticalSAN removes the SubjectAlternativeName OID from
// cert.UnhandledCriticalExtensions if and only if the cert's SAN is a
// well-formed GeneralNames SEQUENCE in which EVERY otherName is a
// well-formed RFC 4108 §5 HardwareModuleName. A SAN that mixes a valid
// HardwareModuleName with any other otherName (different OID, or HMN OID
// but malformed inner bytes) or with any non-otherName GeneralName form is
// left unacknowledged so x509.Verify fails closed.
//
// All other unhandled critical OIDs are also left in place. This is the
// fail-closed counterpart to ExtractHardwareModuleName — see
// certs.SANAllOtherNamesAreHardwareModuleName for the strict checker.
//
// Note: this function mutates cert.UnhandledCriticalExtensions in place.
// Callers must not share *cert across goroutines or cache it; in the current
// production path each handshake parses fresh certs from rawCerts, so the
// mutation is safe.
func clearKnownCriticalSAN(cert *x509.Certificate) {
	if len(cert.UnhandledCriticalExtensions) == 0 {
		return
	}
	allHMN, _ := certs.SANAllOtherNamesAreHardwareModuleName(cert)
	filtered := cert.UnhandledCriticalExtensions[:0]
	for _, oid := range cert.UnhandledCriticalExtensions {
		if oid.Equal(certs.OIDSubjectAltName) && allHMN {
			continue
		}
		filtered = append(filtered, oid)
	}
	cert.UnhandledCriticalExtensions = filtered
}
