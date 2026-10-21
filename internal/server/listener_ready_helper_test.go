package server_test

import (
	"context"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	sepTLS "github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2tls"
	gotls "github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2tls/gotls"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/config"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/server"
)

// A foreign server already on the admin port makes Run's bind fail and also
// answers the readiness probe. The helper must report Run's exit rather than
// treat the foreign answer as ours or wait out the timeout.
func TestWaitForAdminReadyFailsFastWhenRunCannotBind(t *testing.T) {
	c := newSplitListenerCerts(t)

	foreign, err := net.Listen("tcp", c.adminProbe)
	if err != nil {
		t.Fatalf("occupy admin port: %v", err)
	}
	srv := &http.Server{Handler: http.NotFoundHandler(), ReadHeaderTimeout: time.Second}
	go func() { _ = srv.Serve(foreign) }()
	t.Cleanup(func() { _ = srv.Close() })

	cfg := &config.Config{
		Addr:        c.sep2Probe,
		CertFile:    c.certFile,
		KeyFile:     c.keyFile,
		CAFile:      c.caFile,
		AdminListen: c.adminProbe,
		AdminKey:    adminTestKey,
		TimeQuality: sep2.TimeQualityNTP,
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

	start := time.Now()
	msg, ok := waitForAdminReady(c.adminProbe, false, adminReadyTimeout, runErr)
	elapsed := time.Since(start)
	if ok {
		t.Fatal("waitForAdminReady = ready, want failure: the answer came from the foreign server")
	}
	if !strings.Contains(msg, "Run returned") {
		t.Errorf("message = %q, want it to carry Run's exit", msg)
	}
	if elapsed > adminReadyTimeout/2 {
		t.Errorf("took %v, want a fast failure well under %v", elapsed, adminReadyTimeout)
	}
}

// A protocol listener with a slow handshake is still ready. An attempt bound
// shorter than the handshake fails every attempt, whatever the overall
// timeout, which is how a loaded -race run reports a bound listener as
// never ready.
func TestWaitForServerReadyToleratesASlowHandshake(t *testing.T) {
	const handshakeDelay = 400 * time.Millisecond

	c := newSplitListenerCerts(t)
	srvCfg, err := sepTLS.NewCCMServerConfig(c.certFile, c.keyFile, c.caFile)
	if err != nil {
		t.Fatalf("NewCCMServerConfig: %v", err)
	}
	device := newManagementE2EDevice(t, c, "slow-handshake-probe")

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer func() { _ = conn.Close() }()
				time.Sleep(handshakeDelay)
				// The probe's side reports the outcome; this side only serves it.
				_ = gotls.Server(conn, srvCfg).Handshake()
			}()
		}
	}()

	if !waitForServerReady(ln.Addr().String(), adminReadyTimeout, device.tlsConfig) {
		t.Fatalf("waitForServerReady = not ready, want ready: the handshake completes after %v", handshakeDelay)
	}
}

// TestBootWithBindRetryBootsAgainOnALostPort pins the retry that keeps a
// port taken by another test process from failing a boot: two bind
// failures, then a good env, must return that env after exactly three
// boots.
func TestBootWithBindRetryBootsAgainOnALostPort(t *testing.T) {
	boots := 0
	env := bootWithBindRetry(t, func() (*splitListenerEnv, string) {
		boots++
		if boots <= 2 {
			return nil, "server.Run returned before the admin listener answered: listen tcp 127.0.0.1:1: bind: address already in use"
		}
		return &splitListenerEnv{adminAddr: "ok"}, ""
	})
	if env.adminAddr != "ok" || boots != 3 {
		t.Fatalf("env = %+v after %d boots, want the third boot's env after 3", env, boots)
	}
}
