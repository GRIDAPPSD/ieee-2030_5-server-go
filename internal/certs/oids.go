package certs

import (
	"crypto/x509"
	"encoding/asn1"
	"fmt"
	"strconv"
	"strings"
)

// IEEE 2030.5 OID arc: 1.3.6.1.4.1.40732
// Per spec section 6.11.7.1
var (
	OIDIeee20305 = asn1.ObjectIdentifier{1, 3, 6, 1, 4, 1, 40732}

	// DeviceType OIDs (spec section 6.11.7.2)
	OIDDeviceType        = asn1.ObjectIdentifier{1, 3, 6, 1, 4, 1, 40732, 1}
	OIDDeviceTypeGeneric = asn1.ObjectIdentifier{1, 3, 6, 1, 4, 1, 40732, 1, 1}
	OIDDeviceTypeMobile  = asn1.ObjectIdentifier{1, 3, 6, 1, 4, 1, 40732, 1, 2}
	OIDDeviceTypePostMfg = asn1.ObjectIdentifier{1, 3, 6, 1, 4, 1, 40732, 1, 3}

	// Policy OIDs (spec section 6.11.6)
	OIDPolicy            = asn1.ObjectIdentifier{1, 3, 6, 1, 4, 1, 40732, 2}
	OIDPolicyDevAuthTest = asn1.ObjectIdentifier{1, 3, 6, 1, 4, 1, 40732, 2, 1}
	OIDPolicySelfSigned  = asn1.ObjectIdentifier{1, 3, 6, 1, 4, 1, 40732, 2, 2}
	OIDPolicyServiceProv = asn1.ObjectIdentifier{1, 3, 6, 1, 4, 1, 40732, 2, 3}
	OIDPolicyBulkCert    = asn1.ObjectIdentifier{1, 3, 6, 1, 4, 1, 40732, 2, 4}
	OIDPolicyAdmin       = asn1.ObjectIdentifier{1, 3, 6, 1, 4, 1, 40732, 2, 5}

	// HardwareModuleName OID from RFC 4108
	OIDHardwareModuleName = asn1.ObjectIdentifier{1, 3, 6, 1, 5, 5, 7, 8, 4}

	// Certificate Policies extension OID
	OIDCertificatePolicies = asn1.ObjectIdentifier{2, 5, 29, 32}

	// Subject Alternative Name extension OID
	OIDSubjectAltName = asn1.ObjectIdentifier{2, 5, 29, 17}
)

// DeviceType represents the IEEE 2030.5 device type for certificate generation.
type DeviceType int

const (
	DeviceTypeGeneric DeviceType = 1
	DeviceTypeMobile  DeviceType = 2
	DeviceTypePostMfg DeviceType = 3
)

// ParseOID parses a dot-separated OID string (e.g.
// "1.3.6.1.4.1.40732.99") into an asn1.ObjectIdentifier and validates the
// basic ASN.1 / X.660 well-formedness constraints:
//
//   - at least two arcs (X.660 §3.5.1)
//   - arc[0] ∈ {0, 1, 2}
//   - if arc[0] < 2, then arc[1] ∈ [0, 39]
//
// Without these checks, syntactically numeric but semantically invalid OIDs
// would surface much later as ASN.1 marshaling failures during certificate
// generation — i.e. as a 500 from the admin API instead of a clean 400
// validation error.
func ParseOID(s string) (asn1.ObjectIdentifier, error) {
	if s == "" {
		return nil, fmt.Errorf("empty OID")
	}
	parts := strings.Split(s, ".")
	oid := make(asn1.ObjectIdentifier, 0, len(parts))
	for _, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil {
			return nil, fmt.Errorf("invalid OID arc %q in %q", p, s)
		}
		if n < 0 {
			return nil, fmt.Errorf("negative OID arc %d in %q", n, s)
		}
		oid = append(oid, n)
	}

	// X.660 well-formedness.
	if len(oid) < 2 {
		return nil, fmt.Errorf("invalid ASN.1 OID %q: must have at least two arcs", s)
	}
	if oid[0] > 2 {
		return nil, fmt.Errorf("invalid ASN.1 OID %q: first arc must be 0, 1, or 2 (got %d)", s, oid[0])
	}
	if oid[0] < 2 && oid[1] > 39 {
		return nil, fmt.Errorf("invalid ASN.1 OID %q: when first arc is %d, second arc must be in 0..39 (got %d)", s, oid[0], oid[1])
	}

	return oid, nil
}

