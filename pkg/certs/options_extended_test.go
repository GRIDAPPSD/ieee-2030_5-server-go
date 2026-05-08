package certs_test

import (
	"crypto/ecdsa"
	"crypto/x509"
	"encoding/pem"
	"testing"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-go/pkg/certs"
)

// TestDeviceCertValidYears verifies that DeviceCertOptions.ValidYears
// shortens the device cert validity from the SEP2 max (year 9999) to
// time.Now() + N years.
func TestDeviceCertValidYears(t *testing.T) {
	caCert, caKey := newTestCA(t)

	devCertPEM, _, err := certs.GenerateDeviceCert(caCert, caKey, certs.DeviceCertOptions{
		DeviceType:  certs.DeviceTypeGeneric,
		HWSerialNum: "TEST-VY",
		ValidYears:  2,
	})
	if err != nil {
		t.Fatalf("GenerateDeviceCert: %v", err)
	}

	cert := mustParseCert(t, devCertPEM)

	want := time.Now().AddDate(2, 0, 0)
	delta := cert.NotAfter.Sub(want).Abs()
	if delta > time.Minute {
		t.Errorf("NotAfter = %v, want ~%v (delta %v)", cert.NotAfter, want, delta)
	}
}

// TestDeviceCertValidYearsZeroPreservesMaxValidity verifies that a
// ValidYears of 0 keeps the existing SEP2 §6.11.7 behavior of NotAfter
// = year 9999.
func TestDeviceCertValidYearsZeroPreservesMaxValidity(t *testing.T) {
	caCert, caKey := newTestCA(t)

	devCertPEM, _, err := certs.GenerateDeviceCert(caCert, caKey, certs.DeviceCertOptions{
		DeviceType:  certs.DeviceTypeGeneric,
		HWSerialNum: "TEST-MV",
	})
	if err != nil {
		t.Fatalf("GenerateDeviceCert: %v", err)
	}

	cert := mustParseCert(t, devCertPEM)

	if cert.NotAfter.Year() != 9999 {
		t.Errorf("NotAfter.Year() = %d, want 9999 (SEP2 max validity)", cert.NotAfter.Year())
	}
}

// TestDeviceCertCommonName verifies that DeviceCertOptions.CommonName
// is populated in the cert subject. SEP2 §6.11.7 specifies an empty
// subject for device certs, but operators sometimes need a CN for
// labeling. When unset (default), Subject.CommonName must remain empty.
func TestDeviceCertCommonName(t *testing.T) {
	caCert, caKey := newTestCA(t)

	devCertPEM, _, err := certs.GenerateDeviceCert(caCert, caKey, certs.DeviceCertOptions{
		DeviceType:  certs.DeviceTypeGeneric,
		HWSerialNum: "TEST-CN",
		CommonName:  "device-42",
	})
	if err != nil {
		t.Fatalf("GenerateDeviceCert: %v", err)
	}

	cert := mustParseCert(t, devCertPEM)

	if cert.Subject.CommonName != "device-42" {
		t.Errorf("Subject.CommonName = %q, want %q", cert.Subject.CommonName, "device-42")
	}
}

// TestDeviceCertCommonNameDefaultEmpty verifies that without
// CommonName, the cert subject stays empty per SEP2 §6.11.7.
func TestDeviceCertCommonNameDefaultEmpty(t *testing.T) {
	caCert, caKey := newTestCA(t)

	devCertPEM, _, err := certs.GenerateDeviceCert(caCert, caKey, certs.DeviceCertOptions{
		DeviceType:  certs.DeviceTypeGeneric,
		HWSerialNum: "TEST-NOCN",
	})
	if err != nil {
		t.Fatalf("GenerateDeviceCert: %v", err)
	}

	cert := mustParseCert(t, devCertPEM)

	if cert.Subject.CommonName != "" {
		t.Errorf("Subject.CommonName = %q, want empty", cert.Subject.CommonName)
	}
}

func newTestCA(t *testing.T) (*x509.Certificate, *ecdsa.PrivateKey) {
	t.Helper()
	caCertPEM, caKeyPEM, err := certs.GenerateCA(certs.CAOptions{
		CommonName: "Test CA",
		ValidYears: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	caBlock, _ := pem.Decode(caCertPEM)
	caCert, err := x509.ParseCertificate(caBlock.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	keyBlock, _ := pem.Decode(caKeyPEM)
	caKey, err := x509.ParsePKCS8PrivateKey(keyBlock.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	return caCert, caKey.(*ecdsa.PrivateKey)
}

func mustParseCert(t *testing.T, certPEM []byte) *x509.Certificate {
	t.Helper()
	block, _ := pem.Decode(certPEM)
	if block == nil {
		t.Fatal("decode cert PEM: no block")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("parse cert: %v", err)
	}
	return cert
}
