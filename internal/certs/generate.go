package certs

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"time"
)

var maxValidity = time.Date(9999, 12, 31, 23, 59, 59, 0, time.UTC)

// GenerateCA creates a self-signed ECDSA P-256 root CA certificate.
func GenerateCA(opts CAOptions) (certPEM, keyPEM []byte, err error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, fmt.Errorf("generate CA key: %w", err)
	}

	serial, err := randomSerial()
	if err != nil {
		return nil, nil, err
	}

	notAfter := maxValidity
	if opts.ValidYears > 0 {
		notAfter = time.Now().AddDate(opts.ValidYears, 0, 0)
	}

	template := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			Organization: []string{opts.Organization},
			CommonName:   opts.CommonName,
		},
		NotBefore:             time.Now().Add(-1 * time.Minute),
		NotAfter:              notAfter,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLenZero:        false,
	}

	// Add anyPolicy certificate policy (critical per IEEE 2030.5 section 6.11)
	policyExt, err := buildCertPolicies(asn1.ObjectIdentifier{2, 5, 29, 32, 0})
	if err != nil {
		return nil, nil, fmt.Errorf("build CA policy: %w", err)
	}
	policyExt.Critical = true
	template.ExtraExtensions = append(template.ExtraExtensions, policyExt)

	certDER, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return nil, nil, fmt.Errorf("create CA certificate: %w", err)
	}

	certPEM = encodeCertPEM(certDER)
	keyPEM, err = encodeKeyPEM(key)
	if err != nil {
		return nil, nil, err
	}

	return certPEM, keyPEM, nil
}

// GenerateServerCert creates a server certificate signed by the given CA.
func GenerateServerCert(caCert *x509.Certificate, caKey *ecdsa.PrivateKey, opts ServerCertOptions) (certPEM, keyPEM []byte, err error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, fmt.Errorf("generate server key: %w", err)
	}

	serial, err := randomSerial()
	if err != nil {
		return nil, nil, err
	}

	notAfter := maxValidity
	if opts.ValidYears > 0 {
		notAfter = time.Now().AddDate(opts.ValidYears, 0, 0)
	}

	template := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			CommonName: opts.CommonName,
		},
		NotBefore: time.Now().Add(-1 * time.Minute),
		NotAfter:  notAfter,
		KeyUsage:  x509.KeyUsageDigitalSignature | x509.KeyUsageKeyAgreement,
		ExtKeyUsage: []x509.ExtKeyUsage{
			x509.ExtKeyUsageServerAuth,
			x509.ExtKeyUsageClientAuth,
		},
		BasicConstraintsValid: true,
	}

	// Add CertificatePolicies extension (required by EPRI client check_cert)
	policyExt, err := buildCertPolicies(OIDPolicyServiceProv)
	if err != nil {
		return nil, nil, fmt.Errorf("build server policy: %w", err)
	}
	policyExt.Critical = true
	template.ExtraExtensions = append(template.ExtraExtensions, policyExt)

	// Build SAN as critical extension (EPRI client compatibility — its check_cert
	// has a C fall-through that requires SAN to be critical. Harmless per RFC 5280.)
	sanExt, err := buildCriticalSAN(opts.Hosts)
	if err != nil {
		return nil, nil, fmt.Errorf("build SAN: %w", err)
	}
	template.ExtraExtensions = append(template.ExtraExtensions, sanExt)

	certDER, err := x509.CreateCertificate(rand.Reader, template, caCert, &key.PublicKey, caKey)
	if err != nil {
		return nil, nil, fmt.Errorf("create server certificate: %w", err)
	}

	certPEM = encodeCertPEM(certDER)
	keyPEM, err = encodeKeyPEM(key)
	if err != nil {
		return nil, nil, err
	}

	return certPEM, keyPEM, nil
}

// GenerateAdminCert creates an admin certificate signed by the given CA.
// Admin certs carry the admin policy OID and have a Subject CN identifying the admin.
func GenerateAdminCert(caCert *x509.Certificate, caKey *ecdsa.PrivateKey, opts AdminCertOptions) (certPEM, keyPEM []byte, err error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, fmt.Errorf("generate admin key: %w", err)
	}

	serial, err := randomSerial()
	if err != nil {
		return nil, nil, err
	}

	notAfter := maxValidity
	if opts.ValidYears > 0 {
		notAfter = time.Now().AddDate(opts.ValidYears, 0, 0)
	}

	template := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			CommonName: opts.CommonName,
		},
		NotBefore: time.Now().Add(-1 * time.Minute),
		NotAfter:  notAfter,
		KeyUsage:  x509.KeyUsageDigitalSignature | x509.KeyUsageKeyAgreement,
		ExtKeyUsage: []x509.ExtKeyUsage{
			x509.ExtKeyUsageClientAuth,
		},
		BasicConstraintsValid: true,
	}

	policyExt, err := buildCertPolicies(OIDPolicyAdmin)
	if err != nil {
		return nil, nil, fmt.Errorf("build admin policy: %w", err)
	}
	template.ExtraExtensions = append(template.ExtraExtensions, policyExt)

	certDER, err := x509.CreateCertificate(rand.Reader, template, caCert, &key.PublicKey, caKey)
	if err != nil {
		return nil, nil, fmt.Errorf("create admin certificate: %w", err)
	}

	certPEM = encodeCertPEM(certDER)
	keyPEM, err = encodeKeyPEM(key)
	if err != nil {
		return nil, nil, err
	}

	return certPEM, keyPEM, nil
}

