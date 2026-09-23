package certs_test

// #638 fix round 1: LoadCAPair loads a CA certificate independently of its
// private key, so a deployment that keeps a certificate and removes its key
// still gets the certificate back (MEDIUM 1/3), and a certificate paired
// with a key that does not match it is treated as keyless rather than
// silently minting-ready (MEDIUM 2). Every case is proven against real
// generated key material, never a mocked pair.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/certs"
)

func TestLoadCAPair_MatchedPair(t *testing.T) {
	dir := t.TempDir()
	certPEM, keyPEM, err := certs.GenerateCA(certs.CAOptions{CommonName: "638 Matched CA", ValidYears: 1})
	if err != nil {
		t.Fatalf("GenerateCA: %v", err)
	}
	certFile := writeFile(t, dir, "ca.crt", certPEM)
	keyFile := writeFile(t, dir, "ca.key", keyPEM)

	cert, gotPEM, key, certErr, keyErr := certs.LoadCAPair(certFile, keyFile)
	if cert == nil {
		t.Fatal("cert = nil, want the parsed CA certificate")
	}
	if key == nil {
		t.Fatal("key = nil, want the parsed CA key for a matched pair")
	}
	if certErr != nil {
		t.Errorf("certErr = %v, want nil for a matched pair", certErr)
	}
	if keyErr != nil {
		t.Errorf("keyErr = %v, want nil for a matched pair", keyErr)
	}
	if string(gotPEM) != string(certPEM) {
		t.Error("certPEM does not equal the bytes on disk")
	}
}

// TestLoadCAPair_CertOnlyNoKeyFile is the fix for MEDIUM 1: a deployment
// that removed the CA key file still gets the certificate. Control:
// TestLoadCAPair_MatchedPair proves the same function returns a non-nil key
// when one is present, so this is not vacuously passing on an always-nil key.
func TestLoadCAPair_CertOnlyNoKeyFile(t *testing.T) {
	dir := t.TempDir()
	certPEM, _, err := certs.GenerateCA(certs.CAOptions{CommonName: "638 Keyless CA", ValidYears: 1})
	if err != nil {
		t.Fatalf("GenerateCA: %v", err)
	}
	certFile := writeFile(t, dir, "ca.crt", certPEM)
	missingKeyFile := filepath.Join(dir, "does-not-exist.key")

	cert, _, key, certErr, keyErr := certs.LoadCAPair(certFile, missingKeyFile)
	if cert == nil {
		t.Fatal("cert = nil, want the certificate to load even though the key file is absent")
	}
	if key != nil {
		t.Error("key != nil, want nil when the key file does not exist")
	}
	if certErr != nil {
		t.Errorf("certErr = %v, want nil: the certificate loaded, so nothing about it failed", certErr)
	}
	if keyErr == nil {
		t.Error("keyErr = nil, want a non-nil error naming the missing key")
	}
}

// TestLoadCAPair_MismatchedPair is the reproduction and fix for MEDIUM 2:
// a certificate rotated under SEP2_SERVING_CA/SEP2_DEVICE_CA while the old
// shared key stays in place must not load as a usable pair.
func TestLoadCAPair_MismatchedPair(t *testing.T) {
	dir := t.TempDir()
	newCertPEM, _, err := certs.GenerateCA(certs.CAOptions{CommonName: "638 New CA", ValidYears: 1})
	if err != nil {
		t.Fatalf("GenerateCA(new): %v", err)
	}
	_, oldKeyPEM, err := certs.GenerateCA(certs.CAOptions{CommonName: "638 Old CA", ValidYears: 1})
	if err != nil {
		t.Fatalf("GenerateCA(old): %v", err)
	}
	certFile := writeFile(t, dir, "ca.crt", newCertPEM)
	keyFile := writeFile(t, dir, "ca.key", oldKeyPEM)

	cert, _, key, certErr, keyErr := certs.LoadCAPair(certFile, keyFile)
	if cert == nil {
		t.Fatal("cert = nil, want the new certificate to still load")
	}
	if key != nil {
		t.Error("key != nil, want nil for a certificate/key pair from two different CAs")
	}
	if certErr != nil {
		t.Errorf("certErr = %v, want nil: the certificate itself parsed fine", certErr)
	}
	if keyErr == nil {
		t.Error("keyErr = nil, want a non-nil error naming the mismatch")
	}
}

// TestLoadCAPair_CertMissing is also the reproduction for #638 fix round 3
// item 6: a caller branching on keyErr alone must not report a missing
// certificate as a key problem. Before the fix, the missing-certificate
// error was returned AS keyErr with certErr never existing, so the two
// causes were indistinguishable from the caller's side; this asserts
// certErr carries the failure and keyErr does not, and that certErr names
// the certificate rather than the key.
func TestLoadCAPair_CertMissing(t *testing.T) {
	dir := t.TempDir()
	_, keyPEM, err := certs.GenerateCA(certs.CAOptions{CommonName: "638 CA", ValidYears: 1})
	if err != nil {
		t.Fatalf("GenerateCA: %v", err)
	}
	keyFile := writeFile(t, dir, "ca.key", keyPEM)
	missingCertFile := filepath.Join(dir, "does-not-exist.crt")

	cert, certPEM, key, certErr, keyErr := certs.LoadCAPair(missingCertFile, keyFile)
	if cert != nil {
		t.Error("cert != nil, want nil when the certificate file does not exist")
	}
	if certPEM != nil {
		t.Error("certPEM != nil, want nil alongside a nil cert")
	}
	if key != nil {
		t.Error("key != nil, want nil when the certificate itself never loaded")
	}
	if certErr == nil {
		t.Fatal("certErr = nil, want a non-nil error naming the missing certificate")
	}
	if !strings.Contains(certErr.Error(), "CA cert") {
		t.Errorf("certErr = %v, want it to name the certificate, not the key", certErr)
	}
	if keyErr != nil {
		t.Errorf("keyErr = %v, want nil: the key was never reached, so it must not carry the certificate's failure", keyErr)
	}
}

func writeFile(t *testing.T, dir, name string, data []byte) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, data, 0o600); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	return p
}
