package server_test

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"encoding/xml"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	sepTLS "github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2tls"
	gotls "github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2tls/gotls"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/certs"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/config"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/server"
)

// ccmClientTLSConfig builds the outbound *gotls.Config for a device
// certificate/key pair against caPEM, using core's CCM-8-only client
// constructor. Shared across this package's test files, which all dial a
// server that offers CCM-8 only.
func ccmClientTLSConfig(t *testing.T, certPEM, keyPEM, caPEM []byte) *gotls.Config {
	t.Helper()
	cfg, err := sepTLS.NewCCMClientConfigFromPEM(certPEM, keyPEM, caPEM)
	if err != nil {
		t.Fatalf("NewCCMClientConfigFromPEM: %v", err)
	}
	return cfg
}

// ccmHTTPClient wraps cfg in an *http.Client whose Transport dials through
// core's forked TLS stack (gotls), the only way to reach a CCM-8-only
// server: net/http's own TLSClientConfig field only accepts a *tls.Config,
// which cannot negotiate CCM-8 at all. A zero timeout leaves the client
// unbounded, matching http.Client's own zero-value default.
func ccmHTTPClient(cfg *gotls.Config, timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			DisableKeepAlives: true,
			DialTLSContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				return (&gotls.Dialer{Config: cfg}).DialContext(ctx, network, addr)
			},
		},
	}
}

// noClientCertCCMConfig offers the CCM-8 cipher the protocol listener
// negotiates but carries no client certificate. A stdlib client cannot
// negotiate CCM-8 at all, so a stdlib dial being refused proves only that
// the cipher lists do not overlap, not that client authentication is
// enforced; this config isolates that second property.
func noClientCertCCMConfig() *gotls.Config {
	return &gotls.Config{ //nolint:gosec // test-only: the server enforces RequireAnyClientCert regardless of what the client trusts
		InsecureSkipVerify: true,
		CipherSuites:       []uint16{gotls.TLS_ECDHE_ECDSA_WITH_AES_128_CCM_8},
		MinVersion:         gotls.VersionTLS12,
		MaxVersion:         gotls.VersionTLS12,
	}
}

// TestServerIdentityPopulatedUnderCCM is the regression guard for #1.
//
// Before the fix, server.Run() constructed the router with empty SFDI/LFDI
// strings and derived them from the cert only after - so /sdev returned
// empty <sFDI/> and <lFDI/> elements. This test drives the full Run() flow
// end-to-end and asserts that /sdev returns the expected 12-digit SFDI and
// 40-hex-char LFDI computed from the server cert.
func TestServerIdentityPopulatedUnderCCM(t *testing.T) {
	runServerIdentityTest(t)
}

