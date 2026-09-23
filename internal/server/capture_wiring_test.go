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

// bootCaptureListener boots server.Run with both listeners bound,
// SEP2_TRAFFIC_CAPTURE-equivalent (cfg.TrafficCapture) permitting capture,
// and SEP2_TRAFFIC_DIR-equivalent (cfg.TrafficDir) set to a fresh temp
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
		Addr:           c.sep2Probe,
		CertFile:       c.certFile,
		KeyFile:        c.keyFile,
		CAFile:         c.caFile,
		AdminListen:    c.adminProbe,
		AdminKey:       adminTestKey,
		TrafficCapture: true,
		TrafficDir:     trafficDir,
		TZOffset:       -28800,
		TimeQuality:    sep2.TimeQualityNTP,
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

	found, body := waitForTrafficClients(t, client, func() *http.Request {
		return adminReq(t, http.MethodGet, "http://"+env.adminAddr+"/api/traffic/clients", true)
	}, env.deviceLFDI)
	if !found {
		t.Errorf("captured clients do not contain the protocol device's LFDI %q within %s; last body seen: %s", env.deviceLFDI, trafficWaitBound, body)
	}
	// The admin listener carries no client certs at all, so the only way
	// admin traffic could appear here is by connection count: exactly one
	// client (the protocol device) must be present.
	if got := strings.Count(body, `"key"`); got != 1 {
		t.Errorf(`clients response has %d "key" entries, want 1 (the protocol device only): %s`, got, body)
	}
}

// trafficWaitBound is how long waitForTrafficClients polls before giving up.
// Store.Record (sep2capture/store.go) only enqueues onto writeCh; a separate
// writeLoop goroutine does the actual index update Clients() (and this
// route) reads, so a request that reads the endpoint immediately after the
// protocol GET races that goroutine rather than waiting for it.
const trafficWaitBound = 2 * time.Second

