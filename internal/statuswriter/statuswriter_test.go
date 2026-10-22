package statuswriter

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The wrapper must restore http.Flusher / http.Hijacker / io.ReaderFrom from
// the embedded writer, else streaming handlers silently break.
func TestRecorderKeepsHijackReadFromAndFlush(t *testing.T) {
	rec := httptest.NewRecorder()
	sr := New(rec)

	if _, ok := interface{}(sr).(http.Hijacker); !ok {
		t.Fatal("Recorder does not satisfy http.Hijacker")
	}
	if _, ok := interface{}(sr).(io.ReaderFrom); !ok {
		t.Fatal("Recorder does not satisfy io.ReaderFrom")
	}

	if err := http.NewResponseController(sr).Flush(); err != nil {
		t.Errorf("Flush through the Recorder = %v; want nil from a flushable writer", err)
	}
	if !rec.Flushed {
		t.Error("Flush did not reach the embedded Flusher")
	}

	n, err := sr.ReadFrom(strings.NewReader("stream-bytes"))
	if err != nil {
		t.Fatalf("ReadFrom: %v", err)
	}
	if n != int64(len("stream-bytes")) {
		t.Errorf("ReadFrom n = %d, want %d", n, len("stream-bytes"))
	}
	if !sr.wroteHeader {
		t.Error("ReadFrom did not mark the response as written")
	}
	if rec.Body.String() != "stream-bytes" {
		t.Errorf("ReadFrom body = %q, want %q", rec.Body.String(), "stream-bytes")
	}
	if sr.Bytes != n {
		t.Errorf("Bytes after ReadFrom = %d, want %d", sr.Bytes, n)
	}
}

func TestRecorderCapturesFirstStatusAndBytes(t *testing.T) {
	rec := httptest.NewRecorder()
	sr := New(rec)
	if sr.Status != http.StatusOK || sr.Bytes != 0 {
		t.Fatalf("fresh Status/Bytes = %d/%d, want 200/0", sr.Status, sr.Bytes)
	}
	sr.WriteHeader(http.StatusCreated)
	sr.WriteHeader(http.StatusTeapot)
	if _, err := sr.Write([]byte("abc")); err != nil {
		t.Fatal(err)
	}
	if _, err := sr.Write([]byte("de")); err != nil {
		t.Fatal(err)
	}
	if sr.Status != http.StatusCreated {
		t.Errorf("Status = %d, want the first code 201", sr.Status)
	}
	if sr.Bytes != 5 {
		t.Errorf("Bytes = %d, want 5", sr.Bytes)
	}
}

func TestRecorderImplicit200OnWrite(t *testing.T) {
	sr := New(httptest.NewRecorder())
	if _, err := sr.Write([]byte("x")); err != nil {
		t.Fatal(err)
	}
	sr.WriteHeader(http.StatusInternalServerError)
	if sr.Status != http.StatusOK {
		t.Errorf("Status = %d, want implicit 200 kept after a body write", sr.Status)
	}
}

type refusingFlusher struct{ http.ResponseWriter }

var errRefused = errors.New("flush refused")

func (refusingFlusher) FlushError() error { return errRefused }

// The protocol router's buffer refuses to flush; the Recorder in front of it
// must pass the refusal on, not turn it into nil.
func TestRecorderPassesAFlushRefusalThrough(t *testing.T) {
	sr := New(refusingFlusher{httptest.NewRecorder()})
	if err := http.NewResponseController(sr).Flush(); !errors.Is(err, errRefused) {
		t.Errorf("Flush = %v; want the embedded writer's refusal", err)
	}
	if _, ok := interface{}(sr).(http.Flusher); ok {
		t.Error("Recorder satisfies the legacy http.Flusher; that would hide the refusal from w.(http.Flusher) callers")
	}
}

func TestRecorderIgnoresInformationalStatuses(t *testing.T) {
	rec := httptest.NewRecorder()
	sr := New(rec)
	sr.WriteHeader(http.StatusContinue)
	sr.WriteHeader(http.StatusEarlyHints)
	if sr.Status != http.StatusOK {
		t.Fatalf("Status = %d after only 1xx codes; want the default 200, not a 1xx", sr.Status)
	}
	sr.WriteHeader(http.StatusAccepted)
	if sr.Status != http.StatusAccepted {
		t.Errorf("Status = %d; want the final 202 recorded after the 1xx codes", sr.Status)
	}
	sr2 := New(httptest.NewRecorder())
	sr2.WriteHeader(http.StatusSwitchingProtocols)
	if sr2.Status != http.StatusSwitchingProtocols {
		t.Errorf("Status = %d; want 101, which is final", sr2.Status)
	}
}
