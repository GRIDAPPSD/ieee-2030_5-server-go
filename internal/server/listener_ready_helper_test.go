package server_test

import (
	"context"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
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
