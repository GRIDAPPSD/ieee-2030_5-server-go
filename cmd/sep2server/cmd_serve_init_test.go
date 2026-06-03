package main

import (
	"crypto/ecdsa"
	"crypto/x509"
	"os"
	"path/filepath"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-go/internal/certs"
	sepTLS "github.com/GRIDAPPSD/ieee-2030_5-go/internal/tls"
)

// TestEnsureCertsGeneratesIntoEmptyDir verifies the gen-when-empty half of
// the /certs seam: a fresh (empty) cert dir gets a complete CA + server +
// admin set written, and the resulting files parse as valid certs.
func TestEnsureCertsGeneratesIntoEmptyDir(t *testing.T) {
	dir := t.TempDir()

	if err := ensureCerts(dir, []string{"localhost", "127.0.0.1", "sep2server"}); err != nil {
		t.Fatalf("ensureCerts: %v", err)
	}

	for _, name := range []string{"ca.crt", "ca.key", "server.crt", "server.key", "admin.crt", "admin.key"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("expected %s to be generated: %v", name, err)
		}
	}

	// The server leaf must parse and carry the requested SAN host so a device
	// dialing https://sep2server validates the name.
	certPEM, err := os.ReadFile(filepath.Join(dir, "server.crt"))
	if err != nil {
		t.Fatalf("read server.crt: %v", err)
	}
	cert, err := certs.ParseCertificatePEM(certPEM)
	if err != nil {
		t.Fatalf("parse server.crt: %v", err)
	}
	if err := cert.VerifyHostname("sep2server"); err != nil {
		t.Errorf("server cert missing SAN for sep2server: %v", err)
	}
}

// TestEnsureCertsKeyModeIsOwnerOnly pins the private-key permission invariant:
// every generated *.key must be mode 0600 (owner read/write only), never
// group- or world-readable. This is the cert-handling security boundary.
func TestEnsureCertsKeyModeIsOwnerOnly(t *testing.T) {
	dir := t.TempDir()
	if err := ensureCerts(dir, []string{"localhost"}); err != nil {
		t.Fatalf("ensureCerts: %v", err)
	}

	for _, key := range []string{"ca.key", "server.key", "admin.key"} {
		info, err := os.Stat(filepath.Join(dir, key))
		if err != nil {
			t.Fatalf("stat %s: %v", key, err)
		}
		perm := info.Mode().Perm()
		if perm != 0o600 {
			t.Errorf("%s mode = %o, want 0600 (owner-only)", key, perm)
		}
		// Explicit world/group bit assertion: no read for group or other.
		if perm&0o077 != 0 {
			t.Errorf("%s is group/world accessible (mode %o); keys must be owner-only", key, perm)
		}
	}
}

// TestEnsureCertsSkipsWhenServerCertPresent verifies the gen-only-if-empty
// seam AND the identity-stability invariant: when /certs already holds a
// server cert (a prior container start, or a mounted prod secret), ensureCerts
// is a no-op — it does NOT regenerate, so the server's derived LFDI/SFDI
// identity is stable across restarts and a prod secret-mount is honored
// verbatim with no code change.
func TestEnsureCertsSkipsWhenServerCertPresent(t *testing.T) {
	dir := t.TempDir()

	// First call mints the identity.
	if err := ensureCerts(dir, []string{"localhost", "sep2server"}); err != nil {
		t.Fatalf("ensureCerts (first): %v", err)
	}

	before, err := os.ReadFile(filepath.Join(dir, "server.crt"))
	if err != nil {
		t.Fatalf("read server.crt after first gen: %v", err)
	}
	beforeCert, err := certs.ParseCertificatePEM(before)
	if err != nil {
		t.Fatalf("parse first server.crt: %v", err)
	}
	beforeLFDI := sepTLS.LFDI(beforeCert)

	// Second call must be a no-op: same bytes, same derived identity.
	if err := ensureCerts(dir, []string{"localhost", "sep2server"}); err != nil {
		t.Fatalf("ensureCerts (second): %v", err)
	}

	after, err := os.ReadFile(filepath.Join(dir, "server.crt"))
	if err != nil {
		t.Fatalf("read server.crt after second call: %v", err)
	}
	afterCert, err := certs.ParseCertificatePEM(after)
	if err != nil {
		t.Fatalf("parse second server.crt: %v", err)
	}
	afterLFDI := sepTLS.LFDI(afterCert)

	if string(before) != string(after) {
		t.Error("server.crt was rewritten on the second call; identity must be stable across restarts")
	}
	if beforeLFDI != afterLFDI {
		t.Errorf("server LFDI changed across restart: %s -> %s; identity must persist", beforeLFDI, afterLFDI)
	}
}

