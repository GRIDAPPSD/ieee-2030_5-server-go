package server_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-go/pkg/certs"
	pkgserver "github.com/GRIDAPPSD/ieee-2030_5-go/pkg/server"
)

// TestServerLoadsAdminCertServiceWhenCAKeyProvided verifies that
// supplying CAKeyFile alongside CAFile activates the admin cert API
// path inside Server.New. The actual HTTP behavior is exercised in
// internal/server tests; here we confirm New does not error when the
// admin path is wired up.
func TestServerLoadsAdminCertServiceWhenCAKeyProvided(t *testing.T) {
	dir, _ := mustWriteCerts(t)
	mustWriteCAKey(t, dir)
	port := freePort(t)

	srv, err := pkgserver.New(pkgserver.Config{
		Addr:      fmt.Sprintf("127.0.0.1:%d", port),
		CertFile:  filepath.Join(dir, "server.crt"),
		KeyFile:   filepath.Join(dir, "server.key"),
		CAFile:    filepath.Join(dir, "ca.crt"),
		CAKeyFile: filepath.Join(dir, "ca.key"),
	})
	if err != nil {
		t.Fatalf("New with CAKeyFile: %v", err)
	}
	if srv == nil {
		t.Fatal("New returned nil server")
	}

	// Smoke: Start/Shutdown completes cleanly.
	startErr := make(chan error, 1)
	go func() { startErr <- srv.Start(context.Background()) }()
	waitForListener(t, fmt.Sprintf("127.0.0.1:%d", port))

	shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutCtx); err != nil {
		t.Errorf("Shutdown: %v", err)
	}
	<-startErr
}

// TestServerNewWhenCAKeyFileUnset confirms that omitting CAKeyFile
// silently disables the admin cert API. New must not error in this
// case; the operator simply opts out of admin cert minting.
func TestServerNewWhenCAKeyFileUnset(t *testing.T) {
	dir, _ := mustWriteCerts(t)
	port := freePort(t)

	srv, err := pkgserver.New(pkgserver.Config{
		Addr:     fmt.Sprintf("127.0.0.1:%d", port),
		CertFile: filepath.Join(dir, "server.crt"),
		KeyFile:  filepath.Join(dir, "server.key"),
		CAFile:   filepath.Join(dir, "ca.crt"),
		// CAKeyFile intentionally empty
	})
	if err != nil {
		t.Fatalf("New with empty CAKeyFile should not error: %v", err)
	}
	if srv == nil {
		t.Fatal("New returned nil server")
	}
}

// TestServerNewWhenCAKeyFileUnreadable verifies that a CAKeyFile path
// pointing at a missing file is treated as a hard error. The previous
// behavior (silent disable) masked operator misconfiguration; the
// admin cert API is now opt-in via empty CAKeyFile, so any non-empty
// value MUST load.
func TestServerNewWhenCAKeyFileUnreadable(t *testing.T) {
	dir, _ := mustWriteCerts(t)
	port := freePort(t)

	_, err := pkgserver.New(pkgserver.Config{
		Addr:      fmt.Sprintf("127.0.0.1:%d", port),
		CertFile:  filepath.Join(dir, "server.crt"),
		KeyFile:   filepath.Join(dir, "server.key"),
		CAFile:    filepath.Join(dir, "ca.crt"),
		CAKeyFile: filepath.Join(dir, "definitely-not-here.key"),
	})
	if err == nil {
		t.Fatal("New with unreadable CAKeyFile should error, got nil")
	}
}

// TestServerShutdownBeforeStartIsNoop confirms idempotent Shutdown.
func TestServerShutdownBeforeStartIsNoop(t *testing.T) {
	dir, _ := mustWriteCerts(t)
	port := freePort(t)

	srv, err := pkgserver.New(pkgserver.Config{
		Addr:     fmt.Sprintf("127.0.0.1:%d", port),
		CertFile: filepath.Join(dir, "server.crt"),
		KeyFile:  filepath.Join(dir, "server.key"),
		CAFile:   filepath.Join(dir, "ca.crt"),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if err := srv.Shutdown(context.Background()); err != nil {
		t.Errorf("Shutdown before Start: %v", err)
	}
}

// TestServerValidateConfigEmptyAddr verifies the empty-Addr case is
// rejected by New (covers the validateConfig branch).
func TestServerValidateConfigEmptyAddr(t *testing.T) {
	_, err := pkgserver.New(pkgserver.Config{})
	if err == nil {
		t.Fatal("New with empty Config: want error, got nil")
	}
}

// mustWriteCAKey re-creates a CA cert+key pair in dir with the names
// used by the admin cert service (ca.crt + ca.key). It overwrites the
// ca.crt produced by mustWriteCerts to keep them consistent.
func mustWriteCAKey(t *testing.T, dir string) {
	t.Helper()
	caCertPEM, caKeyPEM, err := certs.GenerateCA(certs.CAOptions{
		CommonName: "Admin Test CA",
		ValidYears: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "ca.crt"), caCertPEM, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "ca.key"), caKeyPEM, 0600); err != nil {
		t.Fatal(err)
	}

	// Re-issue server cert against the new CA so mTLS still works
	// against this CA bundle.
	caBlock, _ := pem.Decode(caCertPEM)
	caCert, _ := x509.ParseCertificate(caBlock.Bytes)
	keyBlock, _ := pem.Decode(caKeyPEM)
	caKeyAny, _ := x509.ParsePKCS8PrivateKey(keyBlock.Bytes)
	caKey := caKeyAny.(*ecdsa.PrivateKey)

	srvCertPEM, srvKeyPEM, err := certs.GenerateServerCert(caCert, caKey, certs.ServerCertOptions{
		Hosts:      []string{"127.0.0.1", "localhost"},
		CommonName: "Admin Test Server",
		ValidYears: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "server.crt"), srvCertPEM, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "server.key"), srvKeyPEM, 0600); err != nil {
		t.Fatal(err)
	}
}
