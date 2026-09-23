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

			waitForCapturedGET(t, sink, mode)
		})
	}
}

// waitForCapturedGET polls sink for a MarkHandled GET /dcap exchange with a
// preserved ClientLFDI, up to captureWaitBound, instead of reading sink.All
// once: Recorder.Close(context.Background()) already blocks until every
// finished exchange has reached Sink.Record (recorder.go's Close doc), so
// this bound is defense against a dispatch delay this package's contract
// does not actually leave open, not a fix for one. What it changes is the
// failure: on a miss it names the Mark, HandlerRuns and request bytes of
// every exchange sink held, so the next failure shows what the one recorded
// exchange actually was instead of just its count.
const captureWaitBound = 2 * time.Second

func waitForCapturedGET(t *testing.T, sink *sep2capture.MemorySink, mode string) {
	t.Helper()
	deadline := time.Now().Add(captureWaitBound)
	var seen []sep2capture.Exchange
	for {
		seen = sink.All()
		for _, ex := range seen {
			if ex.Mark != sep2capture.MarkHandled || !strings.Contains(string(ex.Request.Bytes), "GET /dcap") {
				continue
			}
			if ex.ClientLFDI == "" {
				t.Errorf("captured exchange (%s) has no ClientLFDI: identity was not preserved", mode)
			}
			return
		}
		if time.Now().After(deadline) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Errorf("no captured GET /dcap exchange within %s (mode=%s); %d recorded:", captureWaitBound, mode, len(seen))
	for _, ex := range seen {
		t.Errorf("  id=%d conn=%d mark=%s handlerRuns=%d lfdi=%q err=%q reqLen=%d req=%q",
			ex.ID, ex.ConnID, ex.Mark, ex.HandlerRuns, ex.ClientLFDI, ex.Error, len(ex.Request.Bytes), capped(string(ex.Request.Bytes), 200))
	}
}

// capped truncates s to at most n bytes for a diagnostic line, marking the
// cut so a truncated print is never mistaken for the whole value.
func capped(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "...(truncated)"
}