// TestEnsureCertsHonorsMountedProdSecret simulates the production drop-in:
// an externally-issued server cert/key is mounted at /certs BEFORE the
// process starts. ensureCerts must detect the existing leaf and skip
// generation entirely, leaving the operator's cert untouched. No code or
// image change distinguishes this from the dev path — only what is mounted.
func TestEnsureCertsHonorsMountedProdSecret(t *testing.T) {
	dir := t.TempDir()

	// Stand in for an externally-managed cert: generate one out-of-band and
	// place ONLY server.crt/server.key into the dir (as a secret mount would).
	caCertPEM, caKeyPEM, err := certs.GenerateCA(certs.CAOptions{Organization: "Prod", CommonName: "Prod CA", ValidYears: 5})
	if err != nil {
		t.Fatalf("GenerateCA: %v", err)
	}
	caCert, caKey, err := loadCAFromPEM(t, dir, caCertPEM, caKeyPEM)
	if err != nil {
		t.Fatalf("loadCAFromPEM: %v", err)
	}
	prodCertPEM, prodKeyPEM, err := certs.GenerateServerCert(caCert, caKey, certs.ServerCertOptions{
		Hosts:      []string{"sep2.prod.example"},
		CommonName: "Prod Server",
		ValidYears: 1,
	})
	if err != nil {
		t.Fatalf("GenerateServerCert: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "server.crt"), prodCertPEM, 0o644); err != nil {
		t.Fatalf("write prod server.crt: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "server.key"), prodKeyPEM, 0o600); err != nil {
		t.Fatalf("write prod server.key: %v", err)
	}

	// ensureCerts must NOT touch the mounted secret.
	if err := ensureCerts(dir, []string{"localhost"}); err != nil {
		t.Fatalf("ensureCerts over mounted secret: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(dir, "server.crt"))
	if err != nil {
		t.Fatalf("read server.crt: %v", err)
	}
	if string(got) != string(prodCertPEM) {
		t.Error("ensureCerts overwrote a mounted production server cert; the prod secret-mount seam was violated")
	}
}

// loadCAFromPEM writes the CA pair to dir under temporary names and loads it,
// so the prod-secret test can sign a leaf with the existing LoadCA path
// without depending on internal constructors.
func loadCAFromPEM(t *testing.T, dir string, caCertPEM, caKeyPEM []byte) (*x509.Certificate, *ecdsa.PrivateKey, error) {
	t.Helper()
	caCrt := filepath.Join(dir, "_prodca.crt")
	caKeyF := filepath.Join(dir, "_prodca.key")
	if err := os.WriteFile(caCrt, caCertPEM, 0o644); err != nil {
		return nil, nil, err
	}
	if err := os.WriteFile(caKeyF, caKeyPEM, 0o600); err != nil {
		return nil, nil, err
	}
	c, k, err := certs.LoadCA(caCrt, caKeyF)
	// Clean up the throwaway CA files so they don't masquerade as cert dir
	// contents in the assertion below.
	_ = os.Remove(caCrt)
	_ = os.Remove(caKeyF)
	return c, k, err
}
