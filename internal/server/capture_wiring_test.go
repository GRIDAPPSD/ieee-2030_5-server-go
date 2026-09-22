package server_test

// #611 PR 5 wiring: the protocol listener records through Config.Capture,
// and the read-only capture API is mounted on the admin mux behind the
// same auth every other admin route has. These tests drive a real
// server.Run() with both listeners bound, mirroring bootSplitListener in
// server_split_test.go (which this file reuses newSplitListenerCerts and
// mustProbePort from) rather than reinventing cert and port plumbing.

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	sepTLS "github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2tls"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/certs"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/config"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/server"
)

// captureListenerEnv is bootSplitListener's env plus what these tests need
// on top: the traffic directory, a device client to drive protocol traffic,
// and that device's LFDI (the capture client key). runErrCh carries exactly
// one value, so shutdown is the only reader: call it, never read runErrCh
// directly, or a second reader (a t.Cleanup included) blocks forever on an
// already-drained channel.
type captureListenerEnv struct {
	sep2Addr   string
	adminAddr  string
	trafficDir string
	cancel     context.CancelFunc
	runErrCh   chan error
	deviceLFDI string
	deviceHTTP *http.Client

	shutdownOnce sync.Once
	shutdownErr  error
}

// shutdown cancels ctx and waits up to timeout for server.Run to return,
// exactly once: a second call (from a test body and then from t.Cleanup,
// for example) returns the first call's result immediately rather than
// reading runErrCh again.
func (e *captureListenerEnv) shutdown(timeout time.Duration) error {
	e.shutdownOnce.Do(func() {
		e.cancel()
		select {
		case e.shutdownErr = <-e.runErrCh:
		case <-time.After(timeout):
			e.shutdownErr = fmt.Errorf("server.Run did not exit within %s after cancel", timeout)
		}
	})
	return e.shutdownErr
}

// bootCaptureListener boots server.Run with both listeners bound and
// SEP2_TRAFFIC_DIR-equivalent (cfg.TrafficDir) set to a fresh temp
// directory, so capture is on. AdminTLS is left at its default (false,
// plain HTTP): the auth assertions below need no TLS handshake on the
// admin side, only the auth chain.
func bootCaptureListener(t *testing.T) *captureListenerEnv {
	t.Helper()
	c := newSplitListenerCerts(t)
	trafficDir := t.TempDir()

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
		HWSerialNum: "capture-wiring-PROBE",
	})
	if err != nil {
		t.Fatalf("GenerateDeviceCert: %v", err)
	}
	deviceLeaf, err := certs.ParseCertificatePEM(deviceCertPEM)
	if err != nil {
		t.Fatalf("ParseCertificatePEM(device): %v", err)
	}
	clientTLSCfg, err := sepTLS.NewClientTLSConfigFromPEM(deviceCertPEM, deviceKeyPEM, c.caCertPEM)
	if err != nil {
		t.Fatalf("NewClientTLSConfigFromPEM: %v", err)
	}

	cfg := &config.Config{
		Addr:        c.sep2Probe,
		CertFile:    c.certFile,
		KeyFile:     c.keyFile,
		CAFile:      c.caFile,
		AdminListen: c.adminProbe,
		AdminKey:    adminTestKey,
		TrafficDir:  trafficDir,
		TZOffset:    -28800,
		TimeQuality: sep2.TimeQualityNTP,
	}

	ctx, cancel := context.WithCancel(context.Background())
	runErrCh := make(chan error, 1)
	go func() { runErrCh <- server.Run(ctx, cfg, c.svc) }()

	if !waitForServerReady(c.sep2Probe, 3*time.Second, clientTLSCfg) {
		cancel()
		<-runErrCh
		t.Fatalf("SEP2 listener never became ready on %s", c.sep2Probe)
	}

	probeClient := &http.Client{Timeout: 500 * time.Millisecond}
	deadline := time.Now().Add(3 * time.Second)
	ready := false
	for time.Now().Before(deadline) {
		resp, err := probeClient.Get("http://" + c.adminProbe + "/api/certs/ca")
		if err == nil {
			_ = resp.Body.Close()
			ready = true
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if !ready {
		cancel()
		<-runErrCh
		t.Fatalf("admin listener never became ready on %s", c.adminProbe)
	}

	env := &captureListenerEnv{
		sep2Addr:   c.sep2Probe,
		adminAddr:  c.adminProbe,
		trafficDir: trafficDir,
		cancel:     cancel,
		runErrCh:   runErrCh,
		deviceLFDI: sepTLS.LFDI(deviceLeaf),
		deviceHTTP: &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{TLSClientConfig: clientTLSCfg}},
	}
	t.Cleanup(func() {
		if err := env.shutdown(5 * time.Second); err != nil {
			t.Error(err)
		}
	})
	return env
}

