package tls_test

import (
	"crypto/ecdsa"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"io"
	"net"
	"net/http"
	"testing"

	"github.com/craig8/ieee-2030_5-go/internal/certs"
	sepTLS "github.com/craig8/ieee-2030_5-go/internal/tls"
)

func TestMutualTLSHandshake(t *testing.T) {
	// Generate CA, server cert, and device cert
	caCertPEM, caKeyPEM, err := certs.GenerateCA(certs.CAOptions{
		CommonName: "Test CA",
		ValidYears: 1,
	})
	if err != nil {
		t.Fatal(err)
	}

	caCert, caKey := parseCACert(t, caCertPEM, caKeyPEM)

	serverCertPEM, serverKeyPEM, err := certs.GenerateServerCert(caCert, caKey, certs.ServerCertOptions{
		Hosts:      []string{"127.0.0.1", "localhost"},
		CommonName: "Test Server",
		ValidYears: 1,
	})
	if err != nil {
		t.Fatal(err)
	}

	deviceCertPEM, deviceKeyPEM, err := certs.GenerateDeviceCert(caCert, caKey, certs.DeviceCertOptions{
		DeviceType:  certs.DeviceTypeGeneric,
		HWSerialNum: "TEST-001",
	})
	if err != nil {
		t.Fatal(err)
	}

	// Create server TLS config
	serverTLSCfg, err := sepTLS.NewServerTLSConfigFromPEM(serverCertPEM, serverKeyPEM, caCertPEM)
	if err != nil {
		t.Fatal(err)
	}

	// Create client TLS config
	clientTLSCfg, err := sepTLS.NewClientTLSConfigFromPEM(deviceCertPEM, deviceKeyPEM, caCertPEM)
	if err != nil {
		t.Fatal(err)
	}

	// Start TLS server
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()

	tlsListener := tls.NewListener(listener, serverTLSCfg)

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify we received the client certificate
		if r.TLS == nil || len(r.TLS.PeerCertificates) == 0 {
			http.Error(w, "no client cert", http.StatusForbidden)
			return
		}

		cert := r.TLS.PeerCertificates[0]
		sfdi := sepTLS.SFDI(cert)
		lfdi := sepTLS.LFDI(cert)

		_, _ = fmt.Fprintf(w, "SFDI=%s LFDI=%s", sfdi, lfdi)
	})

	srv := &http.Server{Handler: handler}
	go func() { _ = srv.Serve(tlsListener) }()
	defer func() { _ = srv.Close() }()

	// Make client request
	client := &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: clientTLSCfg,
		},
	}

	addr := listener.Addr().String()
	resp, err := client.Get("https://" + addr + "/test")
	if err != nil {
		t.Fatalf("client GET: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}

	body, _ := io.ReadAll(resp.Body)
	bodyStr := string(body)

	if len(bodyStr) == 0 {
		t.Fatal("empty response body")
	}

	// Verify SFDI and LFDI are present
	if !containsSubstring(bodyStr, "SFDI=") || !containsSubstring(bodyStr, "LFDI=") {
		t.Errorf("response should contain SFDI and LFDI, got: %s", bodyStr)
	}

	t.Logf("mutual TLS response: %s", bodyStr)

	// Verify negotiated cipher suite is GCM (our fallback)
	if resp.TLS.CipherSuite != tls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256 {
		t.Errorf("cipher suite = 0x%04x, want GCM 0x%04x",
			resp.TLS.CipherSuite, tls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256)
	}
}

func TestTLSRejectsNoClientCert(t *testing.T) {
	caCertPEM, caKeyPEM, _ := certs.GenerateCA(certs.CAOptions{
		CommonName: "Test CA",
		ValidYears: 1,
	})

	caCert, caKey := parseCACert(t, caCertPEM, caKeyPEM)

	serverCertPEM, serverKeyPEM, _ := certs.GenerateServerCert(caCert, caKey, certs.ServerCertOptions{
		Hosts: []string{"127.0.0.1"},
	})

	serverTLSCfg, _ := sepTLS.NewServerTLSConfigFromPEM(serverCertPEM, serverKeyPEM, caCertPEM)

	listener, _ := net.Listen("tcp", "127.0.0.1:0")
	defer func() { _ = listener.Close() }()

	tlsListener := tls.NewListener(listener, serverTLSCfg)
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
	})}
	go func() { _ = srv.Serve(tlsListener) }()
	defer func() { _ = srv.Close() }()

	// Client WITHOUT a certificate
	client := &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				InsecureSkipVerify: true,
			},
		},
	}

	addr := listener.Addr().String()
	_, err := client.Get("https://" + addr + "/test")
	if err == nil {
		t.Error("expected TLS handshake to fail without client cert")
	}
}

func containsSubstring(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(s) > 0 && containsAt(s, sub))
}

func containsAt(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func parseCACert(t *testing.T, certPEM, keyPEM []byte) (*x509.Certificate, *ecdsa.PrivateKey) {
	t.Helper()
	block, _ := pem.Decode(certPEM)
	if block == nil {
		t.Fatal("failed to decode cert PEM")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("ParseCertificate: %v", err)
	}

	keyBlock, _ := pem.Decode(keyPEM)
	if keyBlock == nil {
		t.Fatal("failed to decode key PEM")
	}
	keyRaw, err := x509.ParsePKCS8PrivateKey(keyBlock.Bytes)
	if err != nil {
		t.Fatalf("ParsePKCS8PrivateKey: %v", err)
	}
	ecKey, ok := keyRaw.(*ecdsa.PrivateKey)
	if !ok {
		t.Fatal("CA key is not ECDSA")
	}
	return cert, ecKey
}
