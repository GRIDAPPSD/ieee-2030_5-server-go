package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/certs"
)

// TestSecureDir pins the mode guarantee item 1 (#601 review) establishes:
// a fresh directory is created 0700, an existing wider one is narrowed to
// 0700, an existing narrower one is left alone (never widened), and a
// symlinked path is refused rather than followed.
func TestSecureDir(t *testing.T) {
	t.Run("absent directory is created 0700", func(t *testing.T) {
		base := t.TempDir()
		dir := filepath.Join(base, "tls")
		if err := secureDir(dir); err != nil {
			t.Fatalf("secureDir: %v", err)
		}
		info, err := os.Stat(dir)
		if err != nil {
			t.Fatalf("stat: %v", err)
		}
		if got := info.Mode().Perm(); got != 0700 {
			t.Errorf("mode = %o, want 700", got)
		}
	})

	t.Run("existing wider directory is narrowed to 0700", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.Chmod(dir, 0755); err != nil {
			t.Fatalf("chmod setup: %v", err)
		}
		if err := secureDir(dir); err != nil {
			t.Fatalf("secureDir: %v", err)
		}
		info, err := os.Stat(dir)
		if err != nil {
			t.Fatalf("stat: %v", err)
		}
		if got := info.Mode().Perm(); got != 0700 {
			t.Errorf("mode = %o, want 700 (narrowed from 755)", got)
		}
	})

	t.Run("existing narrower directory is left unchanged, never widened", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.Chmod(dir, 0500); err != nil {
			t.Fatalf("chmod setup: %v", err)
		}
		if err := secureDir(dir); err != nil {
			t.Fatalf("secureDir: %v", err)
		}
		info, err := os.Stat(dir)
		if err != nil {
			t.Fatalf("stat: %v", err)
		}
		if got := info.Mode().Perm(); got != 0500 {
			t.Errorf("mode = %o, want 500 unchanged (widening to 700 would add the missing write bit)", got)
		}
	})

	t.Run("a symlinked path is refused, not followed", func(t *testing.T) {
		base := t.TempDir()
		real := filepath.Join(base, "real")
		if err := os.Mkdir(real, 0755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		link := filepath.Join(base, "link")
		if err := os.Symlink(real, link); err != nil {
			t.Fatalf("symlink: %v", err)
		}
		if err := secureDir(link); err == nil {
			t.Fatal("secureDir on a symlinked path: want an error, got nil")
		}
		info, err := os.Stat(real)
		if err != nil {
			t.Fatalf("stat real: %v", err)
		}
		if got := info.Mode().Perm(); got != 0755 {
			t.Errorf("real dir mode = %o, want 755 unchanged (secureDir must not act through the symlink)", got)
		}
	})

	t.Run("a regular file at the path is refused", func(t *testing.T) {
		base := t.TempDir()
		path := filepath.Join(base, "not-a-dir")
		if err := os.WriteFile(path, []byte("x"), 0600); err != nil {
			t.Fatalf("write: %v", err)
		}
		if err := secureDir(path); err == nil {
			t.Fatal("secureDir on a regular file: want an error, got nil")
		}
	})
}

// TestRunGenerateCANarrowsExistingKeyAndDir pins review finding 1 (#601):
// a pre-existing key file and a pre-existing wider directory must both end
// up narrowed after generate-ca runs, and the file's contents must be the
// freshly generated key, not the placeholder it started as.
func TestRunGenerateCANarrowsExistingKeyAndDir(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0755); err != nil {
		t.Fatalf("chmod setup: %v", err)
	}
	keyPath := filepath.Join(dir, "ca.key")
	placeholder := []byte("not a real key yet")
	if err := os.WriteFile(keyPath, placeholder, 0644); err != nil {
		t.Fatalf("write placeholder: %v", err)
	}

	if err := runGenerateCA([]string{"-out", dir}); err != nil {
		t.Fatalf("runGenerateCA: %v", err)
	}

	dirInfo, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat dir: %v", err)
	}
	if got := dirInfo.Mode().Perm(); got != 0700 {
		t.Errorf("dir mode = %o, want 700", got)
	}
	keyInfo, err := os.Lstat(keyPath)
	if err != nil {
		t.Fatalf("lstat key: %v", err)
	}
	if keyInfo.Mode()&os.ModeSymlink != 0 {
		t.Fatal("ca.key is a symlink after generate-ca")
	}
	if got := keyInfo.Mode().Perm(); got != 0600 {
		t.Errorf("key mode = %o, want 600 (was 644 before this run)", got)
	}
	got, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatalf("read key: %v", err)
	}
	if strings.Contains(string(got), string(placeholder)) {
		t.Fatal("ca.key still holds the placeholder content; the fresh key was not written")
	}
	if !strings.Contains(string(got), "PRIVATE KEY") {
		t.Errorf("ca.key content = %q, want a PEM private key block", got)
	}
}

