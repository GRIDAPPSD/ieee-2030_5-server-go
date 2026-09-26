package server_test

// GRIDAPPSD/ieee-2030_5-server-go#443 regression test: the EndDeviceIndex
// allocator must be seeded only after every startup writer of EndDevice
// records (the persisted reload, and SEP2_BOOT_FIXTURE) has run. This drives
// the real server.Run() path, not a hand-built index, so it fails if the
// seeding in server.go moves back ahead of bootfixture.Load.

import (
	"context"
	"encoding/xml"
	"io"
	"net"
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

func TestBootFixtureNumericIDDoesNotLockOutNewRegistrant(t *testing.T) {
	dir := t.TempDir()

	caCertPEM, caKeyPEM, err := certs.GenerateCA(certs.CAOptions{
		CommonName: "boot-fixture-order Test CA",
		ValidYears: 1,
	})
	if err != nil {
		t.Fatalf("GenerateCA: %v", err)
	}
	caCert, caKey, err := parsePEMPair(caCertPEM, caKeyPEM)
	if err != nil {
		t.Fatalf("parsePEMPair: %v", err)
	}
	serverCertPEM, serverKeyPEM, err := certs.GenerateServerCert(caCert, caKey, certs.ServerCertOptions{
		Hosts:      []string{"127.0.0.1", "localhost"},
		CommonName: "boot-fixture-order Test Server",
		ValidYears: 1,
	})
	if err != nil {
		t.Fatalf("GenerateServerCert: %v", err)
	}
	deviceCertPEM, deviceKeyPEM, err := certs.GenerateDeviceCert(caCert, caKey, certs.DeviceCertOptions{
		DeviceType:  certs.DeviceTypeGeneric,
		HWSerialNum: "boot-fixture-order-DEV-1",
	})
	if err != nil {
		t.Fatalf("GenerateDeviceCert: %v", err)
	}
	deviceCert, err := certs.ParseCertificatePEM(deviceCertPEM)
	if err != nil {
		t.Fatalf("ParseCertificatePEM(device): %v", err)
	}
	deviceLFDI := sepTLS.LFDI(deviceCert)
	deviceSFDI := sepTLS.SFDI(deviceCert)

	caFile := filepath.Join(dir, "ca.pem")
	certFile := filepath.Join(dir, "server.pem")
	keyFile := filepath.Join(dir, "server-key.pem")
	for _, p := range []struct {
		path string
		data []byte
	}{{caFile, caCertPEM}, {certFile, serverCertPEM}, {keyFile, serverKeyPEM}} {
		if err := os.WriteFile(p.path, p.data, 0o600); err != nil {
			t.Fatalf("write %s: %v", p.path, err)
		}
	}

	// A fixture record at the allocator's first canonical id, under an
	// identity distinct from the self-registering device below.
	fixturePath := filepath.Join(dir, "fixture.yaml")
	fixtureYAML := "end_devices:\n" +
		"  - id: \"1\"\n" +
		"    sfdi: \"111111111111\"\n" +
		"    lfdi: \"1111111111111111111111111111111111111111\"\n"
	if err := os.WriteFile(fixturePath, []byte(fixtureYAML), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	clientTLSCfg := ccmClientTLSConfig(t, deviceCertPEM, deviceKeyPEM, caCertPEM)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var (
		addr     string
		runErrCh chan error
	)
	const startAttempts = 8
	started := false
startLoop:
	for attempt := 0; attempt < startAttempts; attempt++ {
		probe, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("probe listen: %v", err)
		}
		addr = probe.Addr().String()
		_ = probe.Close()

		cfg := &config.Config{
			Addr:            addr,
			CertFile:        certFile,
			KeyFile:         keyFile,
			CAFile:          caFile,
			BootFixtureFile: fixturePath,
			TZOffset:        -28800,
			DSTOffset:       3600,
			DSTStart:        1583661600,
			DSTEnd:          1604214000,
			TimeQuality:     sep2.TimeQualityNTP,
		}

		runErrCh = make(chan error, 1)
		go func(c *config.Config) {
			runErrCh <- server.Run(ctx, c, nil)
		}(cfg)

		readyCh := make(chan bool, 1)
		go func() {
			readyCh <- waitForServerReady(addr, 2*time.Second, clientTLSCfg)
		}()
		select {
		case ok := <-readyCh:
			if ok {
				started = true
				break startLoop
			}
			t.Fatalf("server did not start accepting TLS on %s (attempt %d)", addr, attempt+1)
		case err := <-runErrCh:
			t.Logf("server.Run start attempt %d failed (%v); retrying", attempt+1, err)
			go func() { <-readyCh }()
		}
	}
	if !started {
		t.Fatalf("server failed to start after %d attempts", startAttempts)
	}

	client := ccmHTTPClient(clientTLSCfg, 3*time.Second)

	// The self-registering device presents only its certificate: the
	// allocator's first free id must already know the fixture occupies "1",
	// or Allocate hands this identity "1" too and every retry gets 409
	// forever (idx.Allocate is idempotent per key).
	resp, err := client.Post("https://"+addr+"/edev", "application/sep+xml", nil)
	if err != nil {
		t.Fatalf("POST /edev: %v", err)
	}
	body, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		t.Fatalf("read POST /edev body: %v", err)
	}
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST /edev status = %d, want 201; body=%s", resp.StatusCode, body)
	}
	if loc := resp.Header.Get("Location"); loc == "" || loc == "/edev/1" {
		t.Errorf("Location = %q, want a fresh id distinct from the fixture's /edev/1", loc)
	}

	var dev sep2.EndDevice
	if err := xml.Unmarshal(body, &dev); err != nil {
		t.Fatalf("unmarshal EndDevice: %v\nbody: %s", err, body)
	}
	if dev.LFDI != deviceLFDI || dev.SFDI != deviceSFDI {
		t.Errorf("created device = %+v, want the registering device's own identity %s/%s", dev, deviceLFDI, deviceSFDI)
	}

	// Retry: idx.Allocate is idempotent per key, so a genuine lockout would
	// reproduce the 409 on every subsequent attempt too.
	resp2, err := client.Post("https://"+addr+"/edev", "application/sep+xml", nil)
	if err != nil {
		t.Fatalf("second POST /edev: %v", err)
	}
	body2, _ := io.ReadAll(resp2.Body)
	_ = resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("second POST /edev status = %d, want 200 (idempotent re-registration); body=%s", resp2.StatusCode, body2)
	}

	cancel()
	select {
	case err := <-runErrCh:
		if err != nil {
			t.Errorf("server.Run returned error after cancel: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Error("server.Run did not return within 3s after context cancel")
	}
}
