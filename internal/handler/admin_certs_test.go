package handler_test

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	sepTLS "github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2tls"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/certs"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/handler"
)

// TestHandleCreateServerCertInvalidJSONDoesNotLeakDecoderDetail pins the 400
// path convention (#360) for the admin plane's JSON envelope: a fixed body,
// decoder detail (which can quote attacker-supplied content, e.g. an
// oversized numeric literal echoed verbatim in a json.UnmarshalTypeError)
// left to the operator-facing log.
func TestHandleCreateServerCertInvalidJSONDoesNotLeakDecoderDetail(t *testing.T) {
	const marker = "13370360913370360913370360"

	var buf bytes.Buffer
	prevOut, prevFlags := log.Writer(), log.Flags()
	log.SetFlags(0)
	log.SetOutput(&buf)
	t.Cleanup(func() {
		log.SetOutput(prevOut)
		log.SetFlags(prevFlags)
	})

	svc := newTestCertService(t)
	h := svc.HandleCreateServerCert()

	body := `{"hosts":["localhost"],"validYears":` + marker + `}`
	req := httptest.NewRequest(http.MethodPost, "/api/certs/server", bytes.NewBufferString(body))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
	if got := w.Body.String(); strings.Contains(got, marker) {
		t.Errorf("body = %q; the decoder detail reached the client", got)
	}
	if logged := buf.String(); !strings.Contains(logged, marker) {
		t.Errorf("log = %q; the decoder detail did not reach the operator", logged)
	}
}

// TestHandleCreateDeviceCertInvalidJSONDoesNotLeakDecoderDetail pins the 400
// path convention (#360) for HandleCreateDeviceCert's JSON decode site: an
// oversized numeric literal is echoed verbatim in a json.UnmarshalTypeError,
// which is attacker-supplied content the client-visible body must not carry.
func TestHandleCreateDeviceCertInvalidJSONDoesNotLeakDecoderDetail(t *testing.T) {
	const marker = "13370360913370360913370360913370360913370360"

	var buf bytes.Buffer
	prevOut, prevFlags := log.Writer(), log.Flags()
	log.SetFlags(0)
	log.SetOutput(&buf)
	t.Cleanup(func() {
		log.SetOutput(prevOut)
		log.SetFlags(prevFlags)
	})

	svc := newTestCertService(t)
	h := svc.HandleCreateDeviceCert()

	body := `{"deviceType":` + marker + `}`
	req := httptest.NewRequest(http.MethodPost, "/api/certs/device", bytes.NewBufferString(body))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
	if got := w.Body.String(); strings.Contains(got, marker) {
		t.Errorf("body = %q; the decoder detail reached the client", got)
	}
	if logged := buf.String(); !strings.Contains(logged, marker) {
		t.Errorf("log = %q; the decoder detail did not reach the operator", logged)
	}
}

// TestHandleCreateDeviceCertInvalidHWTypeDoesNotLeakDecoderDetail pins the 400
// path convention (#360) for HandleCreateDeviceCert's hwType OID parse site,
// which certs.ParseOID's error quotes verbatim.
func TestHandleCreateDeviceCertInvalidHWTypeDoesNotLeakDecoderDetail(t *testing.T) {
	const marker = "MARKERXYZ360LEAK"

	var buf bytes.Buffer
	prevOut, prevFlags := log.Writer(), log.Flags()
	log.SetFlags(0)
	log.SetOutput(&buf)
	t.Cleanup(func() {
		log.SetOutput(prevOut)
		log.SetFlags(prevFlags)
	})

	svc := newTestCertService(t)
	h := svc.HandleCreateDeviceCert()

	body := `{"hwSerialNum":"HW1","hwType":"` + marker + `"}`
	req := httptest.NewRequest(http.MethodPost, "/api/certs/device", bytes.NewBufferString(body))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400, body: %s", w.Code, w.Body.String())
	}
	if got := strings.TrimSpace(w.Body.String()); got != `{"error":"invalid hwType"}` {
		t.Errorf("body = %q, want the fixed message envelope", got)
	}
	if got := w.Body.String(); strings.Contains(got, marker) {
		t.Errorf("body = %q; the decoder detail reached the client", got)
	}
	if logged := buf.String(); !strings.Contains(logged, marker) {
		t.Errorf("log = %q; the decoder detail did not reach the operator", logged)
	}
}

