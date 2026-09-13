package csiptest

import (
	"crypto/tls"
	"crypto/x509"
	"os"
	"path/filepath"
	"testing"

	sepTLS "github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2tls"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/certs"
)

// DeviceIdentity is a device certificate a test presents to a booted server,
// with the LFDI and SFDI the server derives from it.
//
// The server authorizes EndDevice access on the certificate, so a test that
// addresses a fixture EndDevice binds that record to the identity it presents
// (see [Bind]) instead of relying on the LFDI written in the fixture.
type DeviceIdentity struct {
	Cert   tls.Certificate
	CAFile string // PEM of the CA that signed Cert, for WithClientCAsFile
	LFDI   string
	SFDI   string
}

// NewDeviceIdentity mints an ephemeral CA and a device certificate signed by
// it. Only the CA certificate is written, under t.TempDir(); no key material
// leaves memory.
func NewDeviceIdentity(t *testing.T, hwSerial string) DeviceIdentity {
	t.Helper()
	caCertPEM, caKeyPEM, err := certs.GenerateCA(certs.CAOptions{
		CommonName: "CSIP Device Identity CA",
		ValidYears: 1,
	})
	if err != nil {
		t.Fatalf("csiptest: device identity: generate CA: %v", err)
	}
	caCert, err := certs.ParseCertificatePEM(caCertPEM)
	if err != nil {
		t.Fatalf("csiptest: device identity: parse CA cert: %v", err)
	}
	caKey, err := certs.ParseKeyPEM(caKeyPEM)
	if err != nil {
		t.Fatalf("csiptest: device identity: parse CA key: %v", err)
	}
	devCertPEM, devKeyPEM, err := certs.GenerateDeviceCert(caCert, caKey, certs.DeviceCertOptions{
		DeviceType:  certs.DeviceTypeGeneric,
		HWSerialNum: hwSerial,
	})
	if err != nil {
		t.Fatalf("csiptest: device identity: generate device cert: %v", err)
	}
	cert, err := tls.X509KeyPair(devCertPEM, devKeyPEM)
	if err != nil {
		t.Fatalf("csiptest: device identity: load key pair: %v", err)
	}
	leaf, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		t.Fatalf("csiptest: device identity: parse leaf: %v", err)
	}
	caFile := filepath.Join(t.TempDir(), "device-identity-ca.pem")
	if err := os.WriteFile(caFile, caCertPEM, 0o600); err != nil {
		t.Fatalf("csiptest: device identity: write CA: %v", err)
	}
	return DeviceIdentity{Cert: cert, CAFile: caFile, LFDI: sepTLS.LFDI(leaf), SFDI: sepTLS.SFDI(leaf)}
}

// WithDeviceIdentity makes the booted server trust id's CA and the returned
// Client present id's certificate.
func WithDeviceIdentity(id DeviceIdentity) BootOption {
	return func(c *bootCfg) {
		cert := id.Cert
		c.clientCert = &cert
		c.clientCAsPath = id.CAFile
	}
}
