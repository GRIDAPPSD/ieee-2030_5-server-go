package adminplane

import (
	"bufio"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

func streamTestHandler(t *testing.T, done <-chan struct{}) *DashboardHandler {
	t.Helper()
	return NewDashboardHandler(dashboardTestStores(t, memory.NewEndDeviceStore()), "TLS").WithStreamsDone(done)
}

// nextDataFrame reads lines until a "data: " frame arrives or the deadline
// passes.
func nextDataFrame(t *testing.T, br *bufio.Reader, within time.Duration) bool {
	t.Helper()
	got := make(chan bool, 1)
	go func() {
		for {
			line, err := br.ReadString('\n')
			if err != nil {
				got <- false
				return
			}
			if strings.HasPrefix(line, "data: ") {
				got <- true
				return
			}
		}
	}()
	select {
	case ok := <-got:
		return ok
	case <-time.After(within):
		return false
	}
}

func TestDashboardSSE_EndsWhenStreamsDoneCloses(t *testing.T) {
	done := make(chan struct{})
	srv := httptest.NewServer(streamTestHandler(t, done).handlerForTest())
	defer srv.Close()

	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	br := bufio.NewReader(resp.Body)
	if !nextDataFrame(t, br, 5*time.Second) {
		t.Fatal("no initial frame")
	}

	close(done)
	ended := make(chan error, 1)
	go func() {
		_, err := io.Copy(io.Discard, br)
		ended <- err
	}()
	select {
	case err := <-ended:
		if err != nil {
			t.Fatalf("stream ended with %v, want a clean end", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("stream still open 2s after StreamsDone closed")
	}
}

func TestDashboardSSE_KeepsDeliveringPastServerWriteTimeout(t *testing.T) {
	oldInterval, oldDeadline := dashboardStreamInterval, dashboardStreamWriteDeadline
	dashboardStreamInterval, dashboardStreamWriteDeadline = 100*time.Millisecond, 5*time.Second
	t.Cleanup(func() { dashboardStreamInterval, dashboardStreamWriteDeadline = oldInterval, oldDeadline })

	srv := httptest.NewUnstartedServer(streamTestHandler(t, nil).handlerForTest())
	srv.Config.WriteTimeout = 400 * time.Millisecond
	srv.Start()
	defer srv.Close()

	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	br := bufio.NewReader(resp.Body)

	start := time.Now()
	frames := 0
	for time.Since(start) < 1200*time.Millisecond {
		if !nextDataFrame(t, br, 2*time.Second) {
			t.Fatalf("stream cut after %d frames at %v, past WriteTimeout 400ms it must keep delivering", frames, time.Since(start))
		}
		frames++
	}
	if frames < 5 {
		t.Errorf("frames = %d over 1.2s at 100ms cadence, want at least 5", frames)
	}
}

// handlerForTest serves only the SSE route.
func (d *DashboardHandler) handlerForTest() http.Handler {
	return http.HandlerFunc(d.handleSSE)
}
