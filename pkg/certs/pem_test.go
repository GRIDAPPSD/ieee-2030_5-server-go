package certs_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-go/pkg/certs"
)

func TestParseCertificatePEM(t *testing.T) {
	certPEM, _, err := certs.GenerateCA(certs.CAOptions{
		CommonName: "Parse Test CA",
		ValidYears: 1,
	})
	if err != nil {
		t.Fatal(err)
	}

	cert, err := certs.ParseCertificatePEM(certPEM)
	if err != nil {
		t.Fatalf("ParseCertificatePEM: %v", err)
	}
	if cert.Subject.CommonName != "Parse Test CA" {
		t.Errorf("CN = %q, want %q", cert.Subject.CommonName, "Parse Test CA")
	}
}

func TestParseCertificatePEMRejectsGarbage(t *testing.T) {
	if _, err := certs.ParseCertificatePEM([]byte("not a pem")); err == nil {
		t.Error("ParseCertificatePEM(garbage): want error, got nil")
	}
}

func TestParseKeyPEM(t *testing.T) {
	_, keyPEM, err := certs.GenerateCA(certs.CAOptions{
		CommonName: "Key Test CA",
		ValidYears: 1,
	})
	if err != nil {
		t.Fatal(err)
	}

	key, err := certs.ParseKeyPEM(keyPEM)
	if err != nil {
		t.Fatalf("ParseKeyPEM: %v", err)
	}
	if key == nil {
		t.Error("ParseKeyPEM returned nil key")
	}
}

func TestParseKeyPEMRejectsGarbage(t *testing.T) {
	if _, err := certs.ParseKeyPEM([]byte("not a pem")); err == nil {
		t.Error("ParseKeyPEM(garbage): want error, got nil")
	}
}

func TestLoadCA(t *testing.T) {
	dir := t.TempDir()
	certPEM, keyPEM, err := certs.GenerateCA(certs.CAOptions{
		CommonName: "LoadCA Test",
		ValidYears: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	certPath := filepath.Join(dir, "ca.crt")
	keyPath := filepath.Join(dir, "ca.key")
	if err := os.WriteFile(certPath, certPEM, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, keyPEM, 0600); err != nil {
		t.Fatal(err)
	}

	cert, key, err := certs.LoadCA(certPath, keyPath)
	if err != nil {
		t.Fatalf("LoadCA: %v", err)
	}
	if cert == nil || key == nil {
		t.Error("LoadCA returned nil cert or key")
	}
	if cert.Subject.CommonName != "LoadCA Test" {
		t.Errorf("CN = %q, want %q", cert.Subject.CommonName, "LoadCA Test")
	}
}

func TestLoadCAMissingCert(t *testing.T) {
	if _, _, err := certs.LoadCA("/nonexistent.crt", "/nonexistent.key"); err == nil {
		t.Error("LoadCA missing files: want error, got nil")
	}
}
