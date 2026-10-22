package server_test

// #1064 done-when, end to end: a booted server writes a request line and a
// LogEvent line to the file of the device that made the requests.

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	sepTLS "github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2tls"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/certs"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/config"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/server"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/devicelog"
)

// Not parallel: it swaps the device log starter, which parallel tests read, and the
// parallel ones do not start until every serial test has finished.
func TestRunWritesRequestAndLogEventLinesToTheDevicesFile(t *testing.T) {
	var started *devicelog.Writer
	var orig server.DeviceLogStarter
	orig = server.SetStartDeviceLog(func(cfg *config.Config) (*devicelog.Writer, func(), error) {
		w, closeLog, err := orig(cfg)
		started = w
		return w, closeLog, err
	})
	t.Cleanup(func() { server.SetStartDeviceLog(orig) })

	env, logDir := bootWithBindRetryDeviceLog(t)

	// The device's first lines go to the shared file while its registration
	// lookup runs; GET until its own file exists, then once more after the
	// target lookup has had time to land.
	base := "https://" + env.sep2Addr
	getEdev := func() {
		resp, err := env.deviceHTTP.Get(base + "/edev/1")
		if err != nil {
			t.Fatalf("GET /edev/1: %v", err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET /edev/1 status = %d, want 200", resp.StatusCode)
		}
	}
	ownFile := filepath.Join(logDir, env.deviceLFDI+".jsonl")
	for deadline := time.Now().Add(3 * time.Second); ; {
		getEdev()
		time.Sleep(10 * time.Millisecond)
		if _, err := os.Stat(ownFile); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the device never got its own file")
		}
	}
	time.Sleep(100 * time.Millisecond)
	getEdev()

	ext := uint32(9001)
	body, err := xml.Marshal(&sep2.LogEvent{
		CreatedDateTime: 1604963588,
		Details:         "boot probe",
		ExtendedData:    &ext,
		FunctionSet:     sep2.FunctionSetLogEvent,
		LogEventCode:    7,
		LogEventID:      1234,
		LogEventPEN:     54465,
		ProfileID:       2,
	})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := env.deviceHTTP.Post(base+"/edev/1/lel?probe=QUERYTOKEN", "application/sep+xml", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST /edev/1/lel: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST /edev/1/lel status = %d, want 201", resp.StatusCode)
	}

	// #1066: the same lines come back through the admin plane's read route,
	// which needs the credential even from loopback. The writer is
	// asynchronous, so poll until the LogEvent line has landed.
	logsPath := "/api/device-logs/" + env.deviceLFDI
	if resp, err := (&http.Client{Timeout: 2 * time.Second}).Get("http://" + env.adminAddr + logsPath); err != nil {
		t.Fatalf("GET %s without a credential: %v", logsPath, err)
	} else {
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("GET %s without a credential = %d, want 401", logsPath, resp.StatusCode)
		}
	}
	var served struct {
		Entries []struct {
			Line map[string]any `json:"line"`
		} `json:"entries"`
	}
	for deadline := time.Now().Add(3 * time.Second); ; {
		status, text := profileAdminDo(t, env.adminAddr, http.MethodGet, logsPath, "")
		served.Entries = nil
		if err := json.Unmarshal([]byte(text), &served); status != http.StatusOK || err != nil {
			t.Fatalf("GET %s = %d %s (%v), want 200", logsPath, status, text, err)
		}
		kinds := map[any]bool{}
		for _, e := range served.Entries {
			kinds[e.Line["kind"]] = true
		}
		if kinds["logevent"] && kinds["request"] {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the read route never returned both a request and a logevent line: %s", text)
		}
		time.Sleep(20 * time.Millisecond)
	}
	for _, e := range served.Entries {
		if e.Line["actor"] != env.deviceLFDI {
			t.Errorf("the read route returned a line of actor %v for %s", e.Line["actor"], env.deviceLFDI)
		}
	}

	// Run closes the writer after the listener drains. A writer Run left
	// open would still accept a line, so the refusal below is what proves
	// the Close ran.
	if err := env.shutdown(10 * time.Second); err != nil {
		t.Fatal(err)
	}
	if started == nil {
		t.Fatal("Run never started a device log writer")
	}
	if err := started.Append(env.deviceLFDI, devicelog.Entry{Kind: "request"}); !errors.Is(err, devicelog.ErrClosed) {
		t.Errorf("Append after Run returned = %v; want ErrClosed: Run must close the writer", err)
	}

	raw, err := os.ReadFile(filepath.Join(logDir, env.deviceLFDI+".jsonl"))
	if err != nil {
		t.Fatalf("device file: %v", err)
	}
	if strings.Contains(string(raw), "QUERYTOKEN") {
		t.Errorf("a line carries the query text: %s", raw)
	}
	var got []map[string]any
	for _, l := range bytes.Split(bytes.TrimSpace(raw), []byte{'\n'}) {
		var m map[string]any
		if err := json.Unmarshal(l, &m); err != nil {
			t.Fatalf("line %q is not one JSON object: %v", l, err)
		}
		got = append(got, m)
	}
	var sawGet, sawPost, sawEvent bool
	for _, m := range got {
		if m["actor"] != env.deviceLFDI {
			t.Errorf("line %v has actor %v, want %s", m, m["actor"], env.deviceLFDI)
		}
		switch {
		case m["kind"] == "request" && m["method"] == "GET" && m["path"] == "/edev/1":
			sawGet = m["status"] == float64(200) && m["edev"] == "1" && m["target"] == env.deviceLFDI
		case m["kind"] == "request" && m["method"] == "POST" && m["path"] == "/edev/1/lel":
			sawPost = m["status"] == float64(201) && m["target"] == env.deviceLFDI
		case m["kind"] == "logevent":
			sawEvent = m["logEventID"] == float64(1234) && m["edev"] == "1" && m["target"] == env.deviceLFDI && m["details"] == "boot probe"
		}
	}
	if !sawGet || !sawPost || !sawEvent {
		t.Errorf("GET request line %v, POST request line %v, LogEvent line %v; want all three in %s", sawGet, sawPost, sawEvent, raw)
	}
	ents, err := os.ReadDir(logDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range ents {
		if e.Name() != env.deviceLFDI+".jsonl" && e.Name() != "unregistered.jsonl" {
			t.Errorf("unexpected file %s in the log directory", e.Name())
		}
	}
}

