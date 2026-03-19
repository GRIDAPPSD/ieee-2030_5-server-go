package auth_test

import (
	"crypto/ecdsa"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/craig8/ieee-2030_5-go/internal/auth"
	"github.com/craig8/ieee-2030_5-go/internal/certs"
)

func TestAdminAuthMTLSWithAdminCert(t *testing.T) {
	adminCert := generateAdminCert(t)

	handler := auth.AdminAuthMiddleware("test-key")(okHandler())
	req := httptest.NewRequest(http.MethodGet, "/api/certs/ca", nil)
	req.TLS = &tls.ConnectionState{
		PeerCertificates: []*x509.Certificate{adminCert},
	}

	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("admin cert should be authorized, got %d", w.Code)
	}
}

func TestAdminAuthMTLSWithDeviceCert(t *testing.T) {
	deviceCert := generateDeviceCert(t)

	handler := auth.AdminAuthMiddleware("test-key")(okHandler())
	req := httptest.NewRequest(http.MethodGet, "/api/certs/ca", nil)
	req.TLS = &tls.ConnectionState{
		PeerCertificates: []*x509.Certificate{deviceCert},
	}

	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("device cert should be rejected, got %d", w.Code)
	}
}

func TestAdminAuthBearerCorrectKey(t *testing.T) {
	handler := auth.AdminAuthMiddleware("my-secret-key")(okHandler())
	req := httptest.NewRequest(http.MethodGet, "/api/certs/ca", nil)
	req.Header.Set("Authorization", "Bearer my-secret-key")

	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("correct Bearer should be authorized, got %d", w.Code)
	}
}

func TestAdminAuthBearerWrongKey(t *testing.T) {
	handler := auth.AdminAuthMiddleware("my-secret-key")(okHandler())
	req := httptest.NewRequest(http.MethodGet, "/api/certs/ca", nil)
	req.Header.Set("Authorization", "Bearer wrong-key")

	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("wrong Bearer should be rejected, got %d", w.Code)
	}
}

func TestAdminAuthBearerDisabledWhenEmpty(t *testing.T) {
	handler := auth.AdminAuthMiddleware("")(okHandler())
	req := httptest.NewRequest(http.MethodGet, "/api/certs/ca", nil)
	req.Header.Set("Authorization", "Bearer anything")

	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	// No admin key configured and no mTLS cert — should reject
	if w.Code != http.StatusUnauthorized {
		t.Errorf("empty admin key should disable Bearer auth, got %d", w.Code)
	}
}

func TestAdminAuthNoCredentials(t *testing.T) {
	handler := auth.AdminAuthMiddleware("test-key")(okHandler())
	req := httptest.NewRequest(http.MethodGet, "/api/certs/ca", nil)

	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("no credentials should be rejected, got %d", w.Code)
	}
}

func TestAdminAuthBearerMalformedHeader(t *testing.T) {
	handler := auth.AdminAuthMiddleware("test-key")(okHandler())
	req := httptest.NewRequest(http.MethodGet, "/api/certs/ca", nil)
	req.Header.Set("Authorization", "Basic dXNlcjpwYXNz")

	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("Basic auth header should be rejected, got %d", w.Code)
	}
}

// helpers

func okHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
}

func generateAdminCert(t *testing.T) *x509.Certificate {
	t.Helper()
	caCert, caKey := mustGenCA(t)
	certPEM, _, err := certs.GenerateAdminCert(caCert, caKey, certs.AdminCertOptions{
		CommonName: "Test Admin",
		ValidYears: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	return mustParseCert(t, certPEM)
}

func generateDeviceCert(t *testing.T) *x509.Certificate {
	t.Helper()
	caCert, caKey := mustGenCA(t)
	certPEM, _, err := certs.GenerateDeviceCert(caCert, caKey, certs.DeviceCertOptions{
		DeviceType:  certs.DeviceTypeGeneric,
		HWSerialNum: "TEST",
	})
	if err != nil {
		t.Fatal(err)
	}
	return mustParseCert(t, certPEM)
}

func mustGenCA(t *testing.T) (*x509.Certificate, *ecdsa.PrivateKey) {
	t.Helper()
	certPEM, keyPEM, err := certs.GenerateCA(certs.CAOptions{
		CommonName: "Test CA",
		ValidYears: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	cert := mustParseCert(t, certPEM)
	block, _ := pem.Decode(keyPEM)
	raw, _ := x509.ParsePKCS8PrivateKey(block.Bytes)
	return cert, raw.(*ecdsa.PrivateKey)
}

func mustParseCert(t *testing.T, data []byte) *x509.Certificate {
	t.Helper()
	block, _ := pem.Decode(data)
	if block == nil {
		t.Fatal("no PEM data")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	return cert
}
