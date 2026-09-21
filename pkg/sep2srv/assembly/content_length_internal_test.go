package assembly

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestBufferContentLength_BodyIsByteIdentical proves the invariant that
// buffering must not change: the bytes a client reads are exactly the bytes
// the handler wrote, in the same order, only the framing headers differ.
func TestBufferContentLength_BodyIsByteIdentical(t *testing.T) {
	want := bytes.Repeat([]byte("abcdefghij"), 300) // 3000 bytes, over the 2048 threshold
	raw := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/sep+xml")
		w.WriteHeader(http.StatusOK)
		// Two separate Write calls: proves the buffer concatenates rather
		// than keeping only the last or first write.
		_, _ = w.Write(want[:1000])
		_, _ = w.Write(want[1000:])
	})

	srv := httptest.NewServer(bufferContentLength(raw))
	defer srv.Close()

	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	got := make([]byte, 0, len(want))
	buf := make([]byte, 512)
	for {
		n, rerr := resp.Body.Read(buf)
		got = append(got, buf[:n]...)
		if rerr != nil {
			break
		}
	}

	if !bytes.Equal(got, want) {
		t.Fatalf("body mismatch: got %d bytes, want %d bytes", len(got), len(want))
	}
	if resp.Header.Get("Content-Type") != "application/sep+xml" {
		t.Errorf("Content-Type = %q, want application/sep+xml", resp.Header.Get("Content-Type"))
	}
	if len(resp.TransferEncoding) != 0 {
		t.Errorf("Transfer-Encoding = %v, want none", resp.TransferEncoding)
	}
	if resp.ContentLength != int64(len(want)) {
		t.Errorf("Content-Length = %d, want %d", resp.ContentLength, len(want))
	}
}

// TestBufferContentLength_NoBodyStatusUnchanged proves the middleware
// passes a bodyless status straight through (data-invariants Rule 3
// applied to framing: the boundary case, an empty body, is itself a
// candidate for the bug, not just the large-body case that was reported).
//
// This does not exercise this file's own buf.body.Len() > 0 guard: net/http
// strips Content-Length for 204/304/1xx on its own regardless of what this
// wrapper sets (net/http/transfer.go suppressedHeaders), so these two cases
// pass even with that guard removed.
// TestBufferContentLength_BodylessOKPreservesHandlerContentLength below is
// the one the guard's removal actually fails.
func TestBufferContentLength_NoBodyStatusUnchanged(t *testing.T) {
	for _, status := range []int{http.StatusNoContent, http.StatusNotModified} {
		status := status
		t.Run(http.StatusText(status), func(t *testing.T) {
			raw := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(status)
			})
			srv := httptest.NewServer(bufferContentLength(raw))
			defer srv.Close()

			resp, err := http.Get(srv.URL)
			if err != nil {
				t.Fatalf("GET: %v", err)
			}
			defer func() { _ = resp.Body.Close() }()

			if resp.StatusCode != status {
				t.Fatalf("status = %d, want %d", resp.StatusCode, status)
			}
			if cl := resp.Header.Get("Content-Length"); cl != "" {
				t.Errorf("Content-Length = %q, want unset", cl)
			}
		})
	}
}

// TestBufferContentLength_BodylessOKPreservesHandlerContentLength is the
// test that the buf.body.Len() > 0 guard itself is what protects: a 200
// (bodyAllowedForStatus is true, so net/http does not suppress
// Content-Length the way it does for 204/304/1xx) where the handler writes
// no bytes but sets its own Content-Length, the way a HEAD response
// reports the size a GET would have returned. If the guard is removed and
// Content-Length is always set from buf.body.Len(), this becomes "0" and
// the handler's own value is lost.
func TestBufferContentLength_BodylessOKPreservesHandlerContentLength(t *testing.T) {
	raw := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "1234")
		w.WriteHeader(http.StatusOK)
	})
	srv := httptest.NewServer(bufferContentLength(raw))
	defer srv.Close()

	resp, err := http.Head(srv.URL)
	if err != nil {
		t.Fatalf("HEAD: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if got := resp.Header.Get("Content-Length"); got != "1234" {
		t.Fatalf("Content-Length = %q, want %q (the handler's own value, unclobbered)", got, "1234")
	}
}

