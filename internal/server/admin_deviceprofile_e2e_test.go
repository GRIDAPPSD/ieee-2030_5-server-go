package server_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/config"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/server"
)

// bootProfileServer runs server.Run over cfg until the admin listener
// answers, and returns a stop function that waits for Run to exit.
func bootProfileServer(t *testing.T, cfg *config.Config, c *splitListenerCerts) (stop func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	runErrCh := make(chan error, 1)
	go func() { runErrCh <- server.Run(ctx, cfg, c.svc) }()
	stop = func() {
		cancel()
		select {
		case <-runErrCh:
		case <-time.After(5 * time.Second):
			t.Error("server.Run did not exit within 5s after cancel")
		}
	}

	client := &http.Client{Timeout: 500 * time.Millisecond}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := client.Get("http://" + c.adminProbe + "/api/certs/ca")
		if err == nil {
			_ = resp.Body.Close()
			return stop
		}
		select {
		case runErr := <-runErrCh:
			t.Fatalf("server.Run exited during boot: %v", runErr)
		default:
		}
		time.Sleep(25 * time.Millisecond)
	}
	stop()
	t.Fatalf("admin listener never became ready on %s", c.adminProbe)
	return nil
}

func profileAdminDo(t *testing.T, addr, method, path, body string) (int, string) {
	t.Helper()
	req, err := http.NewRequest(method, "http://"+addr+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+adminTestKey)
	resp, err := (&http.Client{Timeout: 2 * time.Second}).Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

// TestDeviceProfileNameSurvivesRestart writes a name through the admin plane
// of a real server.Run with SEP2_DATA_DIR set, stops it, boots a second one
// over the same directory and reads the name back over the wire.
func TestDeviceProfileNameSurvivesRestart(t *testing.T) {
	t.Parallel()
	c := newSplitListenerCerts(t)
	cfg := &config.Config{
		Addr:        c.sep2Probe,
		CertFile:    c.certFile,
		KeyFile:     c.keyFile,
		CAFile:      c.caFile,
		AdminListen: c.adminProbe,
		AdminKey:    adminTestKey,
		DataDir:     t.TempDir(),
		TZOffset:    -28800,
		TimeQuality: sep2.TimeQualityNTP,
	}
	const lfdi = "AAAA000000000000000000000000000000000001"

	stop := bootProfileServer(t, cfg, c)
	status, body := profileAdminDo(t, c.adminProbe, http.MethodPut, "/api/device-profiles/"+lfdi, `{"name":"Roof Gateway","kind":"aggregator"}`)
	if status != http.StatusOK {
		stop()
		t.Fatalf("PUT status = %d; body = %s", status, body)
	}
	stop()

	stop = bootProfileServer(t, cfg, c)
	defer stop()
	status, body = profileAdminDo(t, c.adminProbe, http.MethodGet, "/api/device-profiles", "")
	if status != http.StatusOK {
		t.Fatalf("GET status = %d; body = %s", status, body)
	}
	var got struct {
		Profiles []struct{ LFDI, Name, Kind string } `json:"profiles"`
	}
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatalf("decode: %v; body = %s", err, body)
	}
	if len(got.Profiles) != 1 || got.Profiles[0].LFDI != lfdi || got.Profiles[0].Name != "Roof Gateway" || got.Profiles[0].Kind != "aggregator" {
		t.Errorf("after restart profiles = %+v, want the one written before", got.Profiles)
	}
}
