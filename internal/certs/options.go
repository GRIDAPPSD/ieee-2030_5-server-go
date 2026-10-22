package certs

import (
	"encoding/asn1"
	"fmt"
)

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

// DefaultHWTypeOID is the hardware type OID used when a device certificate
// request names none. 40732 is a test private enterprise number, so a
// production device must carry its manufacturer's PEN instead. The Makefile
// and scripts/new-device.sh repeat this value; TestDefaultHWTypeOIDMatchesScripts
// keeps them equal.
const DefaultHWTypeOID = "1.3.6.1.4.1.40732.99"

// DefaultHWTypeEnv names the environment variable that overrides
// DefaultHWTypeOID for the server API and the sep2server certs command.
const DefaultHWTypeEnv = "SEP2_HW_TYPE"

// ResolveDefaultHWType returns the hardware type OID to use when a request
// names none: the value of SEP2_HW_TYPE when set, else DefaultHWTypeOID. A
// malformed SEP2_HW_TYPE is an error naming the variable, so callers fail at
// startup rather than on the first request.
func ResolveDefaultHWType(getenv func(string) string) (string, error) {
	v := getenv(DefaultHWTypeEnv)
	if v == "" {
		return DefaultHWTypeOID, nil
	}
	if _, err := ParseOID(v); err != nil {
		return "", fmt.Errorf("%s: %w", DefaultHWTypeEnv, err)
	}
	return v, nil
}
