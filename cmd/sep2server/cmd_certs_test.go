package main

import (
	"crypto/x509/pkix"
	"encoding/asn1"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-go/internal/certs"
)

// TestRunGenerateDeviceRequiresHWSerial verifies that the generate-device
// CLI rejects an invocation without -hw-serial. Silently producing a cert
// with no HardwareModuleName SAN is non-compliant under CSIP §6.2. (IEEE-017)
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
// PEN OID, not a hardcoded placeholder. (IEEE-017)
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

	gotHWType, gotSerial, ok := extractCLIHardwareModuleName(t, cert.Extensions)
	if !ok {
		t.Fatal("generated cert is missing HardwareModuleName SAN")
	}
	if gotHWType.String() != wantPEN {
		t.Errorf("HWType OID = %v, want %s", gotHWType, wantPEN)
	}
	if gotSerial != "CLI-TEST-SN" {
		t.Errorf("HWSerialNum = %q, want %q", gotSerial, "CLI-TEST-SN")
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

// extractCLIHardwareModuleName is a local copy of the helper used in the
// certs package tests — duplicated here because Go test packages are
// compiled separately and the helper is unexported.
func extractCLIHardwareModuleName(t *testing.T, exts []pkix.Extension) (asn1.ObjectIdentifier, string, bool) {
	t.Helper()

	var sanExt *pkix.Extension
	for i, ext := range exts {
		if ext.Id.Equal(certs.OIDSubjectAltName) {
			sanExt = &exts[i]
			break
		}
	}
	if sanExt == nil {
		return nil, "", false
	}

	var seq asn1.RawValue
	if _, err := asn1.Unmarshal(sanExt.Value, &seq); err != nil {
		t.Fatalf("unmarshal SAN sequence: %v", err)
	}

	rest := seq.Bytes
	for len(rest) > 0 {
		var gn asn1.RawValue
		var err error
		rest, err = asn1.Unmarshal(rest, &gn)
		if err != nil {
			t.Fatalf("unmarshal GeneralName: %v", err)
		}
		if gn.Class != asn1.ClassContextSpecific || gn.Tag != 0 {
			continue
		}
		var on struct {
			TypeID asn1.ObjectIdentifier
			Value  asn1.RawValue
		}
		if _, err := asn1.UnmarshalWithParams(gn.FullBytes, &on, "tag:0"); err != nil {
			t.Fatalf("unmarshal otherName: %v", err)
		}
		if !on.TypeID.Equal(certs.OIDHardwareModuleName) {
			continue
		}
		var hmn struct {
			HWType      asn1.ObjectIdentifier
			HWSerialNum asn1.RawValue
		}
		if _, err := asn1.Unmarshal(on.Value.Bytes, &hmn); err != nil {
			t.Fatalf("unmarshal HardwareModuleName: %v", err)
		}
		return hmn.HWType, string(hmn.HWSerialNum.Bytes), true
	}
	return nil, "", false
}

