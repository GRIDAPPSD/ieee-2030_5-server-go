package assembly

import (
	"bufio"
	"bytes"
	"fmt"
	"log"
	"net"
	"net/http"
	"strconv"
)

// contentLengthBuffer captures a handler's response so its final byte count
// is known before anything reaches the connection. It deliberately does not
// pass Flush or Hijack through to a real connection, and does not implement
// Unwrap: it never holds the real http.ResponseWriter, only a byte buffer,
// so there is nothing to flush early and no connection to hand over. See
// FlushError and Hijack below for how a handler that tries anyway is told
// so, rather than left to find out the hard way.
type contentLengthBuffer struct {
	header       http.Header
	frozenHeader http.Header
	body         bytes.Buffer
	status       int
	wroteHeader  bool
}

func newContentLengthBuffer() *contentLengthBuffer {
	return &contentLengthBuffer{header: make(http.Header)}
}

func (b *contentLengthBuffer) Header() http.Header { return b.header }

// WriteHeader records the final status and freezes a copy of the headers,
// matching net/http: a header mutated after WriteHeader has no effect on
// what is sent (net/http/server.go response.WriteHeader clones
// handlerHeader at the same point). A 1xx status is informational, not
// final: net/http sends it immediately and leaves its own wroteHeader
// unset so a later, real WriteHeader call still applies. This wrapper
// cannot forward an early informational response, since it buffers the
// whole response until the handler returns, but it must not let a 1xx
// masquerade as the final status either (#615).
func (b *contentLengthBuffer) WriteHeader(status int) {
	if b.wroteHeader {
		return
	}
	if status >= 100 && status <= 199 && status != http.StatusSwitchingProtocols {
		return
	}
	b.wroteHeader = true
	b.status = status
	b.frozenHeader = b.header.Clone()
}

func (b *contentLengthBuffer) Write(p []byte) (int, error) {
	if !b.wroteHeader {
		b.WriteHeader(http.StatusOK)
	}
	return b.body.Write(p)
}

var (
	// errFlushUnsupported and errHijackUnsupported both wrap
	// http.ErrNotSupported so a caller using http.NewResponseController
	// (the interface FlushError and Hijack exist for) sees the same
	// sentinel net/http itself returns for an unsupported capability, with
	// a message naming which one and why.
	errFlushUnsupported  = fmt.Errorf("assembly: bufferContentLength buffers the whole response and cannot flush early: %w", http.ErrNotSupported)
	errHijackUnsupported = fmt.Errorf("assembly: bufferContentLength buffers the whole response and holds no real connection to hijack: %w", http.ErrNotSupported)
)

// FlushError reports that this wrapper cannot flush. http.ResponseController
// prefers FlushError over Flusher when both are present, so a handler using
// the modern API (http.NewResponseController(w).Flush()) gets this error
// instead of a silent no-op. The plain, error-less Flush signature is
// deliberately NOT implemented: doing so would satisfy the legacy
// w.(http.Flusher) check and let a handler believe a flush happened when
// nothing did.
func (b *contentLengthBuffer) FlushError() error { return errFlushUnsupported }

// Hijack reports that this wrapper cannot hand over the connection, rather
// than leaving Hijacker unimplemented: implementing it means both the
// legacy w.(http.Hijacker) check and http.NewResponseController find the
// capability and get an explicit error, instead of the type assertion
// failing with no explanation at all.
func (b *contentLengthBuffer) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return nil, nil, errHijackUnsupported
}

// bufferContentLength wraps the whole protocol router so every response
// carries an explicit Content-Length instead of relying on net/http's
// implicit detection, which only works when the entire body fits the
// 2048-byte pre-chunking buffer (bufferBeforeChunkingSize in
// net/http/server.go). A MirrorUsagePoint list, or any other resource past
// that size, goes out chunked, and constrained 2030.5 clients that cannot
// decode chunked responses fail to parse it (#613).
//
// A response with no body (204, a HEAD reply, a write of zero bytes) is
// passed through with only its status and headers: forcing
// Content-Length: 0 onto a 204 would add a header net/http never sends for
// that status, and that is an "every other header is unchanged" invariant
// break, not a framing fix.
func bufferContentLength(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := newContentLengthBuffer()
		next.ServeHTTP(buf, r)

		// A handler that never wrote a final status (no explicit
		// WriteHeader call that stuck, no Write) finalizes here, matching
		// net/http's implicit 200: whatever headers were set by the time
		// ServeHTTP returned are what ships.
		if !buf.wroteHeader {
			buf.WriteHeader(http.StatusOK)
		}

		dst := w.Header()
		for k, v := range buf.frozenHeader {
			dst[k] = v
		}

		if buf.body.Len() > 0 {
			dst.Del("Transfer-Encoding")
			dst.Set("Content-Length", strconv.Itoa(buf.body.Len()))
		}

		w.WriteHeader(buf.status)
		if buf.body.Len() > 0 {
			if _, err := w.Write(buf.body.Bytes()); err != nil {
				log.Printf("assembly: response write error: %v", err)
			}
		}
	})
}
