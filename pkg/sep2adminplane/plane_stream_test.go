package sep2adminplane_test

import (
	"bufio"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2admin"
)

// TestCloseStreamsLetsShutdownReturnWithAStreamOpen serves the plane the
// way an embedder does, with CloseStreams registered on Shutdown, and shuts
// down with a stream open and its reader still reading.
func TestCloseStreamsLetsShutdownReturnWithAStreamOpen(t *testing.T) {
	opened := make(chan context.Context, 1)
	cfg := baseConfig()
	cfg.Panels = []sep2admin.Panel{{
		ID:                "bus",
		Label:             "Bus",
		Placement:         sep2admin.ExtensionSlot(1),
		DescriptorVersion: sep2admin.CurrentDescriptorVersion,
		View: func(context.Context) (sep2admin.Descriptor, error) {
			return sep2admin.Descriptor{Version: sep2admin.CurrentDescriptorVersion}, nil
		},
		Stream: &sep2admin.Stream{
			Param: sep2admin.StreamParam{MaxLen: 4, Charset: "a"},
			Open: func(ctx context.Context, _ sep2admin.StreamRequest, _ sep2admin.StreamSendFunc) error {
				opened <- ctx
				return nil
			},
		},
	}}
	cfg.LoopbackBypass = true
	p := newPlane(t, cfg)
	srv := httptest.NewUnstartedServer(p.Handler())
	srv.Config.RegisterOnShutdown(p.CloseStreams)
	srv.Start()
	defer srv.Close()

	req, err := http.NewRequest(http.MethodGet, srv.URL+"/api/ui/panels/bus/stream?param=a", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Host = "localhost"
	req.Header.Set("Authorization", "Bearer "+testKey)
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("stream = %d, want 200", resp.StatusCode)
	}
	openCtx := <-opened
	body := make(chan string, 1)
	go func() {
		var b strings.Builder
		_, _ = bufio.NewReader(resp.Body).WriteTo(&b) // the assertions below read what arrived; an error just ends it
		body <- b.String()
	}()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := srv.Config.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown with a stream open: %v", err)
	}
	if got := <-body; !strings.Contains(got, `"kind":"status","text":"stream closed: server shutting down"`) {
		t.Fatalf("stream body %q, want the shutdown status event", got)
	}
	select {
	case <-openCtx.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("the embedder's stream context was not cancelled")
	}
	p.CloseStreams()
}