// HardwareModuleName carries the parsed contents of an RFC 4108 §5
// HardwareModuleName otherName entry, as embedded in a SubjectAlternativeName
// extension on an IEEE 2030.5 / CSIP device certificate.
type HardwareModuleName struct {
	HWType      asn1.ObjectIdentifier // manufacturer hwType OID (e.g. PEN-rooted)
	HWSerialNum []byte                // hwSerialNum, raw OCTET STRING bytes
}

// ExtractHardwareModuleName walks a parsed certificate's SubjectAlternativeName
// extension looking for an otherName with TypeID == OIDHardwareModuleName
// (RFC 4108 §5). It returns the parsed HardwareModuleName and ok=true on the
// first match.
//
// The function is intentionally conservative: any ASN.1 parse failure or a
// malformed inner SEQUENCE returns ok=false. This is the canonical extractor;
// callers that need a presence-only check should rely on the boolean ok
// without inspecting the returned struct.
func ExtractHardwareModuleName(cert *x509.Certificate) (HardwareModuleName, bool) {
	for _, ext := range cert.Extensions {
		if !ext.Id.Equal(OIDSubjectAltName) {
			continue
		}

		var seq asn1.RawValue
		if _, err := asn1.Unmarshal(ext.Value, &seq); err != nil {
			return HardwareModuleName{}, false
		}
		rest := seq.Bytes
		for len(rest) > 0 {
			var gn asn1.RawValue
			var err error
			rest, err = asn1.Unmarshal(rest, &gn)
			if err != nil {
				return HardwareModuleName{}, false
			}
			if gn.Class != asn1.ClassContextSpecific || gn.Tag != 0 {
				continue
			}
			var on struct {
				TypeID asn1.ObjectIdentifier
				Value  asn1.RawValue
			}
			if _, err := asn1.UnmarshalWithParams(gn.FullBytes, &on, "tag:0"); err != nil {
				return HardwareModuleName{}, false
			}
			if !on.TypeID.Equal(OIDHardwareModuleName) {
				continue
			}
			var hmn struct {
				HWType      asn1.ObjectIdentifier
				HWSerialNum asn1.RawValue
			}
			if _, err := asn1.Unmarshal(on.Value.Bytes, &hmn); err != nil {
				return HardwareModuleName{}, false
			}
			if len(hmn.HWType) == 0 {
				return HardwareModuleName{}, false
			}
			if hmn.HWSerialNum.Tag != asn1.TagOctetString || hmn.HWSerialNum.Class != asn1.ClassUniversal {
				return HardwareModuleName{}, false
			}
			return HardwareModuleName{
				HWType:      hmn.HWType,
				HWSerialNum: hmn.HWSerialNum.Bytes,
			}, true
		}
	}
	return HardwareModuleName{}, false
}

