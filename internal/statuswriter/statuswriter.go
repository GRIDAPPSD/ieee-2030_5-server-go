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

// WriteHeader records the first final status code and passes every call
// through. A 1xx other than 101 is informational: net/http sends it at once
// and still accepts the real status after it, so it is not the final status
// and is not recorded.
func (s *Recorder) WriteHeader(code int) {
	if code >= 100 && code <= 199 && code != http.StatusSwitchingProtocols {
		s.ResponseWriter.WriteHeader(code)
		return
	}
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
// for a type assertion, so each is written out and delegates only when the
// embedded writer has the capability.

// FlushError flushes through http.ResponseController, so the embedded
// writer's own answer comes back unchanged: nil, or its refusal, as the
// protocol router's buffer gives (it holds the whole response). Plain Flush
// is deliberately not implemented: it would satisfy the legacy
// w.(http.Flusher) check and let a handler believe a flush happened when the
// embedded writer refused it. http.NewResponseController(w).Flush() prefers
// FlushError.
func (s *Recorder) FlushError() error {
	return http.NewResponseController(s.ResponseWriter).Flush()
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
