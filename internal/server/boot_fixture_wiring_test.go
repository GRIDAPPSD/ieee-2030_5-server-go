package server_test

// IEEE-068 boot-fixture wiring test.
//
// Drives the full server.Run() flow with SEP2_BOOT_FIXTURE-equivalent
// config (cfg.BootFixtureFile) and asserts the pre-seeded EndDevice is
// reachable via GET /edev on the live listener. This is the production
// counterpart to internal/bootfixture's unit test: the unit test proves
// the loader writes through the store API, this test proves the loader
// is actually called from Run() before the listener starts serving.

import (
	"context"
	"encoding/xml"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-go/internal/certs"
	"github.com/GRIDAPPSD/ieee-2030_5-go/internal/config"
	"github.com/GRIDAPPSD/ieee-2030_5-go/internal/server"
	sepTLS "gitlab.pnnl.gov/arista/ieee-2030_5/ieee-2030_5-core/pkg/sep2tls"
	"gitlab.pnnl.gov/arista/ieee-2030_5/ieee-2030_5-core/pkg/sep2"
)

// TestBootFixtureWiringGCM asserts the Enphase fixture is loaded into
// stores under GCM mode. The CCM path uses the same Load() call, so we
// only need to cover one cipher mode here — the multi-mode coverage is
// in TestServerIdentityPopulatedUnderCCM (IEEE-001).
func TestBootFixtureWiringGCM(t *testing.T) {
	dir := t.TempDir()

	caCertPEM, caKeyPEM, err := certs.GenerateCA(certs.CAOptions{
		CommonName: "IEEE-068 Test CA",
		ValidYears: 1,
	})
	if err != nil {
		t.Fatalf("GenerateCA: %v", err)
	}
	caCert, err := certs.ParseCertificatePEM(caCertPEM)
	if err != nil {
		t.Fatalf("ParseCertificatePEM(ca): %v", err)
	}
	caKey, err := certs.ParseKeyPEM(caKeyPEM)
	if err != nil {
		t.Fatalf("ParseKeyPEM(ca): %v", err)
	}
	serverCertPEM, serverKeyPEM, err := certs.GenerateServerCert(caCert, caKey, certs.ServerCertOptions{
		Hosts:      []string{"127.0.0.1", "localhost"},
		CommonName: "IEEE-068 Test Server",
		ValidYears: 1,
	})
	if err != nil {
		t.Fatalf("GenerateServerCert: %v", err)
	}
	deviceCertPEM, deviceKeyPEM, err := certs.GenerateDeviceCert(caCert, caKey, certs.DeviceCertOptions{
		DeviceType:  certs.DeviceTypeGeneric,
		HWSerialNum: "IEEE-068-TEST",
	})
	if err != nil {
		t.Fatalf("GenerateDeviceCert: %v", err)
	}

	caFile := filepath.Join(dir, "ca.pem")
	certFile := filepath.Join(dir, "server.pem")
	keyFile := filepath.Join(dir, "server-key.pem")
	if err := os.WriteFile(caFile, caCertPEM, 0o600); err != nil {
		t.Fatalf("write ca: %v", err)
	}
	if err := os.WriteFile(certFile, serverCertPEM, 0o600); err != nil {
		t.Fatalf("write server cert: %v", err)
	}
	if err := os.WriteFile(keyFile, serverKeyPEM, 0o600); err != nil {
		t.Fatalf("write server key: %v", err)
	}

	// Use the test device fixture committed in the repo. The fixture
	// path is resolved relative to the repo root; tests run from the
	// internal/server package dir, so go up two levels.
	repoRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("resolve repo root: %v", err)
	}
	fixturePath := filepath.Join(repoRoot, "test", "csip", "fixtures", "testdevice-edev.yaml")
	if _, err := os.Stat(fixturePath); err != nil {
		t.Fatalf("fixture missing at %s: %v", fixturePath, err)
	}

	clientTLSCfg, err := sepTLS.NewClientTLSConfigFromPEM(deviceCertPEM, deviceKeyPEM, caCertPEM)
	if err != nil {
		t.Fatalf("NewClientTLSConfigFromPEM: %v", err)
	}

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

	client := &http.Client{
		Transport: &http.Transport{TLSClientConfig: clientTLSCfg},
		Timeout:   3 * time.Second,
	}

	resp, err := client.Get("https://" + addr + "/edev")
	if err != nil {
		t.Fatalf("GET /edev: %v", err)
	}
	body, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		t.Fatalf("read /edev body: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /edev status = %d, want 200; body=%s", resp.StatusCode, body)
	}

	var list sep2.EndDeviceList
	if err := xml.Unmarshal(body, &list); err != nil {
		t.Fatalf("unmarshal EndDeviceList: %v\nbody: %s", err, body)
	}

	if list.All != 1 {
		t.Errorf("EndDeviceList.All = %d, want 1; body=%s", list.All, body)
	}
	if len(list.EndDevice) != 1 {
		t.Fatalf("EndDeviceList.EndDevice count = %d, want 1; body=%s", len(list.EndDevice), body)
	}
	dev := list.EndDevice[0]
	if want := "93E795AE91F5F493813E3B39C8BC49F06FCA6258"; dev.LFDI != want {
		t.Errorf("EndDevice.LFDI = %q, want %q", dev.LFDI, want)
	}
	if want := "397028461857"; dev.SFDI != want {
		t.Errorf("EndDevice.SFDI = %q, want %q", dev.SFDI, want)
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

// TestBootFixtureMissingFileFailsRun asserts server.Run aborts (rather
// than booting empty and silently ignoring the typo) when the configured
// SEP2_BOOT_FIXTURE path does not exist. Without this, an operator typo
// would silently produce a working-but-empty server.
func TestBootFixtureMissingFileFailsRun(t *testing.T) {
	dir := t.TempDir()
	caCertPEM, caKeyPEM, err := certs.GenerateCA(certs.CAOptions{
		CommonName: "IEEE-068 Missing-Fixture CA",
		ValidYears: 1,
	})
	if err != nil {
		t.Fatalf("GenerateCA: %v", err)
	}
	caCert, err := certs.ParseCertificatePEM(caCertPEM)
	if err != nil {
		t.Fatalf("ParseCertificatePEM(ca): %v", err)
	}
	caKey, err := certs.ParseKeyPEM(caKeyPEM)
	if err != nil {
		t.Fatalf("ParseKeyPEM(ca): %v", err)
	}
	serverCertPEM, serverKeyPEM, err := certs.GenerateServerCert(caCert, caKey, certs.ServerCertOptions{
		Hosts:      []string{"127.0.0.1", "localhost"},
		CommonName: "IEEE-068 Missing-Fixture Server",
		ValidYears: 1,
	})
	if err != nil {
		t.Fatalf("GenerateServerCert: %v", err)
	}

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

	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("probe listen: %v", err)
	}
	addr := probe.Addr().String()
	_ = probe.Close()

	cfg := &config.Config{
		Addr:            addr,
		CertFile:        certFile,
		KeyFile:         keyFile,
		CAFile:          caFile,
		BootFixtureFile: filepath.Join(dir, "does-not-exist.yaml"),
		TZOffset:        -28800,
		TimeQuality:     sep2.TimeQualityNTP,
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() { errCh <- server.Run(ctx, cfg, nil) }()

	select {
	case err := <-errCh:
		if err == nil {
			t.Fatal("server.Run returned nil for missing boot fixture; want error")
		}
		// We only require that the error mentions the fixture path so an
		// operator can diagnose; the exact wrapping is bootfixture's
		// concern (covered in its own unit test).
		if want := "does-not-exist.yaml"; !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not cite the fixture path", err)
		}
	case <-time.After(3 * time.Second):
		cancel()
		t.Fatal("server.Run did not fail-fast on missing boot fixture within 3s")
	}
}
