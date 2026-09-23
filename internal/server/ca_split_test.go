package server_test

// #622: the protocol listener's ClientCAs trust bundle must come from the
// DEVICE CA (Config.DeviceCAFile / EffectiveDeviceCA), not the serving CA,
// once the two are split. The role map's own "silent divergence" analysis
// (artifacts/outputs/pike-server-622-ca-role-map-2026-09-23.md, section 3)
// names this projection (internal/server/server.go's embedCfg.CAFile
// assignment) as the highest-severity site to get wrong: a server cert
// signed by the wrong CA fails nowhere on this host, but a client-trust
// pool built from the wrong CA is loud immediately, at the very next
// handshake. This test drives that handshake for real, both ways.

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	sepTLS "github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2tls"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/certs"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/config"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/server"
)

// TestProtocolListenerTrustsOnlyTheDeviceCA is #622 done-condition 2 driven
// at the real TLS listener: with ServingCAFile and DeviceCAFile pointed at
// two different CAs, a device cert signed by the device CA completes the
// mTLS handshake and reaches /dcap, and one signed by the serving CA is
// refused at the handshake. Both directions are asserted so this cannot
// pass by the listener trusting everything.
func TestProtocolListenerTrustsOnlyTheDeviceCA(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	servingCACertPEM, servingCAKeyPEM, err := certs.GenerateCA(certs.CAOptions{CommonName: "622 Serving CA", ValidYears: 1})
	if err != nil {
		t.Fatalf("GenerateCA(serving): %v", err)
	}
	deviceCACertPEM, deviceCAKeyPEM, err := certs.GenerateCA(certs.CAOptions{CommonName: "622 Device CA", ValidYears: 1})
	if err != nil {
		t.Fatalf("GenerateCA(device): %v", err)
	}

	servingCACert, err := certs.ParseCertificatePEM(servingCACertPEM)
	if err != nil {
		t.Fatalf("parse serving CA: %v", err)
	}
	servingCAKey, err := certs.ParseKeyPEM(servingCAKeyPEM)
	if err != nil {
		t.Fatalf("parse serving CA key: %v", err)
	}
	deviceCACert, err := certs.ParseCertificatePEM(deviceCACertPEM)
	if err != nil {
		t.Fatalf("parse device CA: %v", err)
	}
	deviceCAKey, err := certs.ParseKeyPEM(deviceCAKeyPEM)
	if err != nil {
		t.Fatalf("parse device CA key: %v", err)
	}

	// The server's own leaf chains to the SERVING CA (#622's stated role).
	// This test's clients skip server-cert verification of their own
	// accord below by trusting servingCACertPEM as RootCAs; what is under
	// test is only the listener's CLIENT-cert (ClientCAs) check.
	serverCertPEM, serverKeyPEM, err := certs.GenerateServerCert(servingCACert, servingCAKey, certs.ServerCertOptions{
		Hosts:      []string{"127.0.0.1", "localhost"},
		CommonName: "622 protocol listener",
		ValidYears: 1,
	})
	if err != nil {
		t.Fatalf("GenerateServerCert: %v", err)
	}

	// The "good" device cert: signed by the device CA.
	goodDeviceCertPEM, goodDeviceKeyPEM, err := certs.GenerateDeviceCert(deviceCACert, deviceCAKey, certs.DeviceCertOptions{
		DeviceType:  certs.DeviceTypeGeneric,
		HWSerialNum: "622-good",
	})
	if err != nil {
		t.Fatalf("GenerateDeviceCert(device CA): %v", err)
	}

	// The "bad" device-shaped cert: signed by the SERVING CA instead. This
	// is the exact shape the protocol listener must refuse once
	// EffectiveDeviceCA, not CAFile, feeds the ClientCAs pool.
	badDeviceCertPEM, badDeviceKeyPEM, err := certs.GenerateDeviceCert(servingCACert, servingCAKey, certs.DeviceCertOptions{
		DeviceType:  certs.DeviceTypeGeneric,
		HWSerialNum: "622-bad",
	})
	if err != nil {
		t.Fatalf("GenerateDeviceCert(serving CA): %v", err)
	}

	write := func(name string, data []byte) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, data, 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
		return p
	}
	servingCAFile := write("serving-ca.pem", servingCACertPEM)
	deviceCAFile := write("device-ca.pem", deviceCACertPEM)
	certFile := write("server.pem", serverCertPEM)
	keyFile := write("server-key.pem", serverKeyPEM)

	sep2Addr := mustProbePort(t)
	cfg := &config.Config{
		Addr:          sep2Addr,
		CertFile:      certFile,
		KeyFile:       keyFile,
		ServingCAFile: servingCAFile,
		DeviceCAFile:  deviceCAFile,
		TZOffset:      -28800,
		TimeQuality:   sep2.TimeQualityNTP,
	}

	ctx, cancel := context.WithCancel(context.Background())
	runErrCh := make(chan error, 1)
	go func() { runErrCh <- server.Run(ctx, cfg, nil) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-runErrCh:
		case <-time.After(3 * time.Second):
			t.Error("server.Run did not exit within 3s after cancel")
		}
	})

	goodClientTLS, err := sepTLS.NewClientTLSConfigFromPEM(goodDeviceCertPEM, goodDeviceKeyPEM, servingCACertPEM)
	if err != nil {
		t.Fatalf("NewClientTLSConfigFromPEM(good): %v", err)
	}
	if !waitForServerReady(sep2Addr, 3*time.Second, goodClientTLS) {
		t.Fatal("protocol listener never became ready for the device-CA-signed client")
	}

	goodClient := &http.Client{
		Transport: &http.Transport{TLSClientConfig: goodClientTLS},
		Timeout:   2 * time.Second,
	}
	resp, err := goodClient.Get("https://" + sep2Addr + "/dcap")
	if err != nil {
		t.Fatalf("device-CA-signed client: GET /dcap: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("device-CA-signed client: status = %d, want 200", resp.StatusCode)
	}

	badClientTLS, err := sepTLS.NewClientTLSConfigFromPEM(badDeviceCertPEM, badDeviceKeyPEM, servingCACertPEM)
	if err != nil {
		t.Fatalf("NewClientTLSConfigFromPEM(bad): %v", err)
	}
	badClient := &http.Client{
		Transport: &http.Transport{TLSClientConfig: badClientTLS},
		Timeout:   1 * time.Second,
	}
	if _, err := badClient.Get("https://" + sep2Addr + "/dcap"); err == nil {
		t.Error("serving-CA-signed client was accepted by the protocol listener; ClientCAs pool is not routed to the device CA")
	}
}