// GenerateSelfSignedTLS creates a self-signed ECDSA P-256 certificate
// for server-only TLS (no client auth). Used for the admin HTTPS listener.
func GenerateSelfSignedTLS(hosts []string) (certPEM, keyPEM []byte, err error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, fmt.Errorf("generate self-signed key: %w", err)
	}

	serial, err := randomSerial()
	if err != nil {
		return nil, nil, err
	}

	template := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			CommonName: "IEEE 2030.5 Admin",
		},
		NotBefore: time.Now().Add(-1 * time.Minute),
		NotAfter:  time.Now().AddDate(1, 0, 0),
		KeyUsage:  x509.KeyUsageDigitalSignature | x509.KeyUsageKeyAgreement,
		ExtKeyUsage: []x509.ExtKeyUsage{
			x509.ExtKeyUsageServerAuth,
		},
		BasicConstraintsValid: true,
	}

	for _, h := range hosts {
		if ip := net.ParseIP(h); ip != nil {
			template.IPAddresses = append(template.IPAddresses, ip)
		} else {
			template.DNSNames = append(template.DNSNames, h)
		}
	}

	certDER, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return nil, nil, fmt.Errorf("create self-signed certificate: %w", err)
	}

	certPEM = encodeCertPEM(certDER)
	keyPEM, err = encodeKeyPEM(key)
	if err != nil {
		return nil, nil, err
	}

	return certPEM, keyPEM, nil
}

// GenerateDeviceCert creates a device certificate signed by the given CA.
// Per IEEE 2030.5 spec section 6.11.7, device certs have an empty Subject
// and include deviceType and policy OIDs in certificate extensions.
func GenerateDeviceCert(caCert *x509.Certificate, caKey *ecdsa.PrivateKey, opts DeviceCertOptions) (certPEM, keyPEM []byte, err error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, fmt.Errorf("generate device key: %w", err)
	}

	serial, err := randomSerial()
	if err != nil {
		return nil, nil, err
	}

	template := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{},
		NotBefore:    time.Now().Add(-1 * time.Minute),
		NotAfter:     maxValidity,
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyAgreement,
		ExtKeyUsage: []x509.ExtKeyUsage{
			x509.ExtKeyUsageClientAuth,
		},
		BasicConstraintsValid: true,
	}

	// Build certificate policies with deviceType OID
	policyOIDs := []asn1.ObjectIdentifier{opts.DeviceType.OID()}
	if opts.IsTestCert {
		policyOIDs = append(policyOIDs, OIDPolicyDevAuthTest)
	}

	policyExt, err := buildCertPolicies(policyOIDs...)
	if err != nil {
		return nil, nil, fmt.Errorf("build device policy: %w", err)
	}
	policyExt.Critical = true
	template.ExtraExtensions = append(template.ExtraExtensions, policyExt)

	// Build HardwareModuleName SAN extension. Required by CSIP §6.2 /
	// IEEE 2030.5 §6.11 for every device cert participating in CSIP
	// registration — silently omitting it produces a non-compliant cert.
	if opts.HWSerialNum == "" {
		return nil, nil, fmt.Errorf("device cert: HWSerialNum is required (CSIP §6.2 HardwareModuleName SAN)")
	}
	sanExt, err := buildHardwareModuleNameSAN(opts.HWType, opts.HWSerialNum)
	if err != nil {
		return nil, nil, fmt.Errorf("build HW SAN: %w", err)
	}
	template.ExtraExtensions = append(template.ExtraExtensions, sanExt)

	certDER, err := x509.CreateCertificate(rand.Reader, template, caCert, &key.PublicKey, caKey)
	if err != nil {
		return nil, nil, fmt.Errorf("create device certificate: %w", err)
	}

	certPEM = encodeCertPEM(certDER)
	keyPEM, err = encodeKeyPEM(key)
	if err != nil {
		return nil, nil, err
	}

	return certPEM, keyPEM, nil
}

