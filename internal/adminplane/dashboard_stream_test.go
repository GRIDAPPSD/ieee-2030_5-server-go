package adminplane

import (
	"bufio"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
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
	defer func() { _ = resp.Body.Close() }()
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
	defer func() { _ = resp.Body.Close() }()
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

// stallWriter is a ResponseWriter whose Write, after the first, blocks until
// the write deadline set through SetWriteDeadline passes: a reader that
// stopped reading.
type stallWriter struct {
	hdr    http.Header
	mu     sync.Mutex
	dl     time.Time
	writes int
}

func (s *stallWriter) Header() http.Header { return s.hdr }
func (s *stallWriter) WriteHeader(int)     {}
func (s *stallWriter) Flush()              {}

func (s *stallWriter) SetWriteDeadline(t time.Time) error {
	s.mu.Lock()
	s.dl = t
	s.mu.Unlock()
	return nil
}

func (s *stallWriter) Write(b []byte) (int, error) {
	s.mu.Lock()
	s.writes++
	first := s.writes == 1
	s.mu.Unlock()
	if first {
		return len(b), nil
	}
	for {
		s.mu.Lock()
		dl := s.dl
		s.mu.Unlock()
		if !dl.IsZero() && !time.Now().Before(dl) {
			return 0, os.ErrDeadlineExceeded
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func (s *stallWriter) blocked() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.writes >= 2
}

func TestDashboardSSE_StreamsDoneCutsAStalledWrite(t *testing.T) {
	oldInterval, oldGrace := dashboardStreamInterval, panelStreamShutdownGrace
	dashboardStreamInterval, panelStreamShutdownGrace = 20*time.Millisecond, 200*time.Millisecond
	t.Cleanup(func() { dashboardStreamInterval, panelStreamShutdownGrace = oldInterval, oldGrace })

	done := make(chan struct{})
	sw := &stallWriter{hdr: http.Header{}}
	h := streamTestHandler(t, done)
	returned := make(chan struct{})
	go func() {
		h.handleSSE(sw, httptest.NewRequest(http.MethodGet, "/dashboard/events", nil))
		close(returned)
	}()
	for start := time.Now(); !sw.blocked(); {
		if time.Since(start) > 5*time.Second {
			t.Fatal("handler never blocked in a write")
		}
		time.Sleep(5 * time.Millisecond)
	}

	close(done)
	select {
	case <-returned:
	case <-time.After(3 * time.Second):
		t.Fatal("handler still blocked in a write 3s after StreamsDone closed")
	}
}
