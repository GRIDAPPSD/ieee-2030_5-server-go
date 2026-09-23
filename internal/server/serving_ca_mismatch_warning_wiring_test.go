package server_test

// #638 fix round 3 item 5: servingCAMismatchWarning, the pure function, was
// tested directly (serving_ca_mismatch_warning_test.go), but its call site
// in server.Run was not. The coverage lane proved the gap by mutation:
// replacing svc.ServingCA() with svc.DeviceCA() at the call site passed the
// suite, and deleting the call site outright also passed. This drives
// server.Run itself and asserts the warning reaches the boot log, so the
// wiring - not just the function - is pinned.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/certs"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/config"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/handler"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/server"
)

func TestRunLogsServingCAMismatchWarning(t *testing.T) {
	dir := t.TempDir()

	servingCACertPEM, _, err := certs.GenerateCA(certs.CAOptions{CommonName: "638 Item5 Serving CA", ValidYears: 1})
	if err != nil {
		t.Fatalf("GenerateCA(serving): %v", err)
	}
	servingCACert, err := certs.ParseCertificatePEM(servingCACertPEM)
	if err != nil {
		t.Fatalf("parse serving CA: %v", err)
	}

	otherCACertPEM, otherCAKeyPEM, err := certs.GenerateCA(certs.CAOptions{CommonName: "638 Item5 Other CA", ValidYears: 1})
	if err != nil {
		t.Fatalf("GenerateCA(other): %v", err)
	}
	otherCACert, err := certs.ParseCertificatePEM(otherCACertPEM)
	if err != nil {
		t.Fatalf("parse other CA: %v", err)
	}
	otherCAKey, err := certs.ParseKeyPEM(otherCAKeyPEM)
	if err != nil {
		t.Fatalf("parse other CA key: %v", err)
	}

	// The server's own leaf is signed by otherCACert, not by the CA svc
	// advertises as the serving CA below - the exact shape the warning
	// exists to catch.
	leafPEM, leafKeyPEM, err := certs.GenerateServerCert(otherCACert, otherCAKey, certs.ServerCertOptions{
		Hosts: []string{"127.0.0.1", "localhost"}, CommonName: "638 Item5 leaf", ValidYears: 1,
	})
	if err != nil {
		t.Fatalf("GenerateServerCert: %v", err)
	}

	write := func(name string, data []byte) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, data, 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
		return p
	}
	certFile := write("leaf.pem", leafPEM)
	keyFile := write("leaf-key.pem", leafKeyPEM)
	// The protocol listener's own trust anchor is irrelevant to this test
	// (no client dials in), so it reuses the mismatched leaf's own CA.
	caFile := write("ca.pem", otherCACertPEM)

	// The device role is given otherCACert - the CA that actually signed
	// the leaf - on purpose: if the call site under test ever compared the
	// leaf against DeviceCA() instead of ServingCA(), that comparison would
	// wrongly MATCH and swallow the warning, so this construction is what
	// lets the wiring test (not just "a warning appears") catch that
	// specific mutation rather than being unable to discriminate it.
	svc := handler.NewAdminCertServiceWithCAs(servingCACert, nil, servingCACertPEM, otherCACert, nil)

	sep2Addr := mustProbePort(t)
	cfg := &config.Config{
		Addr:        sep2Addr,
		CertFile:    certFile,
		KeyFile:     keyFile,
		CAFile:      caFile,
		TZOffset:    -28800,
		TimeQuality: sep2.TimeQualityNTP,
	}

	buf := teeLogOutput(t)

	ctx, cancel := context.WithCancel(context.Background())
	runErrCh := make(chan error, 1)
	go func() { runErrCh <- server.Run(ctx, cfg, svc) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-runErrCh:
		case <-time.After(3 * time.Second):
			t.Error("server.Run did not exit within 3s after cancel")
		}
	})

	const want = "is not signed by the advertised serving CA"
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(buf.String(), want) {
			break
		}
		select {
		case err := <-runErrCh:
			t.Fatalf("server.Run exited during boot: %v", err)
		default:
		}
		time.Sleep(25 * time.Millisecond)
	}

	if !strings.Contains(buf.String(), want) {
		t.Fatalf("boot log never carried the serving-CA mismatch warning; the call site in server.Run is not wired (or not reachable):\n%s", buf.String())
	}
}
