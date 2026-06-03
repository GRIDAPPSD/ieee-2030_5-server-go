package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/GRIDAPPSD/ieee-2030_5-go/internal/certs"
)

// IEEE-177: container runtime cert bootstrap.
//
// ensureCerts implements the "/certs-empty seam": the entrypoint generates a
// self-signed CA + server (+ admin) cert set into certDir ONLY when certDir
// has no pre-existing server identity. If a server cert is already present —
// whether minted on a prior container start (persisted in the named volume)
// or mounted in by an operator (the production secret path) — generation is
// skipped and the existing identity is reused verbatim.
//
// Why a Go subcommand instead of a shell entrypoint: cert generation is
// already pure Go (internal/certs), and the final image is
// distroless/static:nonroot — it has NO shell. Doing the bootstrap in the
// binary keeps that hardened base unchanged (no busybox/alpine swap, no
// added shell-injection surface) and lets os.WriteFile set the exact key
// mode (0600) that a shell `umask` dance can only approximate. The container
// ENTRYPOINT is `serve-init`, which calls ensureCerts then falls through to
// the normal serve path.
//
// Identity stability: the server derives its SFDI/LFDI from its leaf cert
// (see internal/server.deriveServerIdentity). Persisting server.crt/server.key
// across restarts is therefore what keeps the server's network identity
// stable. ensureCerts regenerates ONLY when the leaf is absent, so a restart
// against a populated /certs volume never mints a new identity.

// requiredServerCert is the file whose presence means "this dir already has a
// server identity — do not regenerate". The server's identity is derived from
// this leaf, so it is the authoritative existence marker for the seam.
const requiredServerCert = "server.crt"

// certInitHosts is the SAN host list baked into a generated server cert. The
// dev/in-container default covers loopback plus the compose service name so a
// device on the obs network can dial https://sep2server. Overridable via
// SEP2_CERT_HOSTS (CSV) for operators who generate in-container against a
// different name. The production path mounts an externally-issued cert and
// never reaches generation, so this default only governs the dev seam.
func certInitHosts() []string {
	if v := os.Getenv("SEP2_CERT_HOSTS"); v != "" {
		return parseCSV(v)
	}
	return []string{"localhost", "127.0.0.1", "sep2server"}
}

// ensureCerts realizes the gen-only-if-empty seam. It returns nil (no-op)
// when certDir already holds a server cert, and otherwise mints a fresh
// CA + server + admin set into certDir with key files at mode 0600.
//
// Fail-closed: any error generating or writing the set is returned to the
// caller, which must abort startup rather than serve without TLS material.
func ensureCerts(certDir string, hosts []string) error {
	serverCertPath := filepath.Join(certDir, requiredServerCert)
	if _, err := os.Stat(serverCertPath); err == nil {
		// Identity already present (persisted volume or mounted prod secret):
		// reuse it, do not regenerate. This is the prod drop-in point.
		return nil
	} else if !os.IsNotExist(err) {
		// Stat failed for a reason other than absence (permission, I/O):
		// fail closed rather than assume-empty and clobber.
		return fmt.Errorf("stat %s: %w", serverCertPath, err)
	}

	// Directory may not exist yet on a first-ever mount. MkdirAll is 0700 so
	// the cert material is owner-only at the directory level too.
	if err := os.MkdirAll(certDir, 0o700); err != nil {
		return fmt.Errorf("create cert dir %s: %w", certDir, err)
	}

	// CA.
	caCertPEM, caKeyPEM, err := certs.GenerateCA(certs.CAOptions{
		Organization: "IEEE 2030.5",
		CommonName:   "IEEE 2030.5 Root CA",
		ValidYears:   10,
	})
	if err != nil {
		return fmt.Errorf("generate CA: %w", err)
	}
	if err := writeCert(filepath.Join(certDir, "ca.crt"), caCertPEM); err != nil {
		return err
	}
	if err := writeKey(filepath.Join(certDir, "ca.key"), caKeyPEM); err != nil {
		return err
	}

	caCert, caKey, err := certs.LoadCA(filepath.Join(certDir, "ca.crt"), filepath.Join(certDir, "ca.key"))
	if err != nil {
		return fmt.Errorf("load freshly-generated CA: %w", err)
	}

	// Server leaf — this is what the server's identity is derived from.
	serverCertPEM, serverKeyPEM, err := certs.GenerateServerCert(caCert, caKey, certs.ServerCertOptions{
		Hosts:      hosts,
		CommonName: "IEEE 2030.5 Server",
		ValidYears: 1,
	})
	if err != nil {
		return fmt.Errorf("generate server cert: %w", err)
	}
	if err := writeCert(serverCertPath, serverCertPEM); err != nil {
		return err
	}
	if err := writeKey(filepath.Join(certDir, "server.key"), serverKeyPEM); err != nil {
		return err
	}

	// Admin leaf — used by the admin cert service.
	adminCertPEM, adminKeyPEM, err := certs.GenerateAdminCert(caCert, caKey, certs.AdminCertOptions{
		CommonName: "IEEE 2030.5 Admin",
		ValidYears: 1,
	})
	if err != nil {
		return fmt.Errorf("generate admin cert: %w", err)
	}
	if err := writeCert(filepath.Join(certDir, "admin.crt"), adminCertPEM); err != nil {
		return err
	}
	if err := writeKey(filepath.Join(certDir, "admin.key"), adminKeyPEM); err != nil {
		return err
	}

	return nil
}

// writeCert writes a PEM-encoded certificate at 0644 (public material).
func writeCert(path string, pemBytes []byte) error {
	if err := os.WriteFile(path, pemBytes, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

// writeKey writes a PEM-encoded private key at 0600 (owner read/write only).
// The mode is the security invariant: the key must never be group/world
// readable. os.WriteFile applies the mode on create; for an already-existing
// path we do not reach here because ensureCerts is gen-only-if-empty.
func writeKey(path string, pemBytes []byte) error {
	if err := os.WriteFile(path, pemBytes, 0o600); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	// Enforce 0600 even if a prior umask or pre-existing file widened it.
	if err := os.Chmod(path, 0o600); err != nil {
		return fmt.Errorf("chmod %s: %w", path, err)
	}
	return nil
}

// runServeInit is the container ENTRYPOINT command: bootstrap certs into the
// SEP2_CERT directory's parent (the /certs volume) if absent, then serve.
func runServeInit() error {
	certDir := envOr("SEP2_CERT_DIR", "/certs")
	if err := ensureCerts(certDir, certInitHosts()); err != nil {
		return fmt.Errorf("cert bootstrap: %w", err)
	}
	return runServe()
}
