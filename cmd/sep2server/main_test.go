package main

import (
	"bytes"
	"crypto/x509"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/certs"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/config"
)

// writeTestCAPair generates a CA and writes its cert and key under dir as
// certName/keyName, returning the parsed certificate.
func writeTestCAPair(t *testing.T, dir, cn, certName, keyName string) *x509.Certificate {
	t.Helper()
	certPEM, keyPEM, err := certs.GenerateCA(certs.CAOptions{CommonName: cn, ValidYears: 1})
	if err != nil {
		t.Fatalf("GenerateCA(%s): %v", cn, err)
	}
	if err := os.WriteFile(filepath.Join(dir, certName), certPEM, 0o644); err != nil {
		t.Fatalf("write %s: %v", certName, err)
	}
	if err := os.WriteFile(filepath.Join(dir, keyName), keyPEM, 0o600); err != nil {
		t.Fatalf("write %s: %v", keyName, err)
	}
	block, _ := pem.Decode(certPEM)
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("parse %s: %v", certName, err)
	}
	return cert
}

// unsetCAEnv clears every env var loadAdminCertService reads, so a test
// starts from "nothing set" regardless of what ran before it in the same
// process.
func unsetCAEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{"SEP2_CA_KEY", "SEP2_SERVING_CA_KEY", "SEP2_DEVICE_CA_KEY"} {
		t.Setenv(k, "")
		if err := os.Unsetenv(k); err != nil {
			t.Fatal(err)
		}
	}
}

// TestLoadAdminCertServiceKeyVariablesFallBackToSharedCAKey is #638 fix
// round 2 item 2: SEP2_SERVING_CA_KEY and SEP2_DEVICE_CA_KEY appear only in
// this file and resolve only inside what was runServe, which no test
// called. With neither set, both must fall back to the resolved
// SEP2_CA_KEY, exactly as ServingCAFile/DeviceCAFile fall back to CAFile.
// A fallback that resolved to "" instead of caKeyFile would make LoadCAPair
// fail to read the key and disable minting for that role; this test mints
// through both roles to prove the fallback lands on the real shared key,
// not just that some non-empty path was produced.
func TestLoadAdminCertServiceKeyVariablesFallBackToSharedCAKey(t *testing.T) {
	unsetCAEnv(t)
	dir := t.TempDir()
	writeTestCAPair(t, dir, "638 Item2 Shared CA", "ca.crt", "ca.key")

	cfg := &config.Config{CAFile: filepath.Join(dir, "ca.crt")}
	resolver := &certDirResolver{resolved: true, dir: dir}

	svc, err := loadAdminCertService(cfg, resolver)
	if err != nil {
		t.Fatalf("loadAdminCertService: %v", err)
	}
	if svc == nil {
		t.Fatal("svc is nil; the shared CA certificate did not load")
	}

	serverW := httptest.NewRecorder()
	svc.HandleCreateServerCert().ServeHTTP(serverW, httptest.NewRequest(http.MethodPost, "/api/certs/server", bytes.NewBufferString(`{"hosts":["localhost"]}`)))
	if serverW.Code != http.StatusCreated {
		t.Errorf("POST /api/certs/server: status = %d, want 201 (SEP2_SERVING_CA_KEY should fall back to the resolved SEP2_CA_KEY): %s", serverW.Code, serverW.Body.String())
	}

	deviceW := httptest.NewRecorder()
	svc.HandleCreateDeviceCert().ServeHTTP(deviceW, httptest.NewRequest(http.MethodPost, "/api/certs/device", bytes.NewBufferString(`{"deviceType":1,"hwSerialNum":"ITEM2-001","hwType":"1.3.6.1.4.1.40732.99"}`)))
	if deviceW.Code != http.StatusCreated {
		t.Errorf("POST /api/certs/device: status = %d, want 201 (SEP2_DEVICE_CA_KEY should fall back to the resolved SEP2_CA_KEY): %s", deviceW.Code, deviceW.Body.String())
	}
}

// TestLoadAdminCertServiceServingCertOverrideWithoutKeyDisablesServingMintOnly
// is item 2's second half: "the case where one role's certificate is set
// and its key is not." SEP2_SERVING_CA points at a different CA than the
// shared default while SEP2_SERVING_CA_KEY stays unset, so the resolved
// serving key file is still the SHARED key - which does not match the new
// serving certificate. This proves three things at once: the cert override
// actually reaches LoadCAPair (ServingCA() is the override, not the
// shared CA), the key fallback still lands on the shared key file rather
// than an empty or the override's own (nonexistent) key path, and the
// resulting mismatch disables only the serving mint route, leaving the
// untouched device role (still shared cert+key) minting normally.
func TestLoadAdminCertServiceServingCertOverrideWithoutKeyDisablesServingMintOnly(t *testing.T) {
	unsetCAEnv(t)
	dir := t.TempDir()
	writeTestCAPair(t, dir, "638 Item2 Shared CA", "ca.crt", "ca.key")

	overrideDir := t.TempDir()
	overrideCert := writeTestCAPair(t, overrideDir, "638 Item2 Serving Override CA", "serving.crt", "serving.key")

	cfg := &config.Config{
		CAFile:        filepath.Join(dir, "ca.crt"),
		ServingCAFile: filepath.Join(overrideDir, "serving.crt"),
	}
	resolver := &certDirResolver{resolved: true, dir: dir}

	svc, err := loadAdminCertService(cfg, resolver)
	if err != nil {
		t.Fatalf("loadAdminCertService: %v", err)
	}
	if svc == nil {
		t.Fatal("svc is nil; the override serving CA certificate did not load")
	}
	if got := svc.ServingCA(); !got.Equal(overrideCert) {
		t.Errorf("ServingCA() = %q, want the override CA %q (SEP2_SERVING_CA did not take effect)", got.Subject, overrideCert.Subject)
	}

	serverW := httptest.NewRecorder()
	svc.HandleCreateServerCert().ServeHTTP(serverW, httptest.NewRequest(http.MethodPost, "/api/certs/server", bytes.NewBufferString(`{"hosts":["localhost"]}`)))
	if serverW.Code != http.StatusServiceUnavailable {
		t.Errorf("POST /api/certs/server: status = %d, want 503 (the shared key does not match the override cert): %s", serverW.Code, serverW.Body.String())
	}

	deviceW := httptest.NewRecorder()
	svc.HandleCreateDeviceCert().ServeHTTP(deviceW, httptest.NewRequest(http.MethodPost, "/api/certs/device", bytes.NewBufferString(`{"deviceType":1,"hwSerialNum":"ITEM2-002","hwType":"1.3.6.1.4.1.40732.99"}`)))
	if deviceW.Code != http.StatusCreated {
		t.Errorf("POST /api/certs/device: status = %d, want 201 (the device role is untouched by the serving override): %s", deviceW.Code, deviceW.Body.String())
	}
}