// SANAllOtherNamesAreHardwareModuleName reports whether the cert's
// SubjectAlternativeName extension is a well-formed GeneralNames SEQUENCE
// AND every otherName entry inside is a well-formed RFC 4108 §5
// HardwareModuleName (TypeID == OIDHardwareModuleName, inner value parses as
// the HMN SEQUENCE).
//
// Returns:
//   - (true, true)  — SAN present, well-formed, every otherName is HMN.
//   - (false, true) — SAN present but malformed, OR contains an otherName
//     that is NOT HMN, OR has zero otherName entries. The caller MUST treat
//     the SAN as unacknowledged.
//   - (false, false) — no SAN extension on the cert at all.
//
// Non-otherName GeneralName forms (dNSName, iPAddress, etc.) cause this to
// return false: a CSIP device cert SAN is otherName-only, and the verifier
// path that consults this helper is acknowledge-only for the HMN case. Any
// other SAN content remains unhandled and must trip x509.Verify.
//
// This is the strict counterpart to ExtractHardwareModuleName, which returns
// the first HMN match and is the right tool for callers that just want to
// read the HMN payload off a known-good cert.
func SANAllOtherNamesAreHardwareModuleName(cert *x509.Certificate) (allHMN bool, sanPresent bool) {
	for _, ext := range cert.Extensions {
		if !ext.Id.Equal(OIDSubjectAltName) {
			continue
		}
		// SAN extension found — every return below is (_, true).

		var seq asn1.RawValue
		if _, err := asn1.Unmarshal(ext.Value, &seq); err != nil {
			return false, true
		}
		if seq.Class != asn1.ClassUniversal || seq.Tag != asn1.TagSequence || !seq.IsCompound {
			return false, true
		}

		rest := seq.Bytes
		entries := 0
		for len(rest) > 0 {
			var gn asn1.RawValue
			var err error
			rest, err = asn1.Unmarshal(rest, &gn)
			if err != nil {
				return false, true
			}
			entries++
			// Every GeneralName must be an otherName ([0] IMPLICIT) AND
			// every otherName must be a well-formed HMN. Any non-otherName
			// GeneralName (dNSName, iPAddress, etc.) means the SAN as a
			// whole still carries content the verifier should not silently
			// acknowledge.
			if gn.Class != asn1.ClassContextSpecific || gn.Tag != 0 {
				return false, true
			}
			var on struct {
				TypeID asn1.ObjectIdentifier
				Value  asn1.RawValue
			}
			if _, err := asn1.UnmarshalWithParams(gn.FullBytes, &on, "tag:0"); err != nil {
				return false, true
			}
			if !on.TypeID.Equal(OIDHardwareModuleName) {
				return false, true
			}
			var hmn struct {
				HWType      asn1.ObjectIdentifier
				HWSerialNum asn1.RawValue
			}
			if _, err := asn1.Unmarshal(on.Value.Bytes, &hmn); err != nil {
				return false, true
			}
			if len(hmn.HWType) == 0 {
				return false, true
			}
			if hmn.HWSerialNum.Tag != asn1.TagOctetString || hmn.HWSerialNum.Class != asn1.ClassUniversal {
				return false, true
			}
		}
		if entries == 0 {
			return false, true
		}
		return true, true
	}
	return false, false
}

// HasPolicyOID checks whether a certificate's CertificatePolicies
// extension contains the given policy OID.
func HasPolicyOID(cert *x509.Certificate, target asn1.ObjectIdentifier) bool {
	for _, ext := range cert.Extensions {
		if !ext.Id.Equal(OIDCertificatePolicies) {
			continue
		}

		type policyInformation struct {
			PolicyIdentifier asn1.ObjectIdentifier
		}

		var policies []policyInformation
		if _, err := asn1.Unmarshal(ext.Value, &policies); err != nil {
			return false
		}

		for _, p := range policies {
			if p.PolicyIdentifier.Equal(target) {
				return true
			}
		}
	}
	return false
}

// OID returns the certificate policy OID for this device type.
func (dt DeviceType) OID() asn1.ObjectIdentifier {
	switch dt {
	case DeviceTypeGeneric:
		return OIDDeviceTypeGeneric
	case DeviceTypeMobile:
		return OIDDeviceTypeMobile
	case DeviceTypePostMfg:
		return OIDDeviceTypePostMfg
	default:
		return OIDDeviceTypeGeneric
	}
}
