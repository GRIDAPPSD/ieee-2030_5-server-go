// This file carries the CSIP device-certificate SAN requirement that the TLS
// handshake does not impose. The server hook
// sep2tls.VerifyPeerCertWithHardwareModuleSAN acknowledges a HardwareModuleName
// SAN when a leaf presents one, and accepts a leaf with no SAN at all, so a
// procedure that means to exercise IEEE 2030.5 6.11 material has to say so
// itself.
package csip_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"math/big"
	"testing"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2cert"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/certs"
)

// Distinct wording per branch so a test asserting one refusal cannot be
// satisfied by another, nor by the chain-verification failures elsewhere in
// this package.
var (
	errSANAbsent                = errors.New("CSIP 6.11 device cert: carries no SubjectAlternativeName extension")
	errSANNotCritical           = errors.New("CSIP 6.11 device cert: the SubjectAlternativeName is not marked critical")
	errSANNotHardwareModuleName = errors.New("CSIP 6.11 device cert: the SubjectAlternativeName carries an entry that is not an RFC 4108 HardwareModuleName")
)

// csipDeviceCertSAN reports why leaf fails the IEEE 2030.5 6.11 / CSIP 6.2
// device-certificate SAN requirement, and nil when it satisfies it.
//
// The SAN predicate comes from sep2cert because that is the package the
// server's handshake hook consults. internal/certs carries a copy of the same
// function, and asserting against the copy would keep reporting a pass through
// a core bump that changed the behaviour actually running.
func csipDeviceCertSAN(leaf *x509.Certificate) error {
	allHMN, sanPresent := sep2cert.SANAllOtherNamesAreHardwareModuleName(leaf)
	switch {
	case !sanPresent:
		return errSANAbsent
	case !sanCritical(leaf):
		return errSANNotCritical
	case !allHMN:
		return errSANNotHardwareModuleName
	}
	return nil
}

// sanCritical reports whether the SAN extension is marked critical. RFC 5280
// 4.2.1.6 requires that of an empty-Subject device cert, and a non-critical SAN
// never reaches the handshake hook's acknowledge path at all.
func sanCritical(leaf *x509.Certificate) bool {
	for _, ext := range leaf.Extensions {
		if ext.Id.Equal(sep2cert.OIDSubjectAltName) {
			return ext.Critical
		}
	}
	return false
}

// TestCSIPDeviceCertSANRequirement proves csipDeviceCertSAN refuses, and
// refuses for the right reason. It runs unconditionally, so the requirement is
// covered on runs that supply no SunSpec PKI and skip the procedure using it.
func TestCSIPDeviceCertSANRequirement(t *testing.T) {
	deviceLeaf := mintDeviceLeaf(t)
	hmnSAN := sanExtension(t, deviceLeaf)
	nonCriticalHMN := hmnSAN
	nonCriticalHMN.Critical = false

	tests := []struct {
		name    string
		leaf    *x509.Certificate
		wantErr error
	}{
		{name: "device leaf from the generator the server uses", leaf: deviceLeaf},
		{name: "no SAN extension", leaf: mintLeafWithSAN(t, nil), wantErr: errSANAbsent},
		{name: "HardwareModuleName SAN not marked critical", leaf: mintLeafWithSAN(t, &nonCriticalHMN), wantErr: errSANNotCritical},
		{name: "critical SAN carrying a dNSName", leaf: mintServerLeaf(t), wantErr: errSANNotHardwareModuleName},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := csipDeviceCertSAN(tc.leaf)
			if tc.wantErr == nil {
				if err != nil {
					t.Fatalf("csipDeviceCertSAN() = %v, want nil", err)
				}
				return
			}
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("csipDeviceCertSAN() = %v, want %v", err, tc.wantErr)
			}
		})
	}
}

// mintDeviceLeaf returns an ephemeral CSIP device leaf minted the way the
// server mints one, so the compliant row above tracks the generator rather than
// a hand-built approximation of it.
func mintDeviceLeaf(t *testing.T) *x509.Certificate {
	t.Helper()

	certPEM, _, _ := mintSunSpecMaterial(t)
	leaf, err := certs.ParseCertificatePEM(certPEM)
	if err != nil {
		t.Fatalf("parse minted device leaf: %v", err)
	}
	return leaf
}

// mintServerLeaf returns an ephemeral CSIP server leaf, whose SAN is critical
// and carries a dNSName rather than a HardwareModuleName.
func mintServerLeaf(t *testing.T) *x509.Certificate {
	t.Helper()

	caPEM, caKeyPEM, err := certs.GenerateCA(certs.CAOptions{CommonName: "SAN requirement test root"})
	if err != nil {
		t.Fatalf("generate CA: %v", err)
	}
	caCert, err := certs.ParseCertificatePEM(caPEM)
	if err != nil {
		t.Fatalf("parse CA cert: %v", err)
	}
	caKey, err := certs.ParseKeyPEM(caKeyPEM)
	if err != nil {
		t.Fatalf("parse CA key: %v", err)
	}
	certPEM, _, err := certs.GenerateServerCert(caCert, caKey, certs.ServerCertOptions{
		Hosts:      []string{"csip.invalid"},
		CommonName: "csip.invalid",
	})
	if err != nil {
		t.Fatalf("generate server cert: %v", err)
	}
	leaf, err := certs.ParseCertificatePEM(certPEM)
	if err != nil {
		t.Fatalf("parse minted server leaf: %v", err)
	}
	return leaf
}

// sanExtension returns the leaf's SAN extension. Copying a real one is how the
// non-critical row below gets well-formed HardwareModuleName bytes without
// rebuilding the ASN.1 by hand.
func sanExtension(t *testing.T, leaf *x509.Certificate) pkix.Extension {
	t.Helper()

	for _, ext := range leaf.Extensions {
		if ext.Id.Equal(sep2cert.OIDSubjectAltName) {
			return ext
		}
	}
	t.Fatal("minted device leaf has no SubjectAlternativeName to copy")
	return pkix.Extension{}
}

// mintLeafWithSAN returns a self-signed leaf carrying san, or carrying no SAN
// extension when san is nil. This material is only ever inspected, never
// presented in a handshake, so it needs no issuer and no matching key on disk.
func mintLeafWithSAN(t *testing.T, san *pkix.Extension) *x509.Certificate {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		NotBefore:             time.Now().Add(-time.Minute),
		NotAfter:              time.Now().AddDate(1, 0, 0),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
		BasicConstraintsValid: true,
	}
	if san != nil {
		template.ExtraExtensions = []pkix.Extension{*san}
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create leaf: %v", err)
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("parse leaf: %v", err)
	}
	return leaf
}
