package server_test

// #875: Run gives the protocol router and the admin Devices payload one
// activity recorder. The unit tests cover the recorder and the payload; this
// proves the two ends are joined, which neither can show on its own.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/config"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/server"
)

func TestRunJoinsProtocolActivityToTheDevicesPayload(t *testing.T) {
	c := newSplitListenerCerts(t)
	device := newManagementE2EDevice(t, c, "comms-run")

	fixture := filepath.Join(t.TempDir(), "comms-run.yaml")
	fixtureYAML := fmt.Sprintf("end_devices:\n  - id: \"0\"\n    sfdi: %q\n    lfdi: %q\n    enabled: true\n", device.sfdi, device.lfdi)
	if err := os.WriteFile(fixture, []byte(fixtureYAML), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{
		Addr:              c.sep2Probe,
		CertFile:          c.certFile,
		KeyFile:           c.keyFile,
		CAFile:            c.caFile,
		AdminListen:       c.adminProbe,
		AdminKey:          adminTestKey,
		TimeQuality:       sep2.TimeQualityNTP,
		BootFixtureFile:   fixture,
		CommsOfflineAfter: 90 * time.Second,
	}
	ctx, cancel := context.WithCancel(context.Background())
	runErr := make(chan error, 1)
	go func() { runErr <- server.Run(ctx, cfg, c.svc) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-runErr:
		case <-time.After(5 * time.Second):
			t.Error("server.Run did not exit within 5s after cancel")
		}
	})
	if !waitForServerReady(c.sep2Probe, 5*time.Second, device.tlsConfig) {
		t.Fatal("SEP2 listener never became ready")
	}

	type payload struct {
		Devices []struct {
			LFDI        string  `json:"lfdi"`
			Comms       string  `json:"comms"`
			LastRequest *string `json:"lastRequest"`
		} `json:"devices"`
		CommsOfflineAfterSeconds int `json:"commsOfflineAfterSeconds"`
	}
	admin := &http.Client{Timeout: 3 * time.Second}
	read := func() payload {
		t.Helper()
		var p payload
		deadline := time.Now().Add(3 * time.Second)
		for {
			req, _ := http.NewRequest(http.MethodGet, "http://"+c.adminProbe+"/dashboard/data", nil)
			req.Header.Set("Authorization", "Bearer "+adminTestKey)
			resp, err := admin.Do(req)
			if err == nil {
				body, _ := io.ReadAll(resp.Body)
				_ = resp.Body.Close()
				if err := json.Unmarshal(body, &p); err != nil || len(p.Devices) != 1 {
					t.Fatalf("GET /dashboard/data = %d %s (err %v), want one device", resp.StatusCode, body, err)
				}
				return p
			}
			if time.Now().After(deadline) {
				t.Fatalf("GET /dashboard/data: %v", err)
			}
			time.Sleep(25 * time.Millisecond)
		}
	}

	p := read()
	if p.CommsOfflineAfterSeconds != 90 {
		t.Errorf("commsOfflineAfterSeconds = %d, want 90 from SEP2_COMMS_OFFLINE_AFTER_SECONDS", p.CommsOfflineAfterSeconds)
	}

	proto := ccmHTTPClient(device.tlsConfig, 3*time.Second)
	resp, err := proto.Get("https://" + c.sep2Probe + "/dcap")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /dcap: status %d, want 200", resp.StatusCode)
	}

	p = read()
	if d := p.Devices[0]; d.LFDI != device.lfdi || d.Comms != "online" || d.LastRequest == nil {
		t.Errorf("after a protocol request: device = %+v, want online with a lastRequest", d)
	}
}
