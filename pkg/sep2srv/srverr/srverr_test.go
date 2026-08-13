package srverr_test

import (
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/srverr"
)

// safeBuffer is an io.Writer a test can read while the logger is still
// writing to it. The standard logger serialises its own writes, but nothing
// serialises a write against this test's read, and a plain bytes.Buffer would
// be a data race under -race the moment a handler logged from a server
// goroutine.
type safeBuffer struct {
	mu sync.Mutex
	b  strings.Builder
}

func (s *safeBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *safeBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// captureLog redirects the standard logger for the duration of the test and
// returns the buffer it writes into.
//
// Flags are cleared so an assertion can match from the start of the line
// rather than around a timestamp whose width varies.
func captureLog(t *testing.T) *safeBuffer {
	t.Helper()

	buf := &safeBuffer{}
	prevFlags := log.Flags()
	prevOut := log.Writer()
	log.SetFlags(0)
	log.SetOutput(buf)
	t.Cleanup(func() {
		log.SetOutput(prevOut)
		log.SetFlags(prevFlags)
	})
	return buf
}

// TestInternalLogsTheRouteAndTheError is the core obligation: a 500 that
// leaves no server-side record is a failure an operator cannot investigate.
func TestInternalLogsTheRouteAndTheError(t *testing.T) {
	buf := captureLog(t)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /edev/{id}/der/{derId}", func(w http.ResponseWriter, r *http.Request) {
		srverr.Internal(w, r, errors.New("the backend did not answer"))
	})

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/edev/77/der/9", nil))

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusInternalServerError)
	}
	if got := strings.TrimSpace(rec.Body.String()); got != srverr.DefaultMessage {
		t.Errorf("body = %q, want %q", got, srverr.DefaultMessage)
	}

	line := buf.String()
	wantPrefix := srverr.LogLinePrefix("GET /edev/{id}/der/{derId}")
	if !strings.HasPrefix(line, wantPrefix) {
		t.Fatalf("log = %q, want it to start with %q", line, wantPrefix)
	}
	if !strings.Contains(line, "the backend did not answer") {
		t.Errorf("log = %q, want it to carry the error", line)
	}
}

// TestInternalLogsNothingTheClientSupplied pins the half of the contract that
// is easy to lose to a well-meant "add the id, it helps debugging" change.
//
// The concrete path, the path values, and the body are all attacker-chosen on
// a public interface, and a log line is a widely-readable artifact. The route
// pattern is the server's own registration string and is the only request
// detail this package is allowed to emit.
func TestInternalLogsNothingTheClientSupplied(t *testing.T) {
	buf := captureLog(t)

	const (
		secretPathValue = "4E5F60718293A4B5C6D7E8F9"
		secretHeader    = "s3cr3t-header-value"
		secretBody      = "<pIN>123456</pIN>"
	)

	mux := http.NewServeMux()
	mux.HandleFunc("POST /edev/{id}", func(w http.ResponseWriter, r *http.Request) {
		srverr.Internal(w, r, errors.New("the backend did not answer"))
	})

	req := httptest.NewRequest(http.MethodPost, "/edev/"+secretPathValue+"?q="+secretPathValue,
		strings.NewReader(secretBody))
	req.Header.Set("Authorization", secretHeader)
	mux.ServeHTTP(httptest.NewRecorder(), req)

	line := buf.String()
	for _, leaked := range []string{secretPathValue, secretHeader, secretBody, "pIN"} {
		if strings.Contains(line, leaked) {
			t.Errorf("log = %q; it carries client-supplied %q, which must never reach a log line", line, leaked)
		}
	}
}

// TestInternalMessageKeepsTheDetailOffTheWire is the mirror obligation: the
// detail goes to the operator, never to the client. A 500 body that echoed
// the store's error would hand a caller the shape of the backend.
func TestInternalMessageKeepsTheDetailOffTheWire(t *testing.T) {
	buf := captureLog(t)

	const internalDetail = "dial tcp 10.0.0.4:5432: connection refused"

	mux := http.NewServeMux()
	mux.HandleFunc("POST /mup", func(w http.ResponseWriter, r *http.Request) {
		srverr.InternalMessage(w, r, "registration race", fmt.Errorf("create: %s", internalDetail))
	})

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/mup", nil))

	if got := strings.TrimSpace(rec.Body.String()); got != "registration race" {
		t.Errorf("body = %q, want the caller-chosen message", got)
	}
	if strings.Contains(rec.Body.String(), internalDetail) {
		t.Errorf("body = %q; the internal detail reached the client", rec.Body.String())
	}
	if !strings.Contains(buf.String(), internalDetail) {
		t.Errorf("log = %q; the internal detail did not reach the operator", buf.String())
	}
}

// TestRouteReportsAPlaceholderForAnUnroutedRequest covers the handler invoked
// without a mux in front of it, which is what a handler unit test does.
func TestRouteReportsAPlaceholderForAnUnroutedRequest(t *testing.T) {
	if got := srverr.Route(httptest.NewRequest(http.MethodGet, "/edev/1", nil)); got != srverr.UnroutedPattern {
		t.Errorf("Route with no pattern = %q, want %q", got, srverr.UnroutedPattern)
	}
	if got := srverr.Route(nil); got != srverr.UnroutedPattern {
		t.Errorf("Route(nil) = %q, want %q", got, srverr.UnroutedPattern)
	}

	buf := captureLog(t)
	rec := httptest.NewRecorder()
	srverr.Internal(rec, httptest.NewRequest(http.MethodGet, "/edev/1", nil), errors.New("boom"))

	if !strings.HasPrefix(buf.String(), srverr.LogLinePrefix(srverr.UnroutedPattern)) {
		t.Errorf("log = %q, want it to start with %q", buf.String(), srverr.LogLinePrefix(srverr.UnroutedPattern))
	}
}

// TestInternalNamesAMissingError keeps a call site that had no error value
// from producing a line that reads like a bug in the logging itself.
func TestInternalNamesAMissingError(t *testing.T) {
	buf := captureLog(t)

	rec := httptest.NewRecorder()
	srverr.Internal(rec, httptest.NewRequest(http.MethodGet, "/dcap", nil), nil)

	if strings.Contains(buf.String(), "<nil>") {
		t.Errorf("log = %q, want a named placeholder rather than a rendered nil", buf.String())
	}
	if !strings.Contains(buf.String(), "no error reported") {
		t.Errorf("log = %q, want it to say no error was reported", buf.String())
	}
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusInternalServerError)
	}
}
