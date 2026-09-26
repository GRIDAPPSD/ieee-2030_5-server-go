package server_test

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

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	gotls "github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2tls/gotls"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/certs"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/config"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/server"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

// TestBootFixtureSeedKeepsProtocolEditAcrossRestart drives server.Run twice on
// one data_dir with a boot fixture (#352): an EndDevice edit made over the
// protocol during the first run must still be served after the second start.
func TestBootFixtureSeedKeepsProtocolEditAcrossRestart(t *testing.T) {
	dir := t.TempDir()
	dataDir := filepath.Join(dir, "data")
	if err := os.Mkdir(dataDir, 0o700); err != nil {
		t.Fatalf("mkdir data_dir: %v", err)
	}

	caCertPEM, caKeyPEM, err := certs.GenerateCA(certs.CAOptions{
		CommonName: "boot-fixture-seed Test CA",
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
		CommonName: "boot-fixture-seed Test Server",
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

	repoRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("resolve repo root: %v", err)
	}
	baseFixture, err := os.ReadFile(filepath.Join(repoRoot, "test", "csip", "fixtures", "testdevice-edev.yaml"))
	if err != nil {
		t.Fatalf("read test device fixture: %v", err)
	}
	// The committed fixture plus a DERProgram under its device, so the second
	// boot reaches the DERProgram reconcile as well as the EndDevice one.
	fixture := string(baseFixture) + `
der_programs:
  - end_device_id: "testdevice"
    id: "p1"
    mrid: "A1A1A1A1A1A1A1A1"
    primacy: 3
`
	fixturePath := filepath.Join(dir, "fixture.yaml")
	if err := os.WriteFile(fixturePath, []byte(fixture), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	pkiDir := filepath.Join(repoRoot, "testdata", "csip-pki", "testdevice")
	deviceCertPEM, err := os.ReadFile(filepath.Join(pkiDir, "device_chain.pem"))
	if err != nil {
		t.Fatalf("read test device chain: %v", err)
	}
	deviceKeyPEM, err := os.ReadFile(filepath.Join(pkiDir, "device_key.pem"))
	if err != nil {
		t.Fatalf("read test device key: %v", err)
	}
	clientTLSCfg := ccmClientTLSConfig(t, deviceCertPEM, deviceKeyPEM, caCertPEM)
	client := ccmHTTPClient(clientTLSCfg, 3*time.Second)

	newConfig := func(addr string) *config.Config {
		return &config.Config{
			Addr:            addr,
			CertFile:        certFile,
			KeyFile:         keyFile,
			CAFile:          caFile,
			ExtraClientCAs:  []string{filepath.Join(pkiDir, "root_ca.pem")},
			BootFixtureFile: fixturePath,
			DataDir:         dataDir,
			TZOffset:        -28800,
			DSTOffset:       3600,
			DSTStart:        1583661600,
			DSTEnd:          1604214000,
			TimeQuality:     sep2.TimeQualityNTP,
		}
	}

	addr, stop := startSeedRun(t, newConfig, clientTLSCfg)
	if got := getTestDevice(t, client, addr); got.Enabled == nil || !*got.Enabled {
		t.Fatalf("first run: Enabled = %v, want the fixture's true", got.Enabled)
	}
	putBody := `<EndDevice xmlns="urn:ieee:std:2030.5:ns"><sFDI>397028461857</sFDI><changedTime>0</changedTime><enabled>false</enabled></EndDevice>`
	req, err := http.NewRequest(http.MethodPut, "https://"+addr+"/edev/testdevice", strings.NewReader(putBody))
	if err != nil {
		t.Fatalf("build PUT: %v", err)
	}
	req.Header.Set("Content-Type", "application/sep+xml")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("PUT /edev/testdevice: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("PUT /edev/testdevice status = %d, want 204; body=%s", resp.StatusCode, body)
	}
	if got := getTestDevice(t, client, addr); got.Enabled == nil || *got.Enabled {
		t.Fatalf("first run after PUT: Enabled = %v, want false", got.Enabled)
	}
	stop()

	addr, stop = startSeedRun(t, newConfig, clientTLSCfg)
	got := getTestDevice(t, client, addr)
	if got.Enabled == nil || *got.Enabled {
		t.Errorf("second run: Enabled = %v, want the edited false", got.Enabled)
	}
	if want := "93E795AE91F5F493813E3B39C8BC49F06FCA6258"; got.LFDI != want {
		t.Errorf("second run: LFDI = %q, want %q", got.LFDI, want)
	}
	stop()

	programs, err := memory.NewDERProgramStoreWithPersistence(filepath.Join(dataDir, "derprograms.json"))
	if err != nil {
		t.Fatalf("reopen DERProgram store: %v", err)
	}
	prog, err := programs.Get(context.Background(), "testdevice", "p1")
	if err != nil {
		t.Fatalf("DERProgram (testdevice, p1) after two runs: %v", err)
	}
	if prog.Primacy != 3 || prog.MRID != "A1A1A1A1A1A1A1A1" {
		t.Errorf("DERProgram = Primacy %d MRID %q, want 3 and A1A1A1A1A1A1A1A1", prog.Primacy, prog.MRID)
	}
	if _, err := os.Stat(filepath.Join(dataDir, "bootfixture-seed.json")); err != nil {
		t.Errorf("seed record after two runs: %v", err)
	}
}

// startSeedRun starts server.Run and waits until it serves TLS. Only a bind
// race is retried: any other Run error is the failure under test.
func startSeedRun(t *testing.T, newConfig func(addr string) *config.Config, clientTLSCfg *gotls.Config) (string, func()) {
	t.Helper()
	const startAttempts = 8
	for attempt := 0; attempt < startAttempts; attempt++ {
		probe, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("probe listen: %v", err)
		}
		addr := probe.Addr().String()
		_ = probe.Close()

		ctx, cancel := context.WithCancel(context.Background())
		runErrCh := make(chan error, 1)
		go func() { runErrCh <- server.Run(ctx, newConfig(addr), nil) }()

		readyCh := make(chan bool, 1)
		go func() { readyCh <- waitForServerReady(addr, 2*time.Second, clientTLSCfg) }()
		select {
		case ok := <-readyCh:
			if !ok {
				cancel()
				t.Fatalf("server did not start accepting TLS on %s", addr)
			}
			stop := func() {
				t.Helper()
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
			return addr, stop
		case err := <-runErrCh:
			cancel()
			<-readyCh
			if err != nil && strings.Contains(err.Error(), "address already in use") {
				t.Logf("server.Run start attempt %d lost a bind race (%v); retrying", attempt+1, err)
				continue
			}
			t.Fatalf("server.Run failed to start: %v", err)
		}
	}
	t.Fatalf("server failed to start after %d attempts", startAttempts)
	return "", nil
}

func getTestDevice(t *testing.T, client *http.Client, addr string) sep2.EndDevice {
	t.Helper()
	resp, err := client.Get("https://" + addr + "/edev/testdevice")
	if err != nil {
		t.Fatalf("GET /edev/testdevice: %v", err)
	}
	body, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /edev/testdevice status = %d, want 200; body=%s", resp.StatusCode, body)
	}
	var dev sep2.EndDevice
	if err := xml.Unmarshal(body, &dev); err != nil {
		t.Fatalf("unmarshal EndDevice: %v\nbody: %s", err, body)
	}
	return dev
}
