package server_test

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-go/internal/certs"
	"github.com/GRIDAPPSD/ieee-2030_5-go/internal/config"
	"github.com/GRIDAPPSD/ieee-2030_5-go/internal/handler"
	"github.com/GRIDAPPSD/ieee-2030_5-go/internal/server"
	sepTLS "github.com/GRIDAPPSD/ieee-2030_5-go/internal/tls"
	"github.com/GRIDAPPSD/ieee-2030_5-go/pkg/sep2"
)

func TestAdminIntegrationBearerToken(t *testing.T) {
	env := setupTestEnv(t)

	// Admin HTTPS listener with self-signed cert
	adminCertPEM, adminKeyPEM, err := certs.GenerateSelfSignedTLS([]string{"127.0.0.1"})
	if err != nil {
		t.Fatal(err)
	}
	adminTLSCert, _ := tls.X509KeyPair(adminCertPEM, adminKeyPEM)
	adminTLSCfg := &tls.Config{
		Certificates: []tls.Certificate{adminTLSCert},
		MinVersion:   tls.VersionTLS12,
	}

	adminListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = adminListener.Close() }()

	adminTLSListener := tls.NewListener(adminListener, adminTLSCfg)
	adminRouter := server.NewAdminRouter("test-admin-key", env.svc, nil, "GCM", nil)
	adminSrv := &http.Server{Handler: adminRouter}
	go func() { _ = adminSrv.Serve(adminTLSListener) }()
	defer func() { _ = adminSrv.Close() }()

	// Client trusts the self-signed admin cert
	adminClient := &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		},
	}
	adminURL := "https://" + adminListener.Addr().String()

	// Test: GET /api/certs/ca with correct Bearer
	t.Run("Bearer GET CA", func(t *testing.T) {
		req, _ := http.NewRequest("GET", adminURL+"/api/certs/ca", nil)
		req.Header.Set("Authorization", "Bearer test-admin-key")
		resp, err := adminClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()

		if resp.StatusCode != 200 {
			body, _ := io.ReadAll(resp.Body)
			t.Fatalf("status = %d, body: %s", resp.StatusCode, body)
		}

		var result struct {
			CertPEM string `json:"certPEM"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&result)
		if result.CertPEM == "" {
			t.Error("CA certPEM should not be empty")
		}
	})

	// Test: POST /api/certs/device with correct Bearer
	t.Run("Bearer create device cert", func(t *testing.T) {
		body := `{"deviceType":1,"hwSerialNum":"INV-INTEG-001","hwType":"1.3.6.1.4.1.40732.99"}`
		req, _ := http.NewRequest("POST", adminURL+"/api/certs/device", bytes.NewBufferString(body))
		req.Header.Set("Authorization", "Bearer test-admin-key")
		req.Header.Set("Content-Type", "application/json")
		resp, err := adminClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()

		if resp.StatusCode != 201 {
			respBody, _ := io.ReadAll(resp.Body)
			t.Fatalf("status = %d, body: %s", resp.StatusCode, respBody)
		}

		var result struct {
			CertPEM string `json:"certPEM"`
			KeyPEM  string `json:"keyPEM"`
			SFDI    string `json:"sfdi"`
			LFDI    string `json:"lfdi"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&result)

		if len(result.SFDI) != 12 {
			t.Errorf("SFDI length = %d, want 12", len(result.SFDI))
		}
		if len(result.LFDI) != 40 {
			t.Errorf("LFDI length = %d, want 40", len(result.LFDI))
		}
		if !sepTLS.ValidateSFDI(result.SFDI) {
			t.Errorf("SFDI %q invalid checksum", result.SFDI)
		}
	})

	// Test: Wrong Bearer gets 401
	t.Run("Bearer wrong key rejected", func(t *testing.T) {
		req, _ := http.NewRequest("GET", adminURL+"/api/certs/ca", nil)
		req.Header.Set("Authorization", "Bearer wrong-key")
		resp, err := adminClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != 401 {
			t.Errorf("status = %d, want 401", resp.StatusCode)
		}
	})

	// Test: No auth gets 401
	t.Run("No auth rejected", func(t *testing.T) {
		req, _ := http.NewRequest("GET", adminURL+"/api/certs/ca", nil)
		resp, err := adminClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != 401 {
			t.Errorf("status = %d, want 401", resp.StatusCode)
		}
	})
}

func TestProtocolRegressionWithAdminEnabled(t *testing.T) {
	env := setupTestEnv(t)

	// Start protocol server with admin enabled
	serverTLSCfg, _ := sepTLS.NewServerTLSConfigFromPEM(env.serverCertPEM, env.serverKeyPEM, env.caCertPEM)

	cfg := &config.Config{
		TZOffset:    -28800,
		TimeQuality: sep2.TimeQualityNTP,
		AdminKey:    "test-key",
	}

	listener, _ := net.Listen("tcp", "127.0.0.1:0")
	defer func() { _ = listener.Close() }()

	tlsListener := tls.NewListener(listener, serverTLSCfg)
	stores := newTestStores()
	router := server.NewRouter(cfg, stores, env.svc, "", "", nil)
	srv := &http.Server{Handler: router}
	go func() { _ = srv.Serve(tlsListener) }()
	defer func() { _ = srv.Close() }()

	// Device client (not admin)
	clientTLSCfg, _ := sepTLS.NewClientTLSConfigFromPEM(env.deviceCertPEM, env.deviceKeyPEM, env.caCertPEM)
	client := &http.Client{
		Transport: &http.Transport{TLSClientConfig: clientTLSCfg},
	}
	baseURL := "https://" + listener.Addr().String()

	// Protocol endpoints should still work with device certs
	t.Run("GET /dcap still works", func(t *testing.T) {
		resp, err := client.Get(baseURL + "/dcap")
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != 200 {
			t.Errorf("status = %d, want 200", resp.StatusCode)
		}
	})

	t.Run("GET /tm still works", func(t *testing.T) {
		resp, err := client.Get(baseURL + "/tm")
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != 200 {
			t.Errorf("status = %d, want 200", resp.StatusCode)
		}
	})
}

type testEnv struct {
	caCertPEM     []byte
	serverCertPEM []byte
	serverKeyPEM  []byte
	deviceCertPEM []byte
	deviceKeyPEM  []byte
	svc           *handler.AdminCertService
}

func setupTestEnv(t *testing.T) testEnv {
	t.Helper()

	caCertPEM, caKeyPEM, err := certs.GenerateCA(certs.CAOptions{
		CommonName: "Integration CA",
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
		Hosts: []string{"127.0.0.1", "localhost"},
	})
	if err != nil {
		t.Fatal(err)
	}

	deviceCertPEM, deviceKeyPEM, err := certs.GenerateDeviceCert(caCert, caKey, certs.DeviceCertOptions{
		DeviceType:  certs.DeviceTypeGeneric,
		HWSerialNum: "INTEG-TEST",
	})
	if err != nil {
		t.Fatal(err)
	}

	svc := handler.NewAdminCertService(caCert, caKey, caCertPEM)

	return testEnv{
		caCertPEM:     caCertPEM,
		serverCertPEM: serverCertPEM,
		serverKeyPEM:  serverKeyPEM,
		deviceCertPEM: deviceCertPEM,
		deviceKeyPEM:  deviceKeyPEM,
		svc:           svc,
	}
}
