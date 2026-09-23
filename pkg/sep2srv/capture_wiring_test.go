package sep2srv_test

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	sepTLS "github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2tls"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2capture"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv"
)

// TestOptionsCaptureNilBehavesAsBefore is the capture-off acceptance for
// the sep2srv embeddable (#611 PR 5, "capture off leaves both embeddables'
// listeners and routes unchanged"): a real mTLS client still gets routed
// identically with Options.Capture at its nil zero value.
func TestOptionsCaptureNilBehavesAsBefore(t *testing.T) {
	t.Parallel()
	certs := newTestCertSet(t)

	srv, err := sep2srv.New(sep2srv.Options{
		Addr:     "127.0.0.1:0",
		CertFile: certs.serverCert,
		KeyFile:  certs.serverKey,
		CAFile:   certs.caFile,
	}, echoHandler)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	addr := srv.Addr()

	ctx, cancel := context.WithCancel(context.Background())
	runDone := make(chan error, 1)
	go func() { runDone <- srv.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		<-runDone
	})

	waitForDial(t, addr)

	clientTLSCfg, err := sepTLS.NewClientTLSConfigFromPEM(mustRead(t, certs.deviceCert), mustRead(t, certs.deviceKey), mustRead(t, certs.caFile))
	if err != nil {
		t.Fatalf("NewClientTLSConfigFromPEM: %v", err)
	}
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: clientTLSCfg}}
	resp, err := client.Get("https://" + addr + "/dcap")
	if err != nil {
		t.Fatalf("GET with Capture nil: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200 (Options.Capture nil must not change routing)", resp.StatusCode)
	}
}

// TestOptionsCaptureRecordsRealClientThroughRunBothModes is #611 PR 5's Q7
// item 5 acceptance for the sep2srv embeddable, mirroring the sep2server
// test of the same shape: a real mTLS client through Run, in both cipher
// modes, must be captured with its identity intact.
func TestOptionsCaptureRecordsRealClientThroughRunBothModes(t *testing.T) {
	for _, enableCCM := range []bool{false, true} {
		mode := "GCM"
		if enableCCM {
			mode = "CCM"
		}
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			certs := newTestCertSet(t)
			sink := sep2capture.NewMemorySink()
			rec := sep2capture.NewRecorder(sink, nil)

			srv, err := sep2srv.New(sep2srv.Options{
				Addr:      "127.0.0.1:0",
				CertFile:  certs.serverCert,
				KeyFile:   certs.serverKey,
				CAFile:    certs.caFile,
				EnableCCM: enableCCM,
				Capture:   rec,
			}, echoHandler)
			if err != nil {
				t.Fatalf("New (%s): %v", mode, err)
			}
			addr := srv.Addr()

			ctx, cancel := context.WithCancel(context.Background())
			runDone := make(chan error, 1)
			go func() { runDone <- srv.Run(ctx) }()

			clientTLSCfg, err := sepTLS.NewClientTLSConfigFromPEM(mustRead(t, certs.deviceCert), mustRead(t, certs.deviceKey), mustRead(t, certs.caFile))
			if err != nil {
				t.Fatalf("NewClientTLSConfigFromPEM: %v", err)
			}
			client := &http.Client{Transport: &http.Transport{TLSClientConfig: clientTLSCfg}}
			resp := getWithRetry(t, client, "https://"+addr+"/dcap")
			_ = resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("status (%s) = %d, want 200", mode, resp.StatusCode)
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
