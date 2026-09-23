package sep2server

import (
	"context"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2capture"
)

// TestConfigCaptureNilLeavesListenerUnchanged is #611 PR 5's capture-off
// acceptance: Config.Capture's zero value (nil) must never wrap the
// protocol listener New binds. reflect.TypeOf is the consumer's own view of
// the listener Server.Run serves, not a claim about Attach's internals.
func TestConfigCaptureNilLeavesListenerUnchanged(t *testing.T) {
	t.Parallel()
	material := writeTLSMaterial(t)

	srv, err := New(Config{
		Addr:     "127.0.0.1:0",
		CertFile: material.certFile,
		KeyFile:  material.keyFile,
		CAFile:   material.caFile,
		Auth:     DefaultAuthPolicy(),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = srv.listener.Close() })

	if got := reflect.TypeOf(srv.listener).String(); strings.Contains(got, "sep2capture") {
		t.Errorf("listener type = %q with Config.Capture nil; capture off must not wrap the listener", got)
	}
}

// TestConfigCaptureAttachesRecordingListener is the capture-on counterpart:
// a non-nil Recorder must produce a wrapped listener, proving New's guard
// runs both ways rather than always skipping.
func TestConfigCaptureAttachesRecordingListener(t *testing.T) {
	t.Parallel()
	material := writeTLSMaterial(t)
	rec := sep2capture.NewRecorder(sep2capture.NewMemorySink(), nil)
	t.Cleanup(func() { _ = rec.Close(context.Background()) })

	srv, err := New(Config{
		Addr:     "127.0.0.1:0",
		CertFile: material.certFile,
		KeyFile:  material.keyFile,
		CAFile:   material.caFile,
		Auth:     DefaultAuthPolicy(),
		Capture:  rec,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = srv.listener.Close() })

	if got := reflect.TypeOf(srv.listener).String(); !strings.Contains(got, "sep2capture") {
		t.Errorf("listener type = %q with Config.Capture set; want a sep2capture-wrapped listener", got)
	}
}

// TestCaptureRecordsRealClientThroughRunBothModes is #611 PR 5's Q7 item 5
// acceptance: "capture on serves a real client through Run in both modes."
// A real mTLS client drives GET /dcap through Server.Run, in both GCM and
// CCM-8, and the captured exchange must carry the client's identity (the
// design's central risk: a naive wrapper breaks r.TLS, see the traffic-tab
// design "A finding the brief did not carry").
func TestCaptureRecordsRealClientThroughRunBothModes(t *testing.T) {
	for _, enableCCM := range []bool{false, true} {
		mode := "GCM"
		if enableCCM {
			mode = "CCM"
		}
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			material := writeTLSMaterial(t)
			sink := sep2capture.NewMemorySink()
			rec := sep2capture.NewRecorder(sink, nil)

			srv, err := New(Config{
				Addr:      "127.0.0.1:0",
				CertFile:  material.certFile,
				KeyFile:   material.keyFile,
				CAFile:    material.caFile,
				Auth:      DefaultAuthPolicy(),
				EnableCCM: enableCCM,
				Capture:   rec,
			})
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			addr := srv.Addr()

			ctx, cancel := context.WithCancel(context.Background())
			runDone := make(chan error, 1)
			go func() { runDone <- srv.Run(ctx) }()

			client := &http.Client{
				Timeout:   5 * time.Second,
				Transport: &http.Transport{TLSClientConfig: material.clientTLS},
			}
			status := getWithRetry(t, client, "https://"+addr+"/dcap")
			if status != http.StatusOK {
				t.Fatalf("GET /dcap over mTLS (%s): status %d, want 200", mode, status)
			}

			cancel()
			select {
			case runErr := <-runDone:
				if runErr != nil {
					t.Errorf("Run returned %v after cancel (%s)", runErr, mode)
				}
			case <-time.After(5 * time.Second):
				t.Fatalf("Run did not return within 5s of cancellation (%s)", mode)
			}
			if cerr := rec.Close(context.Background()); cerr != nil {
				t.Errorf("Recorder.Close (%s): %v", mode, cerr)
			}

			var found bool
			for _, ex := range sink.All() {
				if ex.Mark != sep2capture.MarkHandled || !strings.Contains(string(ex.Request.Bytes), "GET /dcap") {
					continue
				}
				found = true
				if ex.ClientLFDI == "" {
					t.Errorf("captured exchange (%s) has no ClientLFDI: identity was not preserved", mode)
				}
			}
			if !found {
				t.Errorf("no captured GET /dcap exchange in %d recorded (mode=%s)", len(sink.All()), mode)
			}
		})
	}
}