// TestRunGenerateCAReplacesSymlinkedKey pins review finding 1's symlink
// case: a symlink at the key path must be replaced with a regular file
// holding the new key, and the file the symlink pointed at must be left
// untouched. That is item 2's chosen behavior (replace, not follow),
// which atomicfile.Write's rename-based commit gives for free: POSIX
// rename(2) replaces whatever inode currently sits at the destination
// path, symlink included, rather than writing through it.
func TestRunGenerateCAReplacesSymlinkedKey(t *testing.T) {
	base := t.TempDir()
	dest := filepath.Join(base, "dest")
	outside := filepath.Join(base, "outside")
	if err := os.Mkdir(dest, 0700); err != nil {
		t.Fatalf("mkdir dest: %v", err)
	}
	if err := os.Mkdir(outside, 0700); err != nil {
		t.Fatalf("mkdir outside: %v", err)
	}
	target := filepath.Join(outside, "leaked.key")
	sentinel := []byte("pre-existing file the symlink points at")
	if err := os.WriteFile(target, sentinel, 0644); err != nil {
		t.Fatalf("write target: %v", err)
	}
	keyPath := filepath.Join(dest, "ca.key")
	if err := os.Symlink(target, keyPath); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	if err := runGenerateCA([]string{"-out", dest}); err != nil {
		t.Fatalf("runGenerateCA: %v", err)
	}

	info, err := os.Lstat(keyPath)
	if err != nil {
		t.Fatalf("lstat: %v", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		t.Fatal("ca.key is still a symlink after generate-ca; the target's mode is not this process's to guarantee")
	}
	if got := info.Mode().Perm(); got != 0600 {
		t.Errorf("ca.key mode = %o, want 600", got)
	}
	keyContent, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatalf("read ca.key: %v", err)
	}
	if !strings.Contains(string(keyContent), "PRIVATE KEY") {
		t.Errorf("ca.key content = %q, want a PEM private key block", keyContent)
	}

	targetContent, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read target: %v", err)
	}
	if string(targetContent) != string(sentinel) {
		t.Errorf("outside/leaked.key content changed: got %q, want the untouched sentinel %q", targetContent, sentinel)
	}
	targetInfo, err := os.Stat(target)
	if err != nil {
		t.Fatalf("stat target: %v", err)
	}
	if got := targetInfo.Mode().Perm(); got != 0644 {
		t.Errorf("outside/leaked.key mode changed: got %o, want 644 unchanged", got)
	}
}

// TestRunGenerateDeviceRequiresHWSerial verifies that the generate-device
// CLI rejects an invocation without -hw-serial. Silently producing a cert
// with no HardwareModuleName SAN is non-compliant under CSIP section 6.2. (#17)
func TestRunGenerateDeviceRequiresHWSerial(t *testing.T) {
	dir := setupTestCertDir(t)

	err := runGenerateDevice([]string{
		"-ca", filepath.Join(dir, "ca.crt"),
		"-ca-key", filepath.Join(dir, "ca.key"),
		"-out", dir,
		// no -hw-serial intentionally
	})
	if err == nil {
		t.Fatal("runGenerateDevice without -hw-serial: want error, got nil")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "hw-serial") &&
		!strings.Contains(strings.ToLower(err.Error()), "serial") {
		t.Errorf("error message should reference hw-serial; got: %v", err)
	}
}