// TestBufferContentLength_InformationalStatusIsNotFinal is item 1 of the
// #615 fix round: a 1xx WriteHeader call must not become the final status.
// The handler here asks for a non-200 final status (202) specifically so
// net/http's own implicit-200-on-Write fallback (server.go response.write)
// cannot be mistaken for a correct fix: pre-fix, the first WriteHeader
// (103) latches, the real 202 call is dropped, and the observed status
// becomes net/http's unrelated implicit 200, not 202 and not 103.
func TestBufferContentLength_InformationalStatusIsNotFinal(t *testing.T) {
	raw := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusEarlyHints) // 103, informational
		w.WriteHeader(http.StatusAccepted)   // 202, the real final status
		_, _ = w.Write([]byte("hello"))
	})
	srv := httptest.NewServer(bufferContentLength(raw))
	defer srv.Close()

	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("status = %d, want %d (202 dropped for a 103, or net/http's own implicit 200)", resp.StatusCode, http.StatusAccepted)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if string(body) != "hello" {
		t.Fatalf("body = %q, want %q", body, "hello")
	}
}

// TestBufferContentLength_HeaderFrozenAtWriteHeader is item 2 of the #615
// fix round: a header set after WriteHeader must not reach the client, the
// same as net/http (server.go response.WriteHeader clones handlerHeader at
// that point). Pre-fix, the wrapper copies buf.header only after
// ServeHTTP returns, so a late mutation leaks through.
func TestBufferContentLength_HeaderFrozenAtWriteHeader(t *testing.T) {
	raw := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Before", "before")
		w.WriteHeader(http.StatusOK)
		w.Header().Set("X-After", "after") // must not reach the client
		_, _ = w.Write([]byte("body"))
	})
	srv := httptest.NewServer(bufferContentLength(raw))
	defer srv.Close()

	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if got := resp.Header.Get("X-Before"); got != "before" {
		t.Errorf("X-Before = %q, want %q", got, "before")
	}
	if got := resp.Header.Get("X-After"); got != "" {
		t.Errorf("X-After = %q, want unset: a header set after WriteHeader must not reach the client", got)
	}
}

// TestBufferContentLength_FlushFailsVisibly is item 3 of the #615 fix
// round: a handler using the modern http.NewResponseController API for an
// optional capability this wrapper cannot provide gets an explicit error,
// not a silent no-op. Pre-fix, FlushError is unimplemented, so
// ResponseController already falls back to its own generic
// http.ErrNotSupported; the message assertion below is what distinguishes
// that generic fallback from this wrapper's own explicit guard.
func TestBufferContentLength_FlushFailsVisibly(t *testing.T) {
	var flushErr error
	raw := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flushErr = http.NewResponseController(w).Flush()
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	srv := httptest.NewServer(bufferContentLength(raw))
	defer srv.Close()

	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if !errors.Is(flushErr, http.ErrNotSupported) {
		t.Fatalf("Flush error = %v, want a match for http.ErrNotSupported", flushErr)
	}
	if !strings.Contains(flushErr.Error(), "bufferContentLength") {
		t.Errorf("Flush error = %q, want a message naming the wrapper, not net/http's generic fallback", flushErr)
	}
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200: the guard must not stop the handler from still responding", resp.StatusCode)
	}
}

// TestBufferContentLength_HijackFailsVisibly is item 3 of the #615 fix
// round, for the legacy type-assertion path rather than
// http.NewResponseController. Pre-fix, Hijacker is unimplemented, so
// w.(http.Hijacker) itself reports ok = false: a handler that checks
// before hijacking cannot even attempt it, and gets no reason why.
func TestBufferContentLength_HijackFailsVisibly(t *testing.T) {
	var ok bool
	var hijackErr error
	raw := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var hj http.Hijacker
		hj, ok = w.(http.Hijacker)
		if ok {
			_, _, hijackErr = hj.Hijack()
		}
		w.WriteHeader(http.StatusOK)
	})
	srv := httptest.NewServer(bufferContentLength(raw))
	defer srv.Close()

	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if !ok {
		t.Fatalf("w.(http.Hijacker) ok = false, want true: a handler that checks before hijacking should be told why, not left unable to even try")
	}
	if !errors.Is(hijackErr, http.ErrNotSupported) {
		t.Fatalf("Hijack error = %v, want a match for http.ErrNotSupported", hijackErr)
	}
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200: the guard must not stop the handler from still responding", resp.StatusCode)
	}
}
