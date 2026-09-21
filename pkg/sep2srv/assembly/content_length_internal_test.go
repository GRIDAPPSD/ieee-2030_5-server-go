package assembly

import (
	"bytes"
	"net/http"
	"net/http/httptest"
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