// TestRunGenerateDeviceHWTypeFlag verifies that the new -hw-type flag is
// honored: the resulting cert SAN must encode the provided manufacturer
// PEN OID, not a hardcoded placeholder. (#17)
func TestRunGenerateDeviceHWTypeFlag(t *testing.T) {
	dir := setupTestCertDir(t)

	wantPEN := "1.3.6.1.4.1.55555.42"

	err := runGenerateDevice([]string{
		"-ca", filepath.Join(dir, "ca.crt"),
		"-ca-key", filepath.Join(dir, "ca.key"),
		"-out", dir,
		"-hw-serial", "CLI-TEST-SN",
		"-hw-type", wantPEN,
		"-name", "device-cli",
	})
	if err != nil {
		t.Fatalf("runGenerateDevice: %v", err)
	}

	certPEM, err := os.ReadFile(filepath.Join(dir, "device-cli.crt"))
	if err != nil {
		t.Fatalf("read generated cert: %v", err)
	}
	cert, err := certs.ParseCertificatePEM(certPEM)
	if err != nil {
		t.Fatalf("parse cert: %v", err)
	}

	hmn, ok := certs.ExtractHardwareModuleName(cert)
	if !ok {
		t.Fatal("generated cert is missing HardwareModuleName SAN")
	}
	if hmn.HWType.String() != wantPEN {
		t.Errorf("HWType OID = %v, want %s", hmn.HWType, wantPEN)
	}
	if string(hmn.HWSerialNum) != "CLI-TEST-SN" {
		t.Errorf("HWSerialNum = %q, want %q", string(hmn.HWSerialNum), "CLI-TEST-SN")
	}
}

// TestRunGenerateDeviceNameValidation pins the strict allowlist on the
// -name flag. The flag is joined into a filesystem path via filepath.Join
// (which normalizes but does NOT reject `..`), so unsanitized input would
// permit path traversal - defense-in-depth for any caller that bypasses
// scripts/new-device.sh and invokes the binary directly.
func TestRunGenerateDeviceNameValidation(t *testing.T) {
	dir := setupTestCertDir(t)

	tests := []struct {
		name      string
		nameValue string
		wantErr   bool
	}{
		{"plain ascii accepted", "device", false},
		{"alnum + hyphen accepted", "inverter-2", false},
		{"alnum + underscore accepted", "test_unit_42", false},
		{"alnum + dot accepted", "site.alpha.gw", false},
		{"max length accepted", "a234567890123456789012345678901234567890123456789012345678901234", false},
		{"path traversal rejected", "../../tmp/owned", true},
		{"forward slash rejected", "tmp/owned", true},
		{"backslash rejected", `tmp\owned`, true},
		{"shell semicolon rejected", "foo;rm", true},
		{"shell pipe rejected", "foo|rm", true},
		{"shell ampersand rejected", "foo&rm", true},
		{"shell dollar rejected", "foo$BAR", true},
		{"backtick rejected", "foo`id`", true},
		{"single quote rejected", "foo'bar", true},
		{"double quote rejected", `foo"bar`, true},
		{"space rejected", "foo bar", true},
		{"newline rejected", "foo\nbar", true},
		{"empty rejected", "", true},
		{"leading dot rejected", ".hidden", true},
		{"leading hyphen rejected", "-flag", true},
		{"too long rejected", "a23456789012345678901234567890123456789012345678901234567890123456789", true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := runGenerateDevice([]string{
				"-ca", filepath.Join(dir, "ca.crt"),
				"-ca-key", filepath.Join(dir, "ca.key"),
				"-out", dir,
				"-hw-serial", "CLI-TEST-SN",
				"-hw-type", "1.3.6.1.4.1.55555.42",
				"-name", tc.nameValue,
			})
			if tc.wantErr {
				if err == nil {
					t.Fatalf("runGenerateDevice with -name=%q: want error, got nil", tc.nameValue)
				}
				if !strings.Contains(err.Error(), "-name must match") {
					t.Errorf("error did not name the rule: %v", err)
				}
			} else {
				if err != nil {
					t.Fatalf("runGenerateDevice with -name=%q: unexpected error: %v", tc.nameValue, err)
				}
			}
		})
	}
}

// setupTestCertDir provisions a temp directory pre-populated with a CA
// cert + key, returning the directory path.
func setupTestCertDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()

	caCertPEM, caKeyPEM, err := certs.GenerateCA(certs.CAOptions{
		Organization: "Test",
		CommonName:   "Test CA",
		ValidYears:   10,
	})
	if err != nil {
		t.Fatalf("GenerateCA: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "ca.crt"), caCertPEM, 0o644); err != nil {
		t.Fatalf("write ca.crt: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "ca.key"), caKeyPEM, 0o600); err != nil {
		t.Fatalf("write ca.key: %v", err)
	}
	return dir
}
