package certs

import "encoding/asn1"

// CAOptions configures root CA certificate generation.
type CAOptions struct {
	Organization string
	CommonName   string
	ValidYears   int // 0 = use max validity (9999-12-31)
}

// ServerCertOptions configures server certificate generation.
type ServerCertOptions struct {
	Hosts      []string // DNS names and/or IP addresses
	CommonName string
	ValidYears int // 0 = use max validity
}

// AdminCertOptions configures admin certificate generation.
type AdminCertOptions struct {
	CommonName string
	ValidYears int // 0 = use max validity
}

// DeviceCertOptions configures device certificate generation.
type DeviceCertOptions struct {
	DeviceType  DeviceType
	HWType      asn1.ObjectIdentifier // manufacturer's hardware type OID
	HWSerialNum string                // hardware serial number
	IsTestCert  bool                  // adds device-auth-test policy OID
}
