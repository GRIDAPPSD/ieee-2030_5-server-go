package server_test

import (
	"context"
	"crypto/tls"
	"encoding/xml"
	"io"
	"net"
	"net/http"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-go/internal/certs"
	"github.com/GRIDAPPSD/ieee-2030_5-go/internal/config"
	"github.com/GRIDAPPSD/ieee-2030_5-go/internal/server"
	sepTLS "github.com/GRIDAPPSD/ieee-2030_5-go/internal/tls"
	"github.com/GRIDAPPSD/ieee-2030_5-go/pkg/sep2"
)

// IEEE-101: end-to-end GET /edev/{id}/rg through the SEP2 protocol mux
// (router + ACL + IdentityMiddleware + NamespaceMiddleware). Verifies the
// admin-write / device-read round trip with a real mTLS client whose LFDI
// matches the seeded EndDevice.
func TestIntegrationRegistrationGet(t *testing.T) {
	caCertPEM, caKeyPEM, err := certs.GenerateCA(certs.CAOptions{
		CommonName: "IEEE-101 Test CA",
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
		CommonName: "IEEE-101 Test Server",
		ValidYears: 1,
	})
	if err != nil {
		t.Fatal(err)
	}

	deviceCertPEM, deviceKeyPEM, err := certs.GenerateDeviceCert(caCert, caKey, certs.DeviceCertOptions{
		DeviceType:  certs.DeviceTypeGeneric,
		HWSerialNum: "IEEE-101-DEV-1",
	})
	if err != nil {
		t.Fatal(err)
	}

	// Derive the device's identity the same way the production server does.
	deviceCert, err := certs.ParseCertificatePEM(deviceCertPEM)
	if err != nil {
		t.Fatal(err)
	}
	deviceLFDI := sepTLS.LFDI(deviceCert)
	deviceSFDI := sepTLS.SFDI(deviceCert)
	deviceID := deviceSFDI[:8]

	serverTLSCfg, err := sepTLS.NewServerTLSConfigFromPEM(serverCertPEM, serverKeyPEM, caCertPEM)
	if err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{
		TZOffset:    0,
		DSTOffset:   0,
		TimeQuality: sep2.TimeQualityNTP,
	}

	stores := newTestStores()

	// Seed the EndDevice + Registration the same way IEEE-095's admin
	// POST /api/devices flow would.
	dev := sep2.EndDevice{
		SubscribableResource: sep2.SubscribableResource{
			Resource: sep2.Resource{Href: "/edev/" + deviceID},
		},
		SFDI: deviceSFDI,
		LFDI: deviceLFDI,
	}
	if err := stores.EndDevices.Create(context.Background(), deviceID, dev); err != nil {
		t.Fatalf("seed EndDevice: %v", err)
	}
	reg := sep2.Registration{
		Resource:           sep2.Resource{Href: "/edev/" + deviceID + "/rg"},
		DateTimeRegistered: 1700000000,
		PIN:                987654,
	}
	if err := stores.Registrations.Create(context.Background(), deviceID, reg); err != nil {
		t.Fatalf("seed Registration: %v", err)
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()

	tlsListener := tls.NewListener(listener, serverTLSCfg)
	router, _ := server.BuildProtocolRouter(cfg, stores, nil, "", "", nil)
	srv := &http.Server{Handler: router}
	go func() { _ = srv.Serve(tlsListener) }()
	defer func() { _ = srv.Close() }()

	clientTLSCfg, err := sepTLS.NewClientTLSConfigFromPEM(deviceCertPEM, deviceKeyPEM, caCertPEM)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: clientTLSCfg}}
	baseURL := "https://" + listener.Addr().String()

	t.Run("GET /edev/{id}/rg returns Registration", func(t *testing.T) {
		resp, err := client.Get(baseURL + "/edev/" + deviceID + "/rg")
		if err != nil {
			t.Fatalf("GET /edev/%s/rg: %v", deviceID, err)
		}
		defer func() { _ = resp.Body.Close() }()

		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			t.Fatalf("status = %d, want 200, body=%s", resp.StatusCode, body)
		}

		body, _ := io.ReadAll(resp.Body)
		var got sep2.Registration
		if err := xml.Unmarshal(body, &got); err != nil {
			t.Fatalf("Unmarshal: %v", err)
		}
		if got.PIN != 987654 {
			t.Errorf("PIN = %d, want 987654", got.PIN)
		}
		if got.DateTimeRegistered != 1700000000 {
			t.Errorf("DateTimeRegistered = %d, want 1700000000", got.DateTimeRegistered)
		}
		if got.Href != "/edev/"+deviceID+"/rg" {
			t.Errorf("Href = %q, want /edev/%s/rg", got.Href, deviceID)
		}
	})

	t.Run("GET /edev/{id}/rg for other device returns 403", func(t *testing.T) {
		// Different EndDevice owned by a different LFDI.
		otherID := "deadbeef"
		otherDev := sep2.EndDevice{
			SubscribableResource: sep2.SubscribableResource{
				Resource: sep2.Resource{Href: "/edev/" + otherID},
			},
			SFDI: "999999999999",
			LFDI: "FFEEDDCCBBAA99887766554433221100AABBCCDD",
		}
		if err := stores.EndDevices.Create(context.Background(), otherID, otherDev); err != nil {
			t.Fatalf("seed other EndDevice: %v", err)
		}
		otherReg := sep2.Registration{
			Resource:           sep2.Resource{Href: "/edev/" + otherID + "/rg"},
			DateTimeRegistered: 1700000001,
			PIN:                111111,
		}
		if err := stores.Registrations.Create(context.Background(), otherID, otherReg); err != nil {
			t.Fatalf("seed other Registration: %v", err)
		}

		resp, err := client.Get(baseURL + "/edev/" + otherID + "/rg")
		if err != nil {
			t.Fatalf("GET /edev/%s/rg: %v", otherID, err)
		}
		_ = resp.Body.Close()

		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("status = %d, want 403", resp.StatusCode)
		}
	})

	t.Run("GET /edev/{id}/rg for unknown device returns 404", func(t *testing.T) {
		resp, err := client.Get(baseURL + "/edev/nosuch99/rg")
		if err != nil {
			t.Fatalf("GET unknown: %v", err)
		}
		_ = resp.Body.Close()

		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("status = %d, want 404", resp.StatusCode)
		}
	})
}
