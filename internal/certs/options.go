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
// request names none. 1.3.6.1.4.1.40732 is the IEEE 2030.5 arc and .99 is a
// project test convention, so a production device must carry its
// manufacturer's PEN instead. The Makefile and scripts/new-device.sh pass no
// hardware type of their own, so this is the only place the value is decided.
const DefaultHWTypeOID = "1.3.6.1.4.1." + DefaultPENText + ".99"

// DefaultPENText is the IEEE 2030.5 test PEN as a decimal string. It is the
// single place the number is written: DefaultHWTypeOID embeds it, and
// config.ParsePEN parses it as the SEP2_PEN default.
const DefaultPENText = "40732"

// DefaultHWTypeEnv names the environment variable that overrides
// DefaultHWTypeOID for the server API and the sep2server certs command.
const DefaultHWTypeEnv = "SEP2_HW_TYPE"

// ResolveDefaultHWType returns the hardware type OID to use when a request
// names none: the value of SEP2_HW_TYPE when set, else DefaultHWTypeOID, as
// the canonical OID string (so "1.+3" reads back as "1.3"). A malformed SEP2_HW_TYPE is an error naming the variable, so callers fail at
// startup rather than on the first request.
func ResolveDefaultHWType(getenv func(string) string) (string, error) {
	v := getenv(DefaultHWTypeEnv)
	if v == "" {
		return DefaultHWTypeOID, nil
	}
	oid, err := ParseOID(v)
	if err != nil {
		return "", fmt.Errorf("%s: %w", DefaultHWTypeEnv, err)
	}
	return oid.String(), nil
}
