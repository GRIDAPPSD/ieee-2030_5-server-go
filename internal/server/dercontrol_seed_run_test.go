package server_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/certs"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/config"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/dercontrol"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

// TestDERControlSeedSurvivesRestart is GRIDAPPSD/ieee-2030_5-server-go#565
// fix round 1, item 3: nothing proved that server.Run hands DERControlStore
// the real data_dir path (server.go's two EffectiveStorePath calls), as
// opposed to a hard-coded "" that would leave every Run-level test green
// while silently dropping persistence. A control declared in the boot
// fixture is created on the first run; the second run, over the same
// data_dir, must find it already on disk, read back through a store opened
// independently of Run.
func TestDERControlSeedSurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	dataDir := filepath.Join(dir, "data")
	if err := os.Mkdir(dataDir, 0o700); err != nil {
		t.Fatalf("mkdir data_dir: %v", err)
	}

	caCertPEM, caKeyPEM, err := certs.GenerateCA(certs.CAOptions{
		CommonName: "dercontrol-seed Test CA",
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
		CommonName: "dercontrol-seed Test Server",
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
	// The committed fixture plus an FSA, a DERProgram and one DERControl
	// under it, so the boot fixture is what "issues" the control, exactly
	// as TestBootFixtureSeedKeepsProtocolEditAcrossRestart's der_programs
	// section does for DERProgram persistence.
	fixture := string(baseFixture) + `
fsas:
  - end_device_id: "testdevice"
    id: "f1"
der_programs:
  - end_device_id: "testdevice"
    fsa_id: "f1"
    id: "p1"
    mrid: "B1B1B1B1B1B1B1B1"
    primacy: 3
der_controls:
  - end_device_id: "testdevice"
    fsa_id: "f1"
    der_program_id: "p1"
    id: "c1"
    mrid: "C1C1C1C1C1C1C1C1"
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

	_, stop := startSeedRun(t, newConfig, clientTLSCfg)
	stop()

	_, stop = startSeedRun(t, newConfig, clientTLSCfg)
	stop()

	dercs, err := memory.NewDERControlStoreWithPersistence(filepath.Join(dataDir, "dercontrols.json"))
	if err != nil {
		t.Fatalf("reopen DERControl store: %v", err)
	}
	got, err := dercs.Get(context.Background(), "testdevice/f1/p1", "c1")
	if err != nil {
		t.Fatalf("DERControl (testdevice/f1/p1, c1) after two runs: %v", err)
	}
	if got.MRID != "C1C1C1C1C1C1C1C1" {
		t.Errorf("DERControl MRID after two runs = %q, want C1C1C1C1C1C1C1C1", got.MRID)
	}
}

// TestDERControlLifecycleStorePath_DataDirDerived is round 1 item 3's other
// half: server.go's second EffectiveStorePath call names "dercontrol-
// lifecycles". Nothing wires a live create path onto LifecycleStore yet
// (internal/dercontrol.Issuer is not reachable from any admin route until
// #566), so this cannot drive a create through server.Run the way the
// DERControl test above does; it follows the same shape
// TestEndDeviceManagementStoreWiring_DataDirDerivedPath already uses for
// exactly that reason: the exact server.go callsite path, and a real
// round trip through the store the callsite constructs.
func TestDERControlLifecycleStorePath_DataDirDerived(t *testing.T) {
	dataDir := t.TempDir()
	cfg := &config.Config{DataDir: dataDir}

	wantPath := filepath.Join(dataDir, "dercontrol-lifecycles.json")
	gotPath := cfg.EffectiveStorePath("dercontrol-lifecycles", "")
	if gotPath != wantPath {
		t.Fatalf("EffectiveStorePath = %q, want %q", gotPath, wantPath)
	}

	store, err := dercontrol.NewLifecycleStoreWithPersistence(gotPath)
	if err != nil {
		t.Fatalf("NewLifecycleStoreWithPersistence(%q): %v", gotPath, err)
	}
	cancelledAt := int64(1000)
	if err := store.Create(context.Background(), "0/0/0", "c1", dercontrol.LifecycleRecord{CancelledAt: &cancelledAt}); err != nil {
		t.Fatalf("store.Create: %v", err)
	}

	info, err := os.Stat(wantPath)
	if err != nil {
		t.Fatalf("expected snapshot at %q: %v", wantPath, err)
	}
	if info.Size() == 0 {
		t.Fatalf("snapshot at %q is empty", wantPath)
	}
}
