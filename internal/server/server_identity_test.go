package server_test

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"encoding/xml"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
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

	// Start the server with a port-pick retry to absorb the port-reuse race
	// when two subtests run back-to-back (ephemeral ports cycle through
	// TIME_WAIT). We pick a port via probe-and-close, hand it to Run, and
	// retry if Run fails to listen.
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

		runErrCh = make(chan error, 1)
		go func(c *config.Config) {
			runErrCh <- server.Run(ctx, c, nil)
		}(cfg)

		// Race readiness against early Run failure. waitForServerReady
		// polls TLS handshake; if Run died first (port already bound),
		// runErrCh fires before the timeout.
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
			t.Fatalf("server did not start accepting TLS on %s after retries (attempt %d)", addr, attempt+1)
		case err := <-runErrCh:
			t.Logf("server.Run start attempt %d failed (%v); retrying with new port", attempt+1, err)
			// Drain the ready goroutine to avoid a leak.
			go func() { <-readyCh }()
		}
	}
	if !started {
		t.Fatalf("server failed to start after %d attempts", startAttempts)
	}

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

// waitForServerReady polls a TLS handshake against addr until it succeeds
// or the timeout elapses. A plain TCP connect can succeed against a stale
// TIME_WAIT socket from a sibling test even when the new Run failed to
// bind, so this checks the full TLS handshake. The caller is responsible
// for separately observing Run's exit channel after this returns false.
// Dials through the fork since the server offers CCM-8 only.
func waitForServerReady(addr string, timeout time.Duration, clientTLSCfg *gotls.Config) bool {
	deadline := time.Now().Add(timeout)
	dialer := &net.Dialer{Timeout: 200 * time.Millisecond}
	probeCfg := clientTLSCfg.Clone()
	for time.Now().Before(deadline) {
		conn, err := gotls.DialWithDialer(dialer, "tcp", addr, probeCfg)
		if err == nil {
			_ = conn.Close()
			return true
		}
		time.Sleep(25 * time.Millisecond)
	}
	return false
}
