package handler_test

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"testing"

	sepTLS "github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2tls"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/certs"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/handler"
)

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