// waitForTrafficClients polls GET /api/traffic/clients, via req (rebuilt
// each attempt: an *http.Request is used at most once), until wantLFDI
// appears in the response body or trafficWaitBound elapses. On a miss it
// still returns the last body the endpoint actually gave, so the failure
// names what was seen instead of just that nothing was found.
func waitForTrafficClients(t *testing.T, client *http.Client, req func() *http.Request, wantLFDI string) (found bool, lastBody string) {
	t.Helper()
	deadline := time.Now().Add(trafficWaitBound)
	for {
		resp, err := client.Do(req())
		if err != nil {
			t.Fatalf("GET /api/traffic/clients: %v", err)
		}
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		lastBody = string(body)
		if strings.Contains(lastBody, wantLFDI) {
			return true, lastBody
		}
		if time.Now().After(deadline) {
			return false, lastBody
		}
		time.Sleep(20 * time.Millisecond)
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

// bootWithCaptureBootLog boots server.Run with cfg (Addr/CertFile/KeyFile/
// CAFile already required by the caller) and returns the boot log
// accumulated up to whichever of wantSubstrings appears first, or up to a
// 3s deadline. Shared by the three #628 fix round 1 boot-line tests below
// (not permitted, permitted but no directory, and permitted with a refused
// directory), which differ only in cfg's capture fields and which log line
// they wait for.
func bootWithCaptureBootLog(t *testing.T, cfg *config.Config, waitFor string) string {
	t.Helper()

	// server.Run logs from its own goroutine while this test polls the
	// buffer from the main one; a bare bytes.Buffer is not safe for that
	// (the race detector catches log.Print's concurrent Write racing this
	// test's String() reads), so every access goes through buf's mutex.
	buf := &syncBuffer{}
	prev := log.Writer()
	log.SetOutput(io.MultiWriter(prev, buf))
	t.Cleanup(func() { log.SetOutput(prev) })

	ctx, cancel := context.WithCancel(context.Background())
	runErrCh := make(chan error, 1)
	go func() { runErrCh <- server.Run(ctx, cfg, nil) }()

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && !strings.Contains(buf.String(), waitFor) {
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

	return buf.String()
}

// TestRunLogsBootLineWhenTrafficCaptureNotSet is #628 fix round 1 item 1's
// not-permitted state: TrafficCapture unset (false), with neither TrafficDir
// nor DataDir set either, is capture off with one boot line saying
// specifically that the permit is unset (not the neighbouring "permitted but
// no directory" message, which also mentions SEP2_TRAFFIC_CAPTURE as part of
// naming both directory variables - the assertion checks the full phrase,
// not a bare substring, so the two states cannot pass for each other).
//
// Mutant: replace `if !cfg.TrafficCapture {` with `if false {`. Measured RED
// both here and on the sibling test below, then reverted (git status
// porcelain clean): with neither TrafficDir nor DataDir set, falling
// through to the "permitted but no directory" branch still names
// SEP2_TRAFFIC_CAPTURE as part of naming both directory variables, but not
// with the exact "is not set" phrase this test requires.
func TestRunLogsBootLineWhenTrafficCaptureNotSet(t *testing.T) {
	c := newSplitListenerCerts(t)
	cfg := &config.Config{
		Addr:        c.sep2Probe,
		CertFile:    c.certFile,
		KeyFile:     c.keyFile,
		CAFile:      c.caFile,
		TZOffset:    -28800,
		TimeQuality: sep2.TimeQualityNTP,
	}

	got := bootWithCaptureBootLog(t, cfg, "traffic capture disabled")
	if !strings.Contains(got, "traffic capture disabled: SEP2_TRAFFIC_CAPTURE is not set") {
		t.Errorf("boot log does not carry the not-permitted line verbatim:\n%s", got)
	}
}

// TestRunLogsBootLineWhenTrafficCaptureEnvRejected is #628 fix round 2's
// silent-failure LOW: an operator who set SEP2_TRAFFIC_CAPTURE to something
// other than the exact literal "true" (a near-miss like "TRUE" or "1", per
// the permit's strict comparison in cmd/sep2server/main.go) gets told which
// value was rejected, not the same "is not set" line a truly-unset variable
// gets, so they look at the value instead of an env-injection gap that is
// not the actual problem.
//
// Mutant: revert the boot-line branch to always print "is not set"
// regardless of TrafficCaptureEnv. This test goes RED because the rejected
// value never appears in the log.
func TestRunLogsBootLineWhenTrafficCaptureEnvRejected(t *testing.T) {
	c := newSplitListenerCerts(t)
	cfg := &config.Config{
		Addr:              c.sep2Probe,
		CertFile:          c.certFile,
		KeyFile:           c.keyFile,
		CAFile:            c.caFile,
		TrafficCapture:    false, // what configFromEnv's == "true" comparison yields for "TRUE"
		TrafficCaptureEnv: "TRUE",
		TZOffset:          -28800,
		TimeQuality:       sep2.TimeQualityNTP,
	}

	got := bootWithCaptureBootLog(t, cfg, "traffic capture disabled")
	if !strings.Contains(got, `traffic capture disabled: SEP2_TRAFFIC_CAPTURE="TRUE" is not "true"`) {
		t.Errorf("boot log does not name the rejected SEP2_TRAFFIC_CAPTURE value:\n%s", got)
	}
	if strings.Contains(got, "SEP2_TRAFFIC_CAPTURE is not set") {
		t.Errorf("boot log used the unset-variable line for a set-but-rejected value:\n%s", got)
	}
}

// TestRunDataDirAloneDoesNotEnableCapture is #628 fix round 1's decisive
// reproduction of the defect both the silent-failure and security lanes
// measured: "with SEP2_DATA_DIR set, SEP2_TRAFFIC_DIR unset and no admin
// listener, <data dir>/traffic was created with the .sep2capture marker, and
// 0 boot log lines contained 'traffic capture'." DataDir alone (no
// TrafficCapture) must now leave capture off and create no traffic
// directory at all - not merely log a disabled line, since EffectiveTrafficDir
// still resolves <DataDir>/traffic whenever DataDir is set, and the pre-fix
// bug was exactly that resolving a directory was treated as "on".
//
// Mutant: the same `if !cfg.TrafficCapture {` -> `if false {` as the
// sibling test above. This test goes RED: with DataDir set, the mutant
// falls through the disabled branches straight into NewStore, and
// <DataDir>/traffic is created (an existing deployment upgrades to
// recording, not to "off changes nothing").
func TestRunDataDirAloneDoesNotEnableCapture(t *testing.T) {
	c := newSplitListenerCerts(t)
	dataDir := t.TempDir()
	cfg := &config.Config{
		Addr:        c.sep2Probe,
		CertFile:    c.certFile,
		KeyFile:     c.keyFile,
		CAFile:      c.caFile,
		DataDir:     dataDir,
		TZOffset:    -28800,
		TimeQuality: sep2.TimeQualityNTP,
	}

	got := bootWithCaptureBootLog(t, cfg, "traffic capture disabled")
	if !strings.Contains(got, "traffic capture disabled: SEP2_TRAFFIC_CAPTURE is not set") {
		t.Errorf("boot log does not carry the not-permitted line with DataDir set alone:\n%s", got)
	}
	if _, err := os.Stat(dataDir + "/traffic"); !os.IsNotExist(err) {
		t.Errorf("os.Stat(%s/traffic) = %v, want IsNotExist: DataDir alone must create no traffic directory", dataDir, err)
	}
}

// TestRunLogsBootLineWhenPermittedButNoDirectory is item 1's second
// disabled state: TrafficCapture true but neither TrafficDir nor DataDir
// resolves a directory (EffectiveTrafficDir returns ""), so capture stays
// off with a boot line naming both directory variables.
//
// Mutant: drop the `else if trafficDir := cfg.EffectiveTrafficDir(); trafficDir
// == ""` branch (fold it into the permit check, or always treat an empty
// directory as sep2capture.NewStore's own refusal). This test goes RED
// because the boot log then never names SEP2_TRAFFIC_DIR or SEP2_DATA_DIR
// (a NewStore call with Dir: "" errors with a different message that does
// not mention either variable).
func TestRunLogsBootLineWhenPermittedButNoDirectory(t *testing.T) {
	c := newSplitListenerCerts(t)
	cfg := &config.Config{
		Addr:           c.sep2Probe,
		CertFile:       c.certFile,
		KeyFile:        c.keyFile,
		CAFile:         c.caFile,
		TrafficCapture: true,
		TZOffset:       -28800,
		TimeQuality:    sep2.TimeQualityNTP,
	}

	got := bootWithCaptureBootLog(t, cfg, "traffic capture disabled")
	if !strings.Contains(got, "SEP2_TRAFFIC_DIR") || !strings.Contains(got, "SEP2_DATA_DIR") {
		t.Errorf("boot log does not name both directory variables when permitted with neither set:\n%s", got)
	}
}

// TestRunLogsBootLineWhenPermittedAndRecording is item 1's enabled state:
// TrafficCapture true with a directory that resolves logs one line naming
// both that capture is on and the directory.
//
// Mutant: delete the `log.Printf("traffic capture enabled: ...")` call in
// the success branch. This test goes RED because the boot log never
// contains "traffic capture enabled" or the directory.
func TestRunLogsBootLineWhenPermittedAndRecording(t *testing.T) {
	c := newSplitListenerCerts(t)
	trafficDir := t.TempDir()
	cfg := &config.Config{
		Addr:           c.sep2Probe,
		CertFile:       c.certFile,
		KeyFile:        c.keyFile,
		CAFile:         c.caFile,
		TrafficCapture: true,
		TrafficDir:     trafficDir,
		TZOffset:       -28800,
		TimeQuality:    sep2.TimeQualityNTP,
	}

	got := bootWithCaptureBootLog(t, cfg, "traffic capture enabled")
	if !strings.Contains(got, "traffic capture enabled") || !strings.Contains(got, trafficDir) {
		t.Errorf("boot log does not name capture on and the directory when permitted and resolved:\n%s", got)
	}
}

// TestRunLogsBootLineWhenDirectoryRefused is #628 fix round 1 item 4, the
// coverage lane's MEDIUM 2: the refused-directory branch
// (log.Printf("traffic capture disabled: %v", storeErr)) had no test, only
// its neighbouring "neither variable set" branch did. Seeding the traffic
// directory with a file sep2capture's guarded reset does not recognize
// (reset.go) makes NewStore refuse it; capture must stay off and the
// server must still start rather than failing.
//
// Mutant (server.go): `return fmt.Errorf("traffic capture: %w", storeErr)`
// in place of the log.Printf. This test goes RED: server.Run returns that
// error instead of nil, and the deadline loop above never sees the
// "traffic capture disabled" line it is waiting for.
func TestRunLogsBootLineWhenDirectoryRefused(t *testing.T) {
	c := newSplitListenerCerts(t)
	trafficDir := t.TempDir()
	if err := os.WriteFile(trafficDir+"/notes.txt", []byte("not ours"), 0o600); err != nil {
		t.Fatalf("seed refused directory: %v", err)
	}
	cfg := &config.Config{
		Addr:           c.sep2Probe,
		CertFile:       c.certFile,
		KeyFile:        c.keyFile,
		CAFile:         c.caFile,
		TrafficCapture: true,
		TrafficDir:     trafficDir,
		TZOffset:       -28800,
		TimeQuality:    sep2.TimeQualityNTP,
	}

	got := bootWithCaptureBootLog(t, cfg, "traffic capture disabled")
	if !strings.Contains(got, "traffic capture disabled") || !strings.Contains(got, "unrecognized entry") {
		t.Errorf("boot log does not name the reset refusal for a directory holding a foreign file:\n%s", got)
	}
}

// syncBuffer wraps bytes.Buffer with a mutex so it is safe as a log.Logger
// output target read concurrently by a test goroutine: log.Logger itself
// serializes each Write, but that gives no ordering guarantee against a
// reader's own String() call on the same buffer.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
}
