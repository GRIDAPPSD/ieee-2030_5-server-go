package server_test

import (
	"crypto/ecdsa"
	"crypto/tls"
	"crypto/x509"
	"encoding/xml"
	"io"
	"math"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/craig8/ieee-2030_5-go/internal/certs"
	"github.com/craig8/ieee-2030_5-go/internal/config"
	"github.com/craig8/ieee-2030_5-go/internal/server"
	sepTLS "github.com/craig8/ieee-2030_5-go/internal/tls"
	"github.com/craig8/ieee-2030_5-go/pkg/sep2"
)

func TestIntegrationEndToEnd(t *testing.T) {
	// Generate all certs
	caCertPEM, caKeyPEM, err := certs.GenerateCA(certs.CAOptions{
		CommonName: "Integration Test CA",
		ValidYears: 1,
	})
	if err != nil {
		t.Fatal(err)
	}

	caCert, caKey, err := parsePEMPair(caCertPEM, caKeyPEM)
	if err != nil {
		t.Fatal(err)
	}

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
		HWSerialNum: "INTEG-TEST-001",
	})
	if err != nil {
		t.Fatal(err)
	}

	// Start TLS server
	serverTLSCfg, err := sepTLS.NewServerTLSConfigFromPEM(serverCertPEM, serverKeyPEM, caCertPEM)
	if err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{
		TZOffset:    -28800,
		DSTOffset:   3600,
		DSTStart:    1583661600,
		DSTEnd:      1604214000,
		TimeQuality: sep2.TimeQualityNTP,
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

	tlsListener := tls.NewListener(listener, serverTLSCfg)
	router := server.NewRouter(cfg, nil)
	srv := &http.Server{Handler: router}
	go srv.Serve(tlsListener)
	defer srv.Close()

	// Create client
	clientTLSCfg, err := sepTLS.NewClientTLSConfigFromPEM(deviceCertPEM, deviceKeyPEM, caCertPEM)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{
		Transport: &http.Transport{TLSClientConfig: clientTLSCfg},
	}
	baseURL := "https://" + listener.Addr().String()

	// Test 1: GET /dcap
	t.Run("GET /dcap", func(t *testing.T) {
		resp, err := client.Get(baseURL + "/dcap")
		if err != nil {
			t.Fatalf("GET /dcap: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != 200 {
			t.Fatalf("status = %d, want 200", resp.StatusCode)
		}

		ct := resp.Header.Get("Content-Type")
		if ct != "application/sep+xml" {
			t.Errorf("Content-Type = %q, want application/sep+xml", ct)
		}

		body, _ := io.ReadAll(resp.Body)
		var dcap sep2.DeviceCapability
		if err := xml.Unmarshal(body, &dcap); err != nil {
			t.Fatalf("Unmarshal: %v", err)
		}

		if dcap.Href != "/dcap" {
			t.Errorf("dcap.Href = %q, want /dcap", dcap.Href)
		}
		if dcap.TimeLink == nil || dcap.TimeLink.Href != "/tm" {
			t.Error("dcap should have TimeLink pointing to /tm")
		}
		if dcap.PollRate != 900 {
			t.Errorf("PollRate = %d, want 900", dcap.PollRate)
		}
	})

	// Test 2: Follow TimeLink to GET /tm
	t.Run("GET /tm", func(t *testing.T) {
		resp, err := client.Get(baseURL + "/tm")
		if err != nil {
			t.Fatalf("GET /tm: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != 200 {
			t.Fatalf("status = %d, want 200", resp.StatusCode)
		}

		body, _ := io.ReadAll(resp.Body)
		var tm sep2.Time
		if err := xml.Unmarshal(body, &tm); err != nil {
			t.Fatalf("Unmarshal: %v", err)
		}

		now := time.Now().Unix()
		diff := math.Abs(float64(tm.CurrentTime - now))
		if diff > 2 {
			t.Errorf("CurrentTime %d more than 2s from now %d", tm.CurrentTime, now)
		}

		if tm.TzOffset != -28800 {
			t.Errorf("TzOffset = %d, want -28800", tm.TzOffset)
		}
		if tm.Quality != sep2.TimeQualityNTP {
			t.Errorf("Quality = %d, want %d", tm.Quality, sep2.TimeQualityNTP)
		}
	})

	// Test 3: Verify TLS cipher suite
	t.Run("TLS cipher suite", func(t *testing.T) {
		resp, err := client.Get(baseURL + "/dcap")
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()

		if resp.TLS.CipherSuite != tls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256 {
			t.Errorf("cipher = 0x%04x, want ECDHE_ECDSA_AES128_GCM", resp.TLS.CipherSuite)
		}
	})

	// Test 4: POST to /dcap should fail
	t.Run("POST /dcap rejected", func(t *testing.T) {
		resp, err := client.Post(baseURL+"/dcap", "application/sep+xml", nil)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()

		if resp.StatusCode != http.StatusMethodNotAllowed {
			t.Errorf("status = %d, want 405", resp.StatusCode)
		}
	})
}

func parsePEMPair(certPEM, keyPEM []byte) (*x509.Certificate, *ecdsa.PrivateKey, error) {
	cert, err := certs.ParseCertificatePEM(certPEM)
	if err != nil {
		return nil, nil, err
	}
	key, err := certs.ParseKeyPEM(keyPEM)
	if err != nil {
		return nil, nil, err
	}
	return cert, key, nil
}