func bootWithBindRetryDeviceLog(t *testing.T) (*captureListenerEnv, string) {
	t.Helper()
	var logDir string
	env := bootWithBindRetry(t, func() (*captureListenerEnv, string) {
		e, dir, failure := tryBootDeviceLog(t)
		logDir = dir
		return e, failure
	})
	return env, logDir
}

// tryBootDeviceLog boots server.Run with the device log directory resolved
// and a fixture that registers the probe device as /edev/1, so its lines
// belong to its own file from the first request.
func tryBootDeviceLog(t *testing.T) (*captureListenerEnv, string, string) {
	t.Helper()
	c := newSplitListenerCerts(t)
	logDir := t.TempDir()

	caCert, _, err := certs.LoadCA(c.caFile, c.caKeyFile)
	if err != nil {
		t.Fatalf("LoadCA: %v", err)
	}
	caKey, err := certs.ParseKeyPEM(c.caKeyPEM)
	if err != nil {
		t.Fatalf("ParseKeyPEM(ca): %v", err)
	}
	deviceCertPEM, deviceKeyPEM, err := certs.GenerateDeviceCert(caCert, caKey, certs.DeviceCertOptions{
		DeviceType:  certs.DeviceTypeGeneric,
		HWSerialNum: "devicelog-boot-PROBE",
	})
	if err != nil {
		t.Fatalf("GenerateDeviceCert: %v", err)
	}
	deviceLeaf, err := certs.ParseCertificatePEM(deviceCertPEM)
	if err != nil {
		t.Fatalf("ParseCertificatePEM(device): %v", err)
	}
	deviceLFDI := sepTLS.LFDI(deviceLeaf)
	clientTLSCfg := ccmClientTLSConfig(t, deviceCertPEM, deviceKeyPEM, c.caCertPEM)

	fixturePath := filepath.Join(t.TempDir(), "fixture.yaml")
	fixture := fmt.Sprintf("end_devices:\n  - id: \"1\"\n    sfdi: %q\n    lfdi: %q\n", sepTLS.SFDI(deviceLeaf), deviceLFDI)
	if err := os.WriteFile(fixturePath, []byte(fixture), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{
		Addr:                 c.sep2Probe,
		CertFile:             c.certFile,
		KeyFile:              c.keyFile,
		CAFile:               c.caFile,
		AdminListen:          c.adminProbe,
		AdminKey:             adminTestKey,
		BootFixtureFile:      fixturePath,
		DeviceLogResolvedDir: logDir,
		TZOffset:             -28800,
		TimeQuality:          sep2.TimeQualityNTP,
	}

	ctx, cancel := context.WithCancel(context.Background())
	runErrCh := make(chan error, 1)
	go func() { runErrCh <- server.Run(ctx, cfg, c.svc) }()
	if msg, ok := waitForListenersReady(c.sep2Probe, c.adminProbe, clientTLSCfg, runErrCh); !ok {
		cancel()
		<-runErrCh
		return nil, logDir, fmt.Sprintf("%s (SEP2 %s, admin %s)", msg, c.sep2Probe, c.adminProbe)
	}
	env := &captureListenerEnv{
		sep2Addr:   c.sep2Probe,
		adminAddr:  c.adminProbe,
		cancel:     cancel,
		runErrCh:   runErrCh,
		deviceLFDI: deviceLFDI,
		deviceHTTP: ccmHTTPClient(clientTLSCfg, 5*time.Second),
	}
	t.Cleanup(func() {
		if err := env.shutdown(5 * time.Second); err != nil {
			t.Error(err)
		}
	})
	return env, logDir, ""
}
