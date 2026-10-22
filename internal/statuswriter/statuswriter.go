// Package statuswriter is the one http.ResponseWriter wrapper that captures
// the response status and the body bytes written, for the request metrics
// and the device log.
package statuswriter

import (
	"bufio"
	"io"
	"net"
	"net/http"
)

// Recorder captures the status code and the body bytes written. Status
// defaults to 200 when WriteHeader is never called (Go's implicit 200 on the
// first Write).
type Recorder struct {
	http.ResponseWriter
	Status      int
	Bytes       int64
	wroteHeader bool
}

// New wraps w with Status 200 and no bytes written.
func New(w http.ResponseWriter) *Recorder {
	return &Recorder{ResponseWriter: w, Status: http.StatusOK}
}

// WriteHeader records the first status code and passes every call through.
func (s *Recorder) WriteHeader(code int) {
	if !s.wroteHeader {
		s.Status = code
		s.wroteHeader = true
	}
	s.ResponseWriter.WriteHeader(code)
}

// Write counts the bytes the underlying writer accepted.
func (s *Recorder) Write(b []byte) (int, error) {
	s.wroteHeader = true
	n, err := s.ResponseWriter.Write(b)
	s.Bytes += int64(n)
	return n, err
}

// The methods below restore the optional http interfaces that the embedded
// http.ResponseWriter may implement. Embedding alone does NOT promote them
// for a type assertion: a caller doing `w.(http.Flusher)` against the
// Recorder fails even when the underlying writer is a Flusher, which would
// silently break streaming/SSE responses. Each passthrough delegates only
// when the embedded writer actually implements the interface.

// Flush implements http.Flusher when the embedded writer does, so streaming
// handlers (e.g. SSE) can force a chunk to the client.
func (s *Recorder) Flush() {
	if f, ok := s.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Hijack implements http.Hijacker when the embedded writer does, so
// connection-upgrade handlers (WebSocket, raw TCP) keep working through the
// wrapper.
func (s *Recorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if h, ok := s.ResponseWriter.(http.Hijacker); ok {
		return h.Hijack()
	}
	return nil, nil, http.ErrNotSupported
}

// ReadFrom implements io.ReaderFrom when the embedded writer does, preserving
// the zero-copy sendfile fast path for large file responses. The status is
// marked written because a successful ReadFrom emits the body (implicit 200
// if WriteHeader was never called), matching the Write path's bookkeeping.
func (s *Recorder) ReadFrom(src io.Reader) (int64, error) {
	s.wroteHeader = true
	if rf, ok := s.ResponseWriter.(io.ReaderFrom); ok {
		n, err := rf.ReadFrom(src)
		s.Bytes += n
		return n, err
	}
	// Fallback: copy through Write so the contract still holds when the
	// embedded writer is not a ReaderFrom.
	n, err := io.Copy(struct{ io.Writer }{s.ResponseWriter}, src)
	s.Bytes += n
	return n, err
}
