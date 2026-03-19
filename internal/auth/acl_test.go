package auth_test

import (
	"crypto/tls"
	"crypto/x509"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/craig8/ieee-2030_5-go/internal/auth"
)

func TestACLAllowedMethod(t *testing.T) {
	rules := []auth.ACLRule{
		{"/edev", auth.MethodGet | auth.MethodHead | auth.MethodPost, auth.AuthDeviceCert, false},
	}

	middleware := auth.ACLMiddleware(rules)(okHandler())
	req := httptest.NewRequest(http.MethodGet, "/edev", nil)
	req.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{{}}}

	w := httptest.NewRecorder()
	middleware.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Errorf("allowed GET should pass, got %d", w.Code)
	}
}

func TestACLDisallowedMethod(t *testing.T) {
	rules := []auth.ACLRule{
		{"/edev", auth.MethodGet | auth.MethodHead, auth.AuthDeviceCert, false},
	}

	middleware := auth.ACLMiddleware(rules)(okHandler())
	req := httptest.NewRequest(http.MethodDelete, "/edev", nil)
	req.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{{}}}

	w := httptest.NewRecorder()
	middleware.ServeHTTP(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("DELETE should be blocked, got %d", w.Code)
	}
}

func TestACLRequiresAuth(t *testing.T) {
	rules := []auth.ACLRule{
		{"/edev", auth.MethodGet, auth.AuthDeviceCert, false},
	}

	middleware := auth.ACLMiddleware(rules)(okHandler())
	// No TLS = AuthNone
	req := httptest.NewRequest(http.MethodGet, "/edev", nil)

	w := httptest.NewRecorder()
	middleware.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Errorf("no cert should be forbidden, got %d", w.Code)
	}
}

func TestACLAllowsNoAuth(t *testing.T) {
	rules := []auth.ACLRule{
		{"/dcap", auth.MethodGet | auth.MethodHead, auth.AuthNone | auth.AuthDeviceCert, false},
	}

	middleware := auth.ACLMiddleware(rules)(okHandler())
	req := httptest.NewRequest(http.MethodGet, "/dcap", nil)

	w := httptest.NewRecorder()
	middleware.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Errorf("/dcap should allow no-auth GET, got %d", w.Code)
	}
}

func TestACLLongestPrefixMatch(t *testing.T) {
	rules := []auth.ACLRule{
		{"/edev", auth.MethodGet | auth.MethodPost, auth.AuthDeviceCert, false},
		{"/edev/1", auth.MethodGet | auth.MethodPut, auth.AuthDeviceCert, true},
	}

	middleware := auth.ACLMiddleware(rules)(okHandler())

	// POST /edev/1 should match /edev/1 rule (no POST allowed)
	req := httptest.NewRequest(http.MethodPost, "/edev/1", nil)
	req.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{{}}}

	w := httptest.NewRecorder()
	middleware.ServeHTTP(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST /edev/1 should match specific rule and be blocked, got %d", w.Code)
	}
}

func TestACLNoMatchingRule(t *testing.T) {
	rules := []auth.ACLRule{
		{"/edev", auth.MethodGet, auth.AuthDeviceCert, false},
	}

	middleware := auth.ACLMiddleware(rules)(okHandler())
	req := httptest.NewRequest(http.MethodGet, "/unknown", nil)

	w := httptest.NewRecorder()
	middleware.ServeHTTP(w, req)

	// No matching rule — pass through to handler (router will 404)
	if w.Code != 200 {
		t.Errorf("no matching rule should pass through, got %d", w.Code)
	}
}

func TestHTTPMethodToBitmap(t *testing.T) {
	tests := []struct {
		method string
		want   uint8
	}{
		{"GET", auth.MethodGet},
		{"PUT", auth.MethodPut},
		{"POST", auth.MethodPost},
		{"DELETE", auth.MethodDelete},
		{"HEAD", auth.MethodHead},
		{"PATCH", 0},
	}

	for _, tt := range tests {
		got := auth.HTTPMethodToBitmap(tt.method)
		if got != tt.want {
			t.Errorf("HTTPMethodToBitmap(%q) = %d, want %d", tt.method, got, tt.want)
		}
	}
}