func runServerIdentityTest(t *testing.T) {
	t.Helper()

	// Generate CA + server cert in a temp dir so we can hand server.Run()
	// file paths the way main.go does.
	dir := t.TempDir()

	caCertPEM, caKeyPEM, err := certs.GenerateCA(certs.CAOptions{
		CommonName: "server-identity Test CA",
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
		CommonName: "server-identity Test Server",
		ValidYears: 1,
	})
	if err != nil {
		t.Fatalf("GenerateServerCert: %v", err)
	}

	deviceCertPEM, deviceKeyPEM, err := certs.GenerateDeviceCert(caCert, caKey, certs.DeviceCertOptions{
		DeviceType:  certs.DeviceTypeGeneric,
		HWSerialNum: "server-identity-TEST",
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

	// Compute the expected SFDI/LFDI from the server leaf - this is what /sdev
	// MUST return when the fix is in place.
	leafBlock, _ := pem.Decode(serverCertPEM)
	if leafBlock == nil {
		t.Fatal("decode server cert PEM")
	}
	serverLeaf, err := x509.ParseCertificate(leafBlock.Bytes)
	if err != nil {
		t.Fatalf("parse server leaf: %v", err)
	}
	wantSFDI := sepTLS.SFDI(serverLeaf)
	wantLFDI := sepTLS.LFDI(serverLeaf)

	// Build a client TLS config we'll reuse for readiness probing and the
	// real request. Server enforces RequireAnyClientCert, so we need a real
	// device cert even for the probe.
	clientTLSCfg := ccmClientTLSConfig(t, deviceCertPEM, deviceKeyPEM, caCertPEM)

	newConfig := func(addr string) *config.Config {
		return &config.Config{
			Addr:        addr,
			CertFile:    certFile,
			KeyFile:     keyFile,
			CAFile:      caFile,
			TZOffset:    -28800,
			DSTOffset:   3600,
			DSTStart:    1583661600,
			DSTEnd:      1604214000,
			TimeQuality: sep2.TimeQualityNTP,
		}
	}
	addr, stop := startRunOnFreePort(t, newConfig, clientTLSCfg)

	// Build a client. The server offers CCM-8 only, so the client dials
	// through the fork; net/http never populates resp.TLS for a connection
	// reached via DialTLSContext (see core's NewCCMClientConfig doc), so the
	// dial hook stashes the negotiated *gotls.Conn for the cipher check
	// below instead.
	var dialedConn *gotls.Conn
	client := &http.Client{
		Timeout: 3 * time.Second,
		Transport: &http.Transport{
			DisableKeepAlives: true,
			DialTLSContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				conn, dialErr := (&gotls.Dialer{Config: clientTLSCfg}).DialContext(ctx, network, addr)
				if dialErr == nil {
					dialedConn = conn.(*gotls.Conn)
				}
				return conn, dialErr
			},
		},
	}

	resp, err := client.Get("https://" + addr + "/sdev")
	if err != nil {
		t.Fatalf("GET /sdev: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /sdev status = %d, want 200", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read /sdev body: %v", err)
	}

	var sdev sep2.SelfDevice
	if err := xml.Unmarshal(body, &sdev); err != nil {
		t.Fatalf("unmarshal SelfDevice: %v\nbody: %s", err, body)
	}

	// The core assertions for #1: SFDI and LFDI must be present and
	// match the values derived from the server's own leaf cert.
	if sdev.SFDI == "" {
		t.Errorf("SelfDevice.SFDI is empty - #1 regression")
	}
	if sdev.LFDI == "" {
		t.Errorf("SelfDevice.LFDI is empty - #1 regression")
	}
	if sdev.SFDI != wantSFDI {
		t.Errorf("SelfDevice.SFDI = %q, want %q (derived from server leaf cert)", sdev.SFDI, wantSFDI)
	}
	if sdev.LFDI != wantLFDI {
		t.Errorf("SelfDevice.LFDI = %q, want %q (derived from server leaf cert)", sdev.LFDI, wantLFDI)
	}

	// Verify TLS negotiated CCM-8, the only suite core's sep2tls package
	// offers, from the dialed connection stashed above.
	if dialedConn == nil {
		t.Fatal("no connection was dialed")
	}
	if got := dialedConn.ConnectionState().CipherSuite; got != gotls.TLS_ECDHE_ECDSA_WITH_AES_128_CCM_8 {
		t.Errorf("cipher = 0x%04x, want CCM-8 (0x%04x)", got, gotls.TLS_ECDHE_ECDSA_WITH_AES_128_CCM_8)
	}

	// Shut the server down and verify it exits cleanly.
	stop()
}

// adminReadyTimeout bounds every listener readiness wait that uses it. Under
// a loaded CI runner the listeners bind well after Run starts, so the bound
// is generous: a ready listener ends the wait at once, and a failed bind
// ends it through Run's exit.
const adminReadyTimeout = 30 * time.Second

// readyProbeTimeout bounds one readiness attempt, TLS handshake included. A
// loaded -race run can take longer than 200ms to finish a CCM handshake,
// and an attempt bound below that never succeeds however long the overall
// wait runs (#857).
const readyProbeTimeout = 5 * time.Second

// pollReady is the one readiness loop behind the helpers below: it calls
// probe until it returns nil, Run exits, or timeout passes, and on failure
// returns a message naming what never became ready and why. runErr may be
// nil when the caller cannot observe Run.
func pollReady(what string, timeout time.Duration, runErr chan error, probe func() error) (string, bool) {
	deadline := time.Now().Add(timeout)
	for {
		if err, done := runExited(runErr); done {
			return fmt.Sprintf("server.Run returned before the %s answered: %v", what, err), false
		}
		err := probe()
		if err == nil {
			return "", true
		}
		if !time.Now().Before(deadline) {
			return fmt.Sprintf("%s never became ready: %v", what, err), false
		}
		time.Sleep(25 * time.Millisecond)
	}
}

// bindAttempts is how many times bootWithBindRetry boots before giving up.
const bindAttempts = 3

// bootWithBindRetry runs boot until it returns an env or fails for a reason
// other than a lost port. mustProbePort cannot see another test process, so
// a port it handed out can be taken before Run binds it; Run then ends with
// a bind error and the boot is repeated on fresh ports (#857). boot returns
// a failure message with a nil env, having stopped Run.
func bootWithBindRetry[E any](t *testing.T, boot func() (*E, string)) *E {
	t.Helper()
	for attempt := 1; ; attempt++ {
		env, failure := boot()
		if env != nil {
			return env
		}
		if attempt == bindAttempts || !strings.Contains(failure, "address already in use") {
			t.Fatal(failure)
		}
		t.Logf("boot attempt %d lost a port to another process, retrying: %s", attempt, failure)
	}
}

// startRunOnFreePort starts server.Run on a probed port, waits until it
// completes a TLS handshake, and returns the address and a stop that cancels
// Run and checks it exits cleanly. Only a lost port is retried (see
// bootWithBindRetry): any other Run error is the failure under test. stop is
// also registered as a cleanup and runs once, so a test may call it early.
func startRunOnFreePort(t *testing.T, newConfig func(addr string) *config.Config, clientTLSCfg *gotls.Config) (string, func()) {
	t.Helper()
	type started struct {
		addr string
		stop func()
	}
	run := bootWithBindRetry(t, func() (*started, string) {
		addr := mustProbePort(t)
		ctx, cancel := context.WithCancel(context.Background())
		runErrCh := make(chan error, 1)
		go func() { runErrCh <- server.Run(ctx, newConfig(addr), nil) }()
		if msg, ok := pollReady("SEP2 listener", adminReadyTimeout, runErrCh, sep2HandshakeProbe(addr, clientTLSCfg)); !ok {
			cancel()
			<-runErrCh
			return nil, fmt.Sprintf("%s (%s)", msg, addr)
		}
		var once sync.Once
		stop := func() {
			once.Do(func() {
				cancel()
				select {
				case err := <-runErrCh:
					if err != nil {
						t.Errorf("server.Run returned error after cancel: %v", err)
					}
				case <-time.After(3 * time.Second):
					t.Error("server.Run did not return within 3s after context cancel")
				}
			})
		}
		return &started{addr, stop}, ""
	})
	t.Cleanup(run.stop)
	return run.addr, run.stop
}

// sep2HandshakeProbe completes a full TLS handshake against addr. A plain
// TCP connect can succeed against a stale TIME_WAIT socket from a sibling
// test even when the new Run failed to bind. Dials through the fork since
// the server offers CCM-8 only.
func sep2HandshakeProbe(addr string, clientTLSCfg *gotls.Config) func() error {
	dialer := &net.Dialer{Timeout: readyProbeTimeout}
	return func() error {
		conn, err := gotls.DialWithDialer(dialer, "tcp", addr, clientTLSCfg.Clone())
		if err != nil {
			return err
		}
		return conn.Close()
	}
}

// waitForServerReady waits for the protocol listener on addr to complete a
// TLS handshake. The caller is responsible for separately observing Run's
// exit channel after this returns false.
func waitForServerReady(addr string, timeout time.Duration, clientTLSCfg *gotls.Config) bool {
	_, ok := pollReady("SEP2 listener", timeout, nil, sep2HandshakeProbe(addr, clientTLSCfg))
	return ok
}

// runExited reports Run's return without consuming it: the value is put
// back so the test's cleanup still sees the exit.
func runExited(runErr chan error) (error, bool) {
	select {
	case err := <-runErr:
		runErr <- err
		return err, true
	default:
		return nil, false
	}
}

// waitForAdminReady polls the admin listener until it answers an HTTP
// request with a status this server can produce. A bare TCP connect can
// race the goroutine that calls Serve, so this requires a real response.
// It stops at once with Run's error if Run returns first (a bind failure),
// instead of waiting out the timeout.
func waitForAdminReady(addr string, useTLS bool, timeout time.Duration, runErr chan error) (string, bool) {
	scheme := "http"
	transport := &http.Transport{DisableKeepAlives: true}
	if useTLS {
		scheme = "https"
		transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
	}
	client := &http.Client{Transport: transport, Timeout: readyProbeTimeout}
	return pollReady("admin listener", timeout, runErr, func() error {
		resp, err := client.Get(scheme + "://" + addr + "/api/certs/ca")
		if err != nil {
			return err
		}
		_ = resp.Body.Close()
		// /api/certs/ca is a registered route, so this server never answers
		// it 404 or 400; those mean a foreign listener holds the port, and
		// Run's bind error is on its way.
		if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusBadRequest {
			return fmt.Errorf("status %d from a listener that is not this server's", resp.StatusCode)
		}
		return nil
	})
}

// waitForListenersReady waits for the protocol listener and then a plain
// HTTP admin listener, so a test never uses either before it is bound. Both
// waits take adminReadyTimeout: Run's exit ends them early, so a tighter
// bound only adds failures under load.
func waitForListenersReady(sep2Addr, adminAddr string, clientTLSCfg *gotls.Config, runErr chan error) (string, bool) {
	if msg, ok := pollReady("SEP2 listener", adminReadyTimeout, runErr, sep2HandshakeProbe(sep2Addr, clientTLSCfg)); !ok {
		return msg, false
	}
	return waitForAdminReady(adminAddr, false, adminReadyTimeout, runErr)
}
