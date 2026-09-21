package assembly

import (
	"bytes"
	"log"
	"net/http"
	"strconv"
)

// contentLengthBuffer captures a handler's response so its final byte count
// is known before anything reaches the connection.
type contentLengthBuffer struct {
	header      http.Header
	body        bytes.Buffer
	status      int
	wroteHeader bool
}

func newContentLengthBuffer() *contentLengthBuffer {
	return &contentLengthBuffer{header: make(http.Header)}
}

func (b *contentLengthBuffer) Header() http.Header { return b.header }

func (b *contentLengthBuffer) WriteHeader(status int) {
	if b.wroteHeader {
		return
	}
	b.wroteHeader = true
	b.status = status
}

func (b *contentLengthBuffer) Write(p []byte) (int, error) {
	if !b.wroteHeader {
		b.WriteHeader(http.StatusOK)
	}
	return b.body.Write(p)
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

		dst := w.Header()
		for k, v := range buf.header {
			dst[k] = v
		}

		status := buf.status
		if status == 0 {
			status = http.StatusOK
		}

		if buf.body.Len() > 0 {
			dst.Del("Transfer-Encoding")
			dst.Set("Content-Length", strconv.Itoa(buf.body.Len()))
		}

		w.WriteHeader(status)
		if buf.body.Len() > 0 {
			if _, err := w.Write(buf.body.Bytes()); err != nil {
				log.Printf("assembly: response write error: %v", err)
			}
		}
	})
}