// buildCriticalSAN builds a SubjectAlternativeName extension with Critical=true.
// Go's x509.CreateCertificate auto-generates SAN as non-critical from DNSNames/IPAddresses,
// so we build it manually to set Critical=true (EPRI client compatibility).
func buildCriticalSAN(hosts []string) (pkix.Extension, error) {
	var rawValues []asn1.RawValue

	for _, h := range hosts {
		if ip := net.ParseIP(h); ip != nil {
			// iPAddress [7]
			ipBytes := ip.To4()
			if ipBytes == nil {
				ipBytes = ip.To16()
			}
			rawValues = append(rawValues, asn1.RawValue{
				Tag:   7,
				Class: asn1.ClassContextSpecific,
				Bytes: ipBytes,
			})
		} else {
			// dNSName [2]
			rawValues = append(rawValues, asn1.RawValue{
				Tag:   2,
				Class: asn1.ClassContextSpecific,
				Bytes: []byte(h),
			})
		}
	}

	val, err := asn1.Marshal(rawValues)
	if err != nil {
		return pkix.Extension{}, fmt.Errorf("marshal SAN: %w", err)
	}

	return pkix.Extension{
		Id:       OIDSubjectAltName,
		Critical: true,
		Value:    val,
	}, nil
}

// buildCertPolicies creates a CertificatePolicies extension containing
// the given policy OIDs as PolicyInformation structures.
func buildCertPolicies(oids ...asn1.ObjectIdentifier) (pkix.Extension, error) {
	type policyInformation struct {
		PolicyIdentifier asn1.ObjectIdentifier
	}

	var policies []policyInformation
	for _, oid := range oids {
		policies = append(policies, policyInformation{PolicyIdentifier: oid})
	}

	val, err := asn1.Marshal(policies)
	if err != nil {
		return pkix.Extension{}, fmt.Errorf("marshal policies: %w", err)
	}

	return pkix.Extension{
		Id:    OIDCertificatePolicies,
		Value: val,
	}, nil
}

// hardwareModuleName is the ASN.1 structure for id-on-hardwareModuleName
// per RFC 4108 section 5.
type hardwareModuleName struct {
	HWType      asn1.ObjectIdentifier
	HWSerialNum asn1.RawValue
}

// buildHardwareModuleNameSAN creates a SubjectAlternativeName extension
// containing a HardwareModuleName OtherName entry.
func buildHardwareModuleNameSAN(hwType asn1.ObjectIdentifier, hwSerialNum string) (pkix.Extension, error) {
	if hwType == nil {
		hwType = OIDIeee20305
	}

	hmn := hardwareModuleName{
		HWType: hwType,
		HWSerialNum: asn1.RawValue{
			Class: asn1.ClassUniversal,
			Tag:   asn1.TagOctetString,
			Bytes: []byte(hwSerialNum),
		},
	}

	hmnBytes, err := asn1.Marshal(hmn)
	if err != nil {
		return pkix.Extension{}, fmt.Errorf("marshal HardwareModuleName: %w", err)
	}

	// Wrap in OtherName: [0] { type-id OID, value [0] EXPLICIT ... }
	otherName := struct {
		TypeID asn1.ObjectIdentifier
		Value  asn1.RawValue
	}{
		TypeID: OIDHardwareModuleName,
		Value: asn1.RawValue{
			Class:      asn1.ClassContextSpecific,
			Tag:        0,
			IsCompound: true,
			Bytes:      hmnBytes,
		},
	}

	otherNameBytes, err := asn1.MarshalWithParams(otherName, "tag:0")
	if err != nil {
		return pkix.Extension{}, fmt.Errorf("marshal OtherName: %w", err)
	}

	// GeneralNames is a SEQUENCE of GeneralName
	sanValue, err := asn1.Marshal(asn1.RawValue{
		Class:      asn1.ClassUniversal,
		Tag:        asn1.TagSequence,
		IsCompound: true,
		Bytes:      otherNameBytes,
	})
	if err != nil {
		return pkix.Extension{}, fmt.Errorf("marshal SAN: %w", err)
	}

	// Device certs use an empty Subject, so RFC 5280 §4.2.1.6 requires the
	// SAN extension to be marked critical.
	return pkix.Extension{
		Id:       OIDSubjectAltName,
		Critical: true,
		Value:    sanValue,
	}, nil
}

func randomSerial() (*big.Int, error) {
	serialBytes := make([]byte, 16)
	if _, err := rand.Read(serialBytes); err != nil {
		return nil, fmt.Errorf("generate serial: %w", err)
	}
	serial := new(big.Int).SetBytes(serialBytes)
	return serial, nil
}

func encodeCertPEM(derBytes []byte) []byte {
	return pem.EncodeToMemory(&pem.Block{
		Type:  "CERTIFICATE",
		Bytes: derBytes,
	})
}

func encodeKeyPEM(key *ecdsa.PrivateKey) ([]byte, error) {
	pkcs8Bytes, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, fmt.Errorf("marshal PKCS8 key: %w", err)
	}
	return pem.EncodeToMemory(&pem.Block{
		Type:  "PRIVATE KEY",
		Bytes: pkcs8Bytes,
	}), nil
}
