// Package certs generates and loads IEEE 2030.5 X.509 certificates.
//
// It covers the four cert flavors used by an IEEE 2030.5 deployment:
// the ECDSA P-256 root CA, the server cert (mTLS server identity),
// the device cert (client identity, whose SFDI/LFDI are computed by
// pkg/identity), and the admin cert (out-of-band administrative
// access). Per IEEE 2030.5-2018 §6.11.7, device certs default to
// "indefinite" validity (NotAfter = 9999-12-31); DeviceCertOptions
// ValidYears overrides that for testing or short-lived scenarios.
package certs

import "encoding/asn1"

// CAOptions configures root CA certificate generation.
type CAOptions struct {
	// Organization populates the Subject's Organization attribute.
	Organization string
	// CommonName populates the Subject's CommonName.
	CommonName string
	// ValidYears sets the cert lifetime in years. Zero falls back to
	// the SEP2 max validity (NotAfter = 9999-12-31).
	ValidYears int
}

// ServerCertOptions configures server certificate generation.
type ServerCertOptions struct {
	// Hosts lists DNS names and/or IP addresses to populate the
	// SubjectAlternativeName extension.
	Hosts []string
	// CommonName populates the Subject's CommonName.
	CommonName string
	// ValidYears sets the cert lifetime in years. Zero falls back to
	// the SEP2 max validity.
	ValidYears int
}

// AdminCertOptions configures admin certificate generation.
type AdminCertOptions struct {
	// CommonName populates the Subject's CommonName, identifying the
	// admin operator.
	CommonName string
	// ValidYears sets the cert lifetime in years. Zero falls back to
	// the SEP2 max validity.
	ValidYears int
}

// DeviceCertOptions configures device certificate generation per
// IEEE 2030.5-2018 §6.11.7.
type DeviceCertOptions struct {
	// DeviceType selects the device-type policy OID.
	DeviceType DeviceType
	// HWType is the manufacturer's hardware-type OID. Zero falls back
	// to the IEEE 2030.5 base OID.
	HWType asn1.ObjectIdentifier
	// HWSerialNum is the hardware serial number embedded in the
	// HardwareModuleName SAN extension. Empty omits the extension.
	HWSerialNum string
	// IsTestCert adds the device-auth-test policy OID.
	IsTestCert bool
	// ValidYears optionally sets the cert lifetime in years. Zero
	// falls back to the SEP2 §6.11.7 max validity (NotAfter =
	// 9999-12-31), the default for production device certs. Set a
	// non-zero value to issue short-lived certs (tests, embedded
	// operators rotating frequently).
	ValidYears int
	// CommonName optionally populates the Subject's CommonName. SEP2
	// §6.11.7 specifies an empty Subject for device certs; leaving
	// this empty preserves that default. Setting it is a non-spec
	// convenience for operators who want a label in the Subject.
	CommonName string
}
