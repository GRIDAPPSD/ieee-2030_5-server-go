package auth_test

import (
	"bytes"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/auth"
)

// #413: the admin plane's only prior observability logged every benign
// loopback admission and nothing about a remote key guess. These tests pin
// the fix's two failure modes directly, not just the feature: slog.SetDefault
// does not go through the stdlib log package, so log.SetOutput captures
// nothing from it and a key-not-logged assertion against an empty buffer
// would pass for the wrong reason. slog.SetDefault is also process-global, so
// none of these tests call t.Parallel().

// captureSlog installs a JSON slog handler over a buffer as the process
// default and restores the previous default on cleanup. Never pair with
// t.Parallel(): the default is process-wide, and a concurrent test hitting
// the same admission paths would interleave writes into this buffer.
func captureSlog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return &buf
}

// logLines parses the captured buffer as one JSON object per line, matching
// slog.NewJSONHandler's one-record-per-line output.
func logLines(t *testing.T, buf *bytes.Buffer) []map[string]any {
	t.Helper()
	var lines []map[string]any
	for _, raw := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if raw == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(raw), &m); err != nil {
			t.Fatalf("captured log line is not valid JSON: %v\nline: %s", err, raw)
		}
		lines = append(lines, m)
	}
	return lines
}

// assertNoCredentialLeak fails the test if credential appears in raw
// verbatim, as a sha256 hash, or as a bare field value equal to its length.
// Length and hash are checked explicitly because they are the two that get
// argued for as harmless: a length narrows a guessing space, and a hash of a
// low-entropy shared secret is trivially reversible (#413).
func assertNoCredentialLeak(t *testing.T, raw string, credential string) {
	t.Helper()
	if strings.Contains(raw, credential) {
		t.Errorf("captured log contains the credential verbatim: %s", raw)
	}
	sum := sha256.Sum256([]byte(credential))
	if hexSum := hex.EncodeToString(sum[:]); strings.Contains(raw, hexSum) {
		t.Errorf("captured log contains a sha256 hash of the credential: %s", raw)
	}
	lengthToken := strconv.Itoa(len(credential))
	for _, line := range strings.Split(raw, "\n") {
		if line == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			continue
		}
		for k, v := range m {
			switch val := v.(type) {
			case string:
				if val == lengthToken {
					t.Errorf("captured log field %q = %q, which equals the credential's length", k, val)
				}
			case float64:
				if strconv.Itoa(int(val)) == lengthToken {
					t.Errorf("captured log field %q = %v, which equals the credential's length", k, val)
				}
			}
		}
	}
}

const wrongBearerKey = "wrong-key-guess"

// TestBearerWrongKeyLogsFailureWithoutTheKey covers the positive assertion
// (the WARN line is actually captured, with the source address and outcome)
// and the negative one (the wrong key never appears) together: the positive
// half is required, because an empty buffer would pass the negative check
// for free (#413).
func TestBearerWrongKeyLogsFailureWithoutTheKey(t *testing.T) {
	buf := captureSlog(t)

	handler := auth.AdminAuthMiddleware("the-real-key", nil, nil)(okHandler())
	req := httptest.NewRequest(http.MethodGet, "/api/certs/ca", nil)
	req.Header.Set("Authorization", "Bearer "+wrongBearerKey)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("wrong Bearer: status = %d, want 401", w.Code)
	}

	var found bool
	for _, l := range logLines(t, buf) {
		if l["msg"] != "admin: credential presented and rejected" {
			continue
		}
		found = true
		if l["admission_path"] != "bearer" {
			t.Errorf("admission_path = %v, want %q", l["admission_path"], "bearer")
		}
		if l["level"] != "WARN" {
			t.Errorf("level = %v, want WARN", l["level"])
		}
		if addr, _ := l["remote_addr"].(string); addr == "" {
			t.Errorf("remote_addr missing from failure log line: %v", l)
		}
	}
	if !found {
		t.Fatalf("no admin_auth_failure line captured; buffer = %s", buf.String())
	}

	assertNoCredentialLeak(t, buf.String(), wrongBearerKey)
}

// TestBearerAbsentDoesNotLogFailure asserts the SPA's own credential-free
// probe (no Authorization header at all) produces no failure line: only a
// PRESENTED-and-wrong credential logs, or the log fills with benign noise on
// every unauthenticated page load (#413).
func TestBearerAbsentDoesNotLogFailure(t *testing.T) {
	buf := captureSlog(t)

	handler := auth.AdminAuthMiddleware("the-real-key", nil, nil)(okHandler())
	req := httptest.NewRequest(http.MethodGet, "/api/certs/ca", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("no credential: status = %d, want 401", w.Code)
	}
	if buf.Len() != 0 {
		t.Errorf("credential-free request logged something: %s", buf.String())
	}
}

// TestBearerCorrectKeyLogsSuccess closes the asymmetry from the other side: a
// Bearer admission is a credential PRESENTATION event and must be observable,
// same as the loopback-bypass admission already is (#413).
func TestBearerCorrectKeyLogsSuccess(t *testing.T) {
	buf := captureSlog(t)

	handler := auth.AdminAuthMiddleware("the-real-key", nil, nil)(okHandler())
	req := httptest.NewRequest(http.MethodGet, "/api/certs/ca", nil)
	req.Header.Set("Authorization", "Bearer the-real-key")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("correct Bearer: status = %d, want 200", w.Code)
	}
	var found bool
	for _, l := range logLines(t, buf) {
		if l["msg"] == "admin: credential presented and accepted" && l["admission_path"] == "bearer" {
			found = true
		}
	}
	if !found {
		t.Fatalf("no admin_auth_success line for the bearer path; captured = %s", buf.String())
	}
}

// TestMTLSAdminCertLogsSuccess extends the same success observability to the
// mTLS path (#413).
func TestMTLSAdminCertLogsSuccess(t *testing.T) {
	buf := captureSlog(t)
	adminCert := generateAdminCert(t)

	handler := auth.AdminAuthMiddleware("test-key", nil, nil)(okHandler())
	req := httptest.NewRequest(http.MethodGet, "/api/certs/ca", nil)
	req.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{adminCert}}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("admin cert: status = %d, want 200", w.Code)
	}
	var found bool
	for _, l := range logLines(t, buf) {
		if l["msg"] == "admin: credential presented and accepted" && l["admission_path"] == "mtls" {
			found = true
		}
	}
	if !found {
		t.Fatalf("no admin_auth_success line for the mtls path; captured = %s", buf.String())
	}
}

// TestMTLSDeviceCertDoesNotLogFailure pins a deliberate scope boundary: a
// device certificate lacking the admin policy OID is a PKI mismatch, not an
// admin-key guess, and closing that gap is not part of this pass. Logging
// every such refusal at WARN would be a different, unreviewed noise source.
func TestMTLSDeviceCertDoesNotLogFailure(t *testing.T) {
	buf := captureSlog(t)
	deviceCert := generateDeviceCert(t)

	handler := auth.AdminAuthMiddleware("test-key", nil, nil)(okHandler())
	req := httptest.NewRequest(http.MethodGet, "/api/certs/ca", nil)
	req.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{deviceCert}}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("device cert: status = %d, want 401", w.Code)
	}
	if buf.Len() != 0 {
		t.Errorf("device-cert refusal logged something: %s", buf.String())
	}
}
