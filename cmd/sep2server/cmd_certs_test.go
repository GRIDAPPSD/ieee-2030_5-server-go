package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/certs"
)

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