// adminReq builds an admin-listener request forced through the real auth
// chain: X-Forwarded-For declines the loopback bypass (Path 0), matching
// server_split_test.go's own pattern, since these tests bind 127.0.0.1.
func adminReq(t *testing.T, method, url string, bearer bool) *http.Request {
	t.Helper()
	req, err := http.NewRequest(method, url, nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("X-Forwarded-For", "203.0.113.5")
	if bearer {
		req.Header.Set("Authorization", "Bearer "+adminTestKey)
	}
	return req
}

// TestRunMountsTrafficUnderAdminMuxWithSameAuth is #611 PR 5's Q7 item 5
// acceptance: "an unauthenticated request to every new route is refused."
// Every capture route answers 401 with no credential and something other
// than 401/404 with a valid Bearer, proving each is mounted behind
// AdminAuthMiddleware rather than left unmounted (which would also read
// as "refused", just for the wrong reason: 404).
func TestRunMountsTrafficUnderAdminMuxWithSameAuth(t *testing.T) {
	t.Parallel()
	env := bootCaptureListener(t)
	client := &http.Client{Timeout: 3 * time.Second}

	routes := []string{
		"/api/traffic/clients",
		"/api/traffic/exchanges?client=x",
		"/api/traffic/exchanges/1",
		"/api/traffic/exchanges/1/request",
		"/api/traffic/exchanges/1/response",
		"/api/traffic/stream",
		"/api/traffic/stats",
	}

	for _, route := range routes {
		t.Run("unauth "+route, func(t *testing.T) {
			resp, err := client.Do(adminReq(t, http.MethodGet, "http://"+env.adminAddr+route, false))
			if err != nil {
				t.Fatalf("GET %s (no auth): %v", route, err)
			}
			_ = resp.Body.Close()
			if resp.StatusCode != http.StatusUnauthorized {
				t.Errorf("status = %d, want 401 (unauthenticated request must be refused)", resp.StatusCode)
			}
		})
		t.Run("authed "+route, func(t *testing.T) {
			req := adminReq(t, http.MethodGet, "http://"+env.adminAddr+route, true)
			if strings.Contains(route, "/stream") {
				req.Header.Set("Accept", "text/event-stream")
			}
			resp, err := client.Do(req)
			if err != nil {
				// The SSE route may still be open at t.Cleanup time; a
				// client-side timeout on a long-lived stream is expected
				// and is not evidence the route is unmounted.
				if strings.Contains(route, "/stream") {
					return
				}
				t.Fatalf("GET %s (Bearer): %v", route, err)
			}
			body, _ := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusUnauthorized {
				t.Errorf("status = 401 with a valid Bearer: same auth as other admin routes is not wired")
			}
			// A 404 on /exchanges/1* is legitimate here (no exchange with
			// that id has been recorded yet, per Store.Exchange's own
			// ErrNotFound contract) and must be told apart from net/http's
			// own "no route matched" miss, which is the actual "not
			// mounted" signal and carries a different body.
			if resp.StatusCode == http.StatusNotFound && strings.Contains(string(body), "page not found") {
				t.Errorf("status = 404 with a valid Bearer and net/http's own miss body %q: route not mounted under the admin mux", body)
			}
		})
	}
}

// TestRunSSEStreamAcceptsOneTimeTicket is the ticket half of Q7 item 5:
// "the SSE route accepts the one-time ticket." A ticket minted through the
// existing /auth/ticket endpoint (the same mechanism the #159 dashboard SSE
// route already relies on, per AdminAuthMiddleware Path C) authenticates a
// GET /api/traffic/stream that carries no Bearer header at all.
func TestRunSSEStreamAcceptsOneTimeTicket(t *testing.T) {
	t.Parallel()
	env := bootCaptureListener(t)
	client := &http.Client{Timeout: 3 * time.Second}

	ticketResp, err := client.Do(adminReq(t, http.MethodPost, "http://"+env.adminAddr+"/auth/ticket", true))
	if err != nil {
		t.Fatalf("POST /auth/ticket: %v", err)
	}
	body, _ := io.ReadAll(ticketResp.Body)
	_ = ticketResp.Body.Close()
	if ticketResp.StatusCode != http.StatusOK {
		t.Fatalf("POST /auth/ticket: status %d body %s", ticketResp.StatusCode, body)
	}
	const marker = `"ticket":"`
	i := strings.Index(string(body), marker)
	if i < 0 {
		t.Fatalf("ticket response %s carries no ticket field", body)
	}
	rest := string(body)[i+len(marker):]
	ticket := rest[:strings.IndexByte(rest, '"')]
	if ticket == "" {
		t.Fatal("minted ticket is empty")
	}

	streamCtx, cancelStream := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancelStream()
	req, _ := http.NewRequestWithContext(streamCtx, http.MethodGet, "http://"+env.adminAddr+"/api/traffic/stream?ticket="+ticket, nil)
	req.Header.Set("X-Forwarded-For", "203.0.113.5")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("GET /api/traffic/stream?ticket=...: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("ticket-authenticated stream status = %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "text/event-stream") {
		t.Errorf("Content-Type = %q, want text/event-stream", ct)
	}
}

// TestRunCapturesProtocolTrafficNotAdminTraffic is Q7 item 5's "the admin
// listener is never wrapped": a request that only ever reaches the admin
// listener must never show up as a captured client or exchange, because
// Config.Capture is passed to sep2server.New alone, never to the admin
// http.Server startAdminServer builds.
func TestRunCapturesProtocolTrafficNotAdminTraffic(t *testing.T) {
	t.Parallel()
	env := bootCaptureListener(t)
	client := &http.Client{Timeout: 3 * time.Second}

	// One protocol-listener request (mTLS, real device identity).
	protoResp, err := env.deviceHTTP.Get("https://" + env.sep2Addr + "/dcap")
	if err != nil {
		t.Fatalf("GET /dcap: %v", err)
	}
	_ = protoResp.Body.Close()

	// Several admin-listener requests, none of which carry a device cert.
	for _, route := range []string{"/api/certs/ca", "/api/certs/device-types", "/login"} {
		resp, err := client.Do(adminReq(t, http.MethodGet, "http://"+env.adminAddr+route, true))
		if err != nil {
			t.Fatalf("GET %s: %v", route, err)
		}
		_ = resp.Body.Close()
	}

	clientsResp, err := client.Do(adminReq(t, http.MethodGet, "http://"+env.adminAddr+"/api/traffic/clients", true))
	if err != nil {
		t.Fatalf("GET /api/traffic/clients: %v", err)
	}
	body, _ := io.ReadAll(clientsResp.Body)
	_ = clientsResp.Body.Close()

	if !strings.Contains(string(body), env.deviceLFDI) {
		t.Errorf("captured clients %s do not contain the protocol device's LFDI %q", body, env.deviceLFDI)
	}
	// The admin listener carries no client certs at all, so the only way
	// admin traffic could appear here is by connection count: exactly one
	// client (the protocol device) must be present.
	if got := strings.Count(string(body), `"key"`); got != 1 {
		t.Errorf(`clients response has %d "key" entries, want 1 (the protocol device only): %s`, got, body)
	}
}

// TestRunClosesCaptureAndEndsStreamOnShutdown is Q7 item 5's "shutdown ends
// streams and closes the store": cancelling Run's context must both return
// Run within a bound (proving Recorder.Close then Store.Close completed)
// and end an open SSE stream rather than holding it past the client's own
// deadline.
func TestRunClosesCaptureAndEndsStreamOnShutdown(t *testing.T) {
	env := bootCaptureListener(t)
	client := &http.Client{}

	streamCtx, cancelStream := context.WithCancel(context.Background())
	defer cancelStream()
	req, _ := http.NewRequestWithContext(streamCtx, http.MethodGet, "http://"+env.adminAddr+"/api/traffic/stream", nil)
	req.Header.Set("X-Forwarded-For", "203.0.113.5")
	req.Header.Set("Authorization", "Bearer "+adminTestKey)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("GET /api/traffic/stream: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("stream status = %d, want 200", resp.StatusCode)
	}

	readDone := make(chan error, 1)
	go func() {
		_, readErr := io.Copy(io.Discard, resp.Body)
		readDone <- readErr
	}()

	if runErr := env.shutdown(10 * time.Second); runErr != nil {
		t.Fatalf("%v (capture Close likely hung)", runErr)
	}

	select {
	case <-readDone:
		// Body read ended (server closed the connection): the stream did
		// not outlive shutdown.
	case <-time.After(3 * time.Second):
		t.Error("SSE stream body read did not end within 3s of shutdown")
	}

	entries, err := os.ReadDir(env.trafficDir)
	if err != nil {
		t.Fatalf("ReadDir(%s): %v", env.trafficDir, err)
	}
	if len(entries) == 0 {
		t.Errorf("traffic directory %s is empty after Store.Close; want the marker file and at least one segment", env.trafficDir)
	}
}

// TestRunLogsBootLineWhenTrafficDirUnset is Q7 item 5's directory
// resolution: "Run builds the capture when a directory resolves ..., else
// capture is off with one boot line naming both" (SEP2_TRAFFIC_DIR and
// SEP2_DATA_DIR). No admin listener is needed for this one; only the
// protocol listener and the boot log matter.
func TestRunLogsBootLineWhenTrafficDirUnset(t *testing.T) {
	c := newSplitListenerCerts(t)

	var buf bytes.Buffer
	prev := log.Writer()
	log.SetOutput(io.MultiWriter(prev, &buf))
	t.Cleanup(func() { log.SetOutput(prev) })

	cfg := &config.Config{
		Addr:        c.sep2Probe,
		CertFile:    c.certFile,
		KeyFile:     c.keyFile,
		CAFile:      c.caFile,
		TZOffset:    -28800,
		TimeQuality: sep2.TimeQualityNTP,
	}

	ctx, cancel := context.WithCancel(context.Background())
	runErrCh := make(chan error, 1)
	go func() { runErrCh <- server.Run(ctx, cfg, nil) }()

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && !strings.Contains(buf.String(), "traffic capture disabled") {
		time.Sleep(25 * time.Millisecond)
	}

	cancel()
	select {
	case runErr := <-runErrCh:
		if runErr != nil {
			t.Errorf("server.Run returned %v after cancel, want nil", runErr)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("server.Run did not exit within 5s after cancel")
	}

	got := buf.String()
	if !strings.Contains(got, "SEP2_TRAFFIC_DIR") || !strings.Contains(got, "SEP2_DATA_DIR") {
		t.Errorf("boot log does not name both variables when neither is set:\n%s", got)
	}
}