func TestHandleGetCA(t *testing.T) {
	svc := newTestCertService(t)
	h := svc.HandleGetCA()

	req := httptest.NewRequest(http.MethodGet, "/api/certs/ca", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}

	var resp struct {
		CertPEM string `json:"certPEM"`
	}
	_ = json.NewDecoder(w.Body).Decode(&resp)
	if resp.CertPEM == "" {
		t.Error("certPEM should not be empty")
	}
	// Verify it doesn't contain a private key
	if bytes.Contains([]byte(resp.CertPEM), []byte("PRIVATE KEY")) {
		t.Error("CA response must NOT contain private key")
	}
}

func TestHandleCreateServerCert(t *testing.T) {
	svc := newTestCertService(t)
	h := svc.HandleCreateServerCert()

	body := `{"hosts":["localhost","127.0.0.1"],"commonName":"Test","validYears":1}`
	req := httptest.NewRequest(http.MethodPost, "/api/certs/server", bytes.NewBufferString(body))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201, body: %s", w.Code, w.Body.String())
	}

	var resp struct {
		CertPEM string `json:"certPEM"`
		KeyPEM  string `json:"keyPEM"`
	}
	_ = json.NewDecoder(w.Body).Decode(&resp)
	if resp.CertPEM == "" || resp.KeyPEM == "" {
		t.Error("response should contain certPEM and keyPEM")
	}
}

func TestHandleCreateServerCertMissingHosts(t *testing.T) {
	svc := newTestCertService(t)
	h := svc.HandleCreateServerCert()

	body := `{"commonName":"Test"}`
	req := httptest.NewRequest(http.MethodPost, "/api/certs/server", bytes.NewBufferString(body))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func TestHandleCreateDeviceCert(t *testing.T) {
	svc := newTestCertService(t)
	h := svc.HandleCreateDeviceCert()

	body := `{"deviceType":1,"hwSerialNum":"INV-001","hwType":"1.3.6.1.4.1.40732.99"}`
	req := httptest.NewRequest(http.MethodPost, "/api/certs/device", bytes.NewBufferString(body))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201, body: %s", w.Code, w.Body.String())
	}

	var resp struct {
		CertPEM string `json:"certPEM"`
		KeyPEM  string `json:"keyPEM"`
		SFDI    string `json:"sfdi"`
		LFDI    string `json:"lfdi"`
	}
	_ = json.NewDecoder(w.Body).Decode(&resp)

	if resp.CertPEM == "" || resp.KeyPEM == "" {
		t.Error("should contain certPEM and keyPEM")
	}
	if len(resp.SFDI) != 12 {
		t.Errorf("SFDI length = %d, want 12", len(resp.SFDI))
	}
	if !sepTLS.ValidateSFDI(resp.SFDI) {
		t.Errorf("SFDI %q has invalid checksum", resp.SFDI)
	}
	if len(resp.LFDI) != 40 {
		t.Errorf("LFDI length = %d, want 40", len(resp.LFDI))
	}
}

func TestHandleCreateDeviceCertInvalidJSON(t *testing.T) {
	svc := newTestCertService(t)
	h := svc.HandleCreateDeviceCert()

	req := httptest.NewRequest(http.MethodPost, "/api/certs/device", bytes.NewBufferString("{bad"))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func newTestCertService(t *testing.T) *handler.AdminCertService {
	t.Helper()
	caCertPEM, caKeyPEM, err := certs.GenerateCA(certs.CAOptions{
		CommonName: "Test CA",
		ValidYears: 1,
	})
	if err != nil {
		t.Fatal(err)
	}

	block, _ := pem.Decode(caCertPEM)
	caCert, _ := x509.ParseCertificate(block.Bytes)
	keyBlock, _ := pem.Decode(caKeyPEM)
	raw, _ := x509.ParsePKCS8PrivateKey(keyBlock.Bytes)

	return handler.NewAdminCertService(caCert, raw.(*ecdsa.PrivateKey), caCertPEM)
}
