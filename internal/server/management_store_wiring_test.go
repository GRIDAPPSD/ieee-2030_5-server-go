package server_test

import (
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/config"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/server"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

// #677 fix round, item 6 / C2: the seven EndDeviceManagementStore
// persistence tests prove the store persists when handed a path; nothing
// proved that server.Run hands it one.
//
// TestEndDeviceManagementStoreWiring_ThroughServerRun is the decisive test:
// it drives a real server.Run and a real admin-plane create over the wire,
// then checks the file the production wiring is supposed to have written.
// The two tests below it follow subscription_store_wiring_test.go's shape
// (recomputing cfg.EffectiveStorePath and handing the result straight to
// the store constructor) for the regressions that shape actually catches -
// a wrong store-name string, or a revert to a hand-rolled path - but that
// shape never calls into server.go at all, so on its own it does NOT
// observe a mutation of the call server.go:137 makes; the coverage lane's
// review cited it as sufficient, and proving that claim (PROVE THE CHECK
// CAN FAIL, applied to the recommendation itself) is what surfaced the
// gap. TestEndDeviceManagementStoreWiring_ThroughServerRun is what closes
// it: it fails if server.go stops calling EffectiveStorePath for this
// store, however that regression is shaped.
func TestEndDeviceManagementStoreWiring_ThroughServerRun(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()
	c := newSplitListenerCerts(t)
	cfg := &config.Config{
		Addr:        c.sep2Probe,
		CertFile:    c.certFile,
		KeyFile:     c.keyFile,
		CAFile:      c.caFile,
		AdminListen: c.adminProbe,
		AdminKey:    adminTestKey,
		DataDir:     dataDir,
		TZOffset:    -28800,
		TimeQuality: sep2.TimeQualityNTP,
	}

	ctx, cancel := context.WithCancel(context.Background())
	runErrCh := make(chan error, 1)
	go func() { runErrCh <- server.Run(ctx, cfg, c.svc) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-runErrCh:
		case <-time.After(5 * time.Second):
			t.Error("server.Run did not exit within 5s after cancel")
		}
	})

	client := &http.Client{Timeout: 500 * time.Millisecond}
	deadline := time.Now().Add(5 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		resp, err := client.Get("http://" + c.adminProbe + "/api/certs/ca")
		if err == nil {
			_ = resp.Body.Close()
			lastErr = nil
			break
		}
		lastErr = err
		select {
		case runErr := <-runErrCh:
			t.Fatalf("server.Run exited during boot: %v", runErr)
		default:
		}
		time.Sleep(25 * time.Millisecond)
	}
	if lastErr != nil {
		t.Fatalf("admin listener never became ready on %s: %v", c.adminProbe, lastErr)
	}

	body := `{"managerLFDI":"AAAA000000000000000000000000000000000001","managedLFDI":"BBBB000000000000000000000000000000000002"}`
	req, err := http.NewRequest(http.MethodPost, "http://"+c.adminProbe+"/api/management-pairs", strings.NewReader(body))
	if err != nil {
		t.Fatalf("build create request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+adminTestKey)
	createResp, err := client.Do(req)
	if err != nil {
		t.Fatalf("POST /api/management-pairs: %v", err)
	}
	defer func() { _ = createResp.Body.Close() }()
	if createResp.StatusCode != http.StatusCreated {
		b, _ := io.ReadAll(createResp.Body)
		t.Fatalf("POST /api/management-pairs: status = %d, want 201; body = %s", createResp.StatusCode, b)
	}

	wantPath := filepath.Join(dataDir, "enddevicemanagement.json")
	info, err := os.Stat(wantPath)
	if err != nil {
		t.Fatalf("expected the real server.Run wiring to have written a snapshot at %q: %v", wantPath, err)
	}
	if info.Size() == 0 {
		t.Fatalf("snapshot at %q is empty", wantPath)
	}
}

func TestEndDeviceManagementStoreWiring_DataDirDerivedPath(t *testing.T) {
	t.Parallel()

	dataDir := t.TempDir()
	cfg := &config.Config{DataDir: dataDir}

	// Exact callsite shape from internal/server/server.go.
	wantPath := filepath.Join(dataDir, "enddevicemanagement.json")
	gotPath := cfg.EffectiveStorePath("enddevicemanagement", "")
	if gotPath != wantPath {
		t.Fatalf("EffectiveStorePath = %q, want %q", gotPath, wantPath)
	}

	store, err := memory.NewEndDeviceManagementStoreWithPersistence(gotPath)
	if err != nil {
		t.Fatalf("NewEndDeviceManagementStoreWithPersistence(%q): %v", gotPath, err)
	}

	// Trigger a write so the snapshot lands on disk.
	if err := store.Assign(context.Background(), "AAAA000000000000000000000000000000000001", "BBBB000000000000000000000000000000000002"); err != nil {
		t.Fatalf("store.Assign: %v", err)
	}

	info, err := os.Stat(wantPath)
	if err != nil {
		t.Fatalf("expected snapshot at %q: %v", wantPath, err)
	}
	if info.Size() == 0 {
		t.Fatalf("snapshot at %q is empty", wantPath)
	}
}

// #677 fix round regression: an empty DataDir must keep the store pure
// in-memory, with no on-disk artifact, mirroring
// TestSubscriptionStoreWiring_InMemoryWhenBothEmpty.
func TestEndDeviceManagementStoreWiring_InMemoryWhenDataDirEmpty(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{}

	gotPath := cfg.EffectiveStorePath("enddevicemanagement", "")
	if gotPath != "" {
		t.Fatalf("EffectiveStorePath = %q, want \"\" (in-memory)", gotPath)
	}

	store, err := memory.NewEndDeviceManagementStoreWithPersistence(gotPath)
	if err != nil {
		t.Fatalf("NewEndDeviceManagementStoreWithPersistence(\"\"): %v", err)
	}
	if err := store.Assign(context.Background(), "AAAA000000000000000000000000000000000001", "BBBB000000000000000000000000000000000002"); err != nil {
		t.Fatalf("store.Assign: %v", err)
	}
	// No file path to check: pure in-memory.
}
