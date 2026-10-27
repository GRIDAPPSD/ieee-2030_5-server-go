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
//   - at least two arcs (X.660 section 3.5.1)
//   - arc[0] in {0, 1, 2}
//   - if arc[0] < 2, then arc[1] in [0, 39]
//
// Without these checks, syntactically numeric but semantically invalid OIDs
// would surface much later as ASN.1 marshaling failures during certificate
// generation - i.e. as a 500 from the admin API instead of a clean 400
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

// HardwareModuleName carries the parsed contents of an RFC 4108 section 5
// HardwareModuleName otherName entry, as embedded in a SubjectAlternativeName
// extension on an IEEE 2030.5 / CSIP device certificate.
type HardwareModuleName struct {
	HWType      asn1.ObjectIdentifier // manufacturer hwType OID (e.g. PEN-rooted)
	HWSerialNum []byte                // hwSerialNum, raw OCTET STRING bytes
}

// ExtractHardwareModuleName walks a parsed certificate's SubjectAlternativeName
// extension looking for an otherName with TypeID == OIDHardwareModuleName
// (RFC 4108 section 5). It returns the parsed HardwareModuleName and ok=true on the
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

// DeviceTypeInfo describes one certificate device type for API responses:
// the numeric value a mint request's deviceType field expects, a stable
// machine-readable name, and a human-readable label.
type DeviceTypeInfo struct {
	Value int    `json:"value"`
	Name  string `json:"name"`
	Label string `json:"label"`
}

// deviceTypeCatalog lists every DeviceType this server mints, referencing the
// constants above so a value can never diverge from its definition.
// TestDeviceTypeCatalogComplete (oids_internal_test.go) parses this file's
// const block and fails if a constant is declared here without a matching
// entry in this list.
var deviceTypeCatalog = []DeviceType{
	DeviceTypeGeneric,
	DeviceTypeMobile,
	DeviceTypePostMfg,
}

// Name returns the stable, machine-readable name for a device type.
func (dt DeviceType) Name() string {
	switch dt {
	case DeviceTypeGeneric:
		return "generic"
	case DeviceTypeMobile:
		return "mobile"
	case DeviceTypePostMfg:
		return "post_manufacture"
	default:
		return ""
	}
}

// Label returns the human-readable label for a device type.
func (dt DeviceType) Label() string {
	switch dt {
	case DeviceTypeGeneric:
		return "Generic"
	case DeviceTypeMobile:
		return "Mobile"
	case DeviceTypePostMfg:
		return "Post-Manufacture"
	default:
		return ""
	}
}

// AllDeviceTypes returns every certificate device type this server mints, in
// ascending value order, for API responses such as GET /api/certs/device-types.
func AllDeviceTypes() []DeviceTypeInfo {
	infos := make([]DeviceTypeInfo, 0, len(deviceTypeCatalog))
	for _, dt := range deviceTypeCatalog {
		infos = append(infos, DeviceTypeInfo{
			Value: int(dt),
			Name:  dt.Name(),
			Label: dt.Label(),
		})
	}
	return infos
}
