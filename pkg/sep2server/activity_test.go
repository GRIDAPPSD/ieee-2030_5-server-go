package sep2server

import (
	"context"
	"net/http"
	"testing"
	"time"

	sepTLS "github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2tls"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/certs"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/activity"
)

// A request over the CCM-8 listener, through DefaultAuthPolicy's identity
// and ACL, is recorded under the client certificate's LFDI.
func TestActivityRecordsCCMRequest(t *testing.T) {
	t.Parallel()
	material := writeTLSMaterial(t)
	leaf, err := certs.ParseCertificatePEM(material.deviceCertPEM)
	if err != nil {
		t.Fatalf("parse device cert: %v", err)
	}
	wantLFDI := sepTLS.LFDI(leaf)

	rec := activity.New()
	cfg := Config{
		Addr:     "127.0.0.1:0",
		CertFile: material.certFile,
		KeyFile:  material.keyFile,
		CAFile:   material.caFile,
		Auth:     DefaultAuthPolicy(),
	}
	cfg.Router.Activity = rec
	srv, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	runDone := make(chan error, 1)
	go func() { runDone <- srv.Run(ctx) }()
	defer func() {
		cancel()
		select {
		case <-runDone:
		case <-time.After(5 * time.Second):
			t.Error("Run did not return within 5s of cancellation")
		}
	}()

	client := ccmHTTPClient(material.clientTLS, 5*time.Second)
	if status := getWithRetry(t, client, "https://"+srv.Addr()+"/dcap"); status != http.StatusOK {
		t.Fatalf("GET /dcap over mTLS: status %d, want 200", status)
	}

	at, n, ok := rec.Last(wantLFDI)
	if !ok || n != 1 {
		t.Fatalf("Last(%s) count = %d ok=%v, want 1 true", wantLFDI, n, ok)
	}
	if time.Since(at) > time.Minute {
		t.Errorf("recorded time %v is not recent", at)
	}
}

// A request the ACL refuses after identity is established (DELETE /dcap is
// 405) is not a device in contact: the same client's GET is recorded and its
// refused DELETE is not counted.
func TestActivityDoesNotCountACLRefusedCCMRequest(t *testing.T) {
	t.Parallel()
	material := writeTLSMaterial(t)
	leaf, err := certs.ParseCertificatePEM(material.deviceCertPEM)
	if err != nil {
		t.Fatalf("parse device cert: %v", err)
	}
	wantLFDI := sepTLS.LFDI(leaf)

	rec := activity.New()
	cfg := Config{
		Addr:     "127.0.0.1:0",
		CertFile: material.certFile,
		KeyFile:  material.keyFile,
		CAFile:   material.caFile,
		Auth:     DefaultAuthPolicy(),
	}
	cfg.Router.Activity = rec
	srv, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	runDone := make(chan error, 1)
	go func() { runDone <- srv.Run(ctx) }()
	defer func() {
		cancel()
		select {
		case <-runDone:
		case <-time.After(5 * time.Second):
			t.Error("Run did not return within 5s of cancellation")
		}
	}()

	client := ccmHTTPClient(material.clientTLS, 5*time.Second)
	url := "https://" + srv.Addr() + "/dcap"
	if status := getWithRetry(t, client, url); status != http.StatusOK {
		t.Fatalf("control: GET /dcap status %d, want 200", status)
	}
	req, err := http.NewRequest(http.MethodDelete, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("DELETE /dcap: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("DELETE /dcap status %d, want 405 from the ACL", resp.StatusCode)
	}
	if _, n, ok := rec.Last(wantLFDI); !ok || n != 1 {
		t.Errorf("count after one GET and one refused DELETE = %d ok=%v, want 1", n, ok)
	}
}
