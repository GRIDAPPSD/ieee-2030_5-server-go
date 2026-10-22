package statuswriter

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The wrapper must restore http.Flusher / http.Hijacker / io.ReaderFrom from
// the embedded writer, else streaming handlers silently break.
func TestRecorderPreservesOptionalInterfaces(t *testing.T) {
	rec := httptest.NewRecorder()
	sr := New(rec)

	if _, ok := interface{}(sr).(http.Flusher); !ok {
		t.Fatal("Recorder does not satisfy http.Flusher")
	}
	if _, ok := interface{}(sr).(http.Hijacker); !ok {
		t.Fatal("Recorder does not satisfy http.Hijacker")
	}
	if _, ok := interface{}(sr).(io.ReaderFrom); !ok {
		t.Fatal("Recorder does not satisfy io.ReaderFrom")
	}

	sr.Flush()
	if !rec.Flushed {
		t.Error("Recorder.Flush did not delegate to the embedded Flusher")
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
