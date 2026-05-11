package tls

import (
	"crypto/x509"
	"encoding/asn1"
	"errors"
	"fmt"
)

// oidSubjectAltName is the SAN extension OID (2.5.29.17). Duplicated here to
// avoid an import cycle with internal/certs. RFC 5280 §4.2.1.6.
var oidSubjectAltName = asn1.ObjectIdentifier{2, 5, 29, 17}

// verifyClientCertWithHardwareModuleSAN performs full chain verification
// against the provided CA pool, then acknowledges that the SAN extension
// is critical (as required for IEEE 2030.5 / CSIP device certs with an
// empty Subject) by removing it from UnhandledCriticalExtensions before
// the stdlib's own Verify call would have rejected the chain.
//
// IEEE 2030.5 §6.11 and CSIP §6.2 require the HardwareModuleName SAN.
// RFC 5280 §4.2.1.6 requires the SAN to be marked critical when the
// Subject is empty. Go's x509 parser does not understand the
// otherName HardwareModuleName form, so it lists the SAN OID under
// UnhandledCriticalExtensions; this hook tells x509.Verify it is
// expected.
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

// clearKnownCriticalSAN removes the SAN OID from UnhandledCriticalExtensions
// if the cert's SAN is the IEEE 2030.5 HardwareModuleName otherName form.
// It does not touch other unhandled critical extensions — those still cause
// Verify to fail, which is the safe default.
func clearKnownCriticalSAN(cert *x509.Certificate) {
	if len(cert.UnhandledCriticalExtensions) == 0 {
		return
	}
	filtered := cert.UnhandledCriticalExtensions[:0]
	for _, oid := range cert.UnhandledCriticalExtensions {
		if oid.Equal(oidSubjectAltName) && certHasHardwareModuleSAN(cert) {
			continue
		}
		filtered = append(filtered, oid)
	}
	cert.UnhandledCriticalExtensions = filtered
}

// certHasHardwareModuleSAN reports whether the cert's SAN extension carries
// a HardwareModuleName otherName entry (RFC 4108 OID 1.3.6.1.5.5.7.8.4).
// It is intentionally conservative: any parse failure or mismatch means
// the SAN is NOT acknowledged and the cert will be rejected.
func certHasHardwareModuleSAN(cert *x509.Certificate) bool {
	var oidHardwareModuleName = asn1.ObjectIdentifier{1, 3, 6, 1, 5, 5, 7, 8, 4}

	for _, ext := range cert.Extensions {
		if !ext.Id.Equal(oidSubjectAltName) {
			continue
		}

		var seq asn1.RawValue
		if _, err := asn1.Unmarshal(ext.Value, &seq); err != nil {
			return false
		}
		rest := seq.Bytes
		for len(rest) > 0 {
			var gn asn1.RawValue
			var err error
			rest, err = asn1.Unmarshal(rest, &gn)
			if err != nil {
				return false
			}
			if gn.Class != asn1.ClassContextSpecific || gn.Tag != 0 {
				continue
			}
			var on struct {
				TypeID asn1.ObjectIdentifier
				Value  asn1.RawValue
			}
			if _, err := asn1.UnmarshalWithParams(gn.FullBytes, &on, "tag:0"); err != nil {
				return false
			}
			if on.TypeID.Equal(oidHardwareModuleName) {
				return true
			}
		}
	}
	return false
}
