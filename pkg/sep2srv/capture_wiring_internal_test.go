package sep2srv

// #628 fix round 1, coverage lane MEDIUM 1: pkg/sep2server pins "Options.Capture
// nil must not change routing" with an in-package reflect.TypeOf assertion on
// the listener New actually built (sep2server/capture_wiring_test.go). This
// package's own sep2srv_test.go acceptance test can only assert a 200 through a
// real client, which cannot fail on a mutant that always attaches a
// memory-sink Recorder: a wrapped listener still answers 200. This file adds
// the sibling in-package assertion so sep2srv guards the same property the
// same way.

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/certs"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2capture"
)

// internalTLSMaterial mints just enough TLS material for New: a CA and a
// server leaf. No device cert is needed here since this test never dials
// the listener; it only inspects the *Server New returns.
type internalTLSMaterial struct {
	certFile string
	keyFile  string
	caFile   string
}

func writeInternalTLSMaterial(t *testing.T) internalTLSMaterial {
	t.Helper()
	dir := t.TempDir()

	caCertPEM, caKeyPEM, err := certs.GenerateCA(certs.CAOptions{
		CommonName: "sep2srv internal Test CA",
		ValidYears: 1,
	})
	if err != nil {
		t.Fatalf("GenerateCA: %v", err)
	}
	caCert, err := certs.ParseCertificatePEM(caCertPEM)
	if err != nil {
		t.Fatalf("ParseCertificatePEM: %v", err)
	}
	caKey, err := certs.ParseKeyPEM(caKeyPEM)
	if err != nil {
		t.Fatalf("ParseKeyPEM: %v", err)
	}

	serverCertPEM, serverKeyPEM, err := certs.GenerateServerCert(caCert, caKey, certs.ServerCertOptions{
		Hosts:      []string{"127.0.0.1", "localhost"},
		CommonName: "sep2srv internal Test Server",
		ValidYears: 1,
	})
	if err != nil {
		t.Fatalf("GenerateServerCert: %v", err)
	}

	return internalTLSMaterial{
		certFile: writeInternalTempFile(t, dir, "server.pem", serverCertPEM),
		keyFile:  writeInternalTempFile(t, dir, "server-key.pem", serverKeyPEM),
		caFile:   writeInternalTempFile(t, dir, "ca.pem", caCertPEM),
	}
}

func writeInternalTempFile(t *testing.T, dir, name string, data []byte) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	return path
}

// TestOptionsCaptureNilLeavesListenerUnchanged is the sep2srv sibling of
// sep2server's TestConfigCaptureNilLeavesListenerUnchanged (#628 fix round
// 1, coverage lane MEDIUM 1). Mutant: attach a memory-sink Recorder inside
// New whenever opts.Capture is nil (e.g. `if opts.Capture == nil { opts.Capture
// = sep2capture.NewRecorder(sep2capture.NewMemorySink(), nil) }` before the
// existing `if opts.Capture != nil` guard). That mutant leaves every test in
// this package's external sep2srv_test.go green, since a wrapped listener
// still answers 200; this assertion goes RED because the listener's runtime
// type gains "sep2capture" once nil is silently replaced.
func TestOptionsCaptureNilLeavesListenerUnchanged(t *testing.T) {
	t.Parallel()
	material := writeInternalTLSMaterial(t)

	srv, err := New(Options{
		Addr:     "127.0.0.1:0",
		CertFile: material.certFile,
		KeyFile:  material.keyFile,
		CAFile:   material.caFile,
	}, func(Identity) http.Handler {
		return http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = srv.listener.Close() })

	if got := reflect.TypeOf(srv.listener).String(); strings.Contains(got, "sep2capture") {
		t.Errorf("listener type = %q with Options.Capture nil; capture off must not wrap the listener", got)
	}
}

// TestOptionsCaptureAttachesRecordingListener is the capture-on counterpart,
// proving the guard above runs both ways rather than always skipping.
func TestOptionsCaptureAttachesRecordingListener(t *testing.T) {
	t.Parallel()
	material := writeInternalTLSMaterial(t)
	rec := sep2capture.NewRecorder(sep2capture.NewMemorySink(), nil)
	t.Cleanup(func() { _ = rec.Close(context.Background()) })

	srv, err := New(Options{
		Addr:     "127.0.0.1:0",
		CertFile: material.certFile,
		KeyFile:  material.keyFile,
		CAFile:   material.caFile,
		Capture:  rec,
	}, func(Identity) http.Handler {
		return http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = srv.listener.Close() })

	if got := reflect.TypeOf(srv.listener).String(); !strings.Contains(got, "sep2capture") {
		t.Errorf("listener type = %q with Options.Capture set; want it wrapped by sep2capture", got)
	}
}
