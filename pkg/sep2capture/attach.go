package sep2capture

import (
	"crypto/tls"
	"net"
	"net/http"

	sepTLS "github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2tls"
)

// connectionStater is implemented by *tls.Conn, core's *gotls.Conn (via
// this package's own Conn from PR 1), and anything else net/http already
// trusts for identity. identityFrom uses it rather than a concrete type so
// Attach works whether ln is this package's Listener or a bare
// tls.Listener that has already handshaken.
type connectionStater interface {
	ConnectionState() tls.ConnectionState
}

// identityFrom derives LFDI and SFDI from a connection's peer leaf, the
// same derivation internal/auth.IdentityMiddleware uses per request. It
// returns ok=false for a connection with no TLS state or no peer
// certificate (an anonymous listener, or a handshake this package did not
// verify), so a recorder for such a connection simply carries no identity
// rather than a fabricated one.
func identityFrom(c net.Conn) (lfdi, sfdi string, ok bool) {
	cs, isStater := c.(connectionStater)
	if !isStater {
		return "", "", false
	}
	state := cs.ConnectionState()
	if len(state.PeerCertificates) == 0 {
		return "", "", false
	}
	cert := state.PeerCertificates[0]
	return sepTLS.LFDI(cert), sepTLS.SFDI(cert), true
}

// recordingConn wraps an accepted connection to copy every byte it moves
// into the exchange the connRecorder currently has open, without changing
// what Read or Write return to their caller. ConnectionState is forwarded
// explicitly: embedding net.Conn alone promotes only net.Conn's own
// methods, so without this net/http would not see it and every request
// would fail identity (the same lesson PR 1's Conn documents).
type recordingConn struct {
	net.Conn
	rec *connRecorder
}

func (c *recordingConn) ConnectionState() tls.ConnectionState {
	if cs, ok := c.Conn.(connectionStater); ok {
		return cs.ConnectionState()
	}
	return tls.ConnectionState{}
}

func (c *recordingConn) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	isPeek := len(p) == 1
	if n > 0 {
		c.rec.recordInbound(p[:n], isPeek)
	}
	// Only the aborted-peek's own timeout is not the exchange's: net/http
	// deliberately times this read out on every exchange close
	// (connReader.abortPendingRead, server.go) to reclaim it for the next
	// request, and ignores that timeout itself (connReader.backgroundRead
	// does the same check net/http/server.go, err.(net.Error) with
	// Timeout()). Any other error on this read is real: a reset or a
	// corrupt TLS record arriving while net/http is waiting on this
	// background peek is exactly as much a connection failure as one on
	// any other read, and net/http itself does not discard it either
	// (handleReadErrorLocked runs for it). Only the timeout net/http
	// itself manufactures is not.
	if err != nil && !(isPeek && isTimeout(err)) {
		c.rec.noteError(err)
	}
	return n, err
}

func (c *recordingConn) Write(p []byte) (int, error) {
	n, err := c.Conn.Write(p)
	if n > 0 {
		c.rec.recordOutbound(p[:n])
	}
	c.rec.noteError(err)
	return n, err
}

var _ net.Conn = (*recordingConn)(nil)
var _ connectionStater = (*recordingConn)(nil)

// recordingListener wraps a handshaken listener so every Accept'd
// connection gets a connRecorder before net/http ever sees it.
type recordingListener struct {
	inner net.Listener
	rs    *recorderSet
}

func (l *recordingListener) Accept() (net.Conn, error) {
	c, err := l.inner.Accept()
	if err != nil {
		return nil, err
	}
	return l.rs.wrap(c), nil
}

func (l *recordingListener) Close() error   { return l.inner.Close() }
func (l *recordingListener) Addr() net.Addr { return l.inner.Addr() }

var _ net.Listener = (*recordingListener)(nil)

// Dropped returns how many exchanges Attach's listener ln has dropped
// because a connection's queue to its Sink was full or Sink.Record
// panicked (see exchange.go's Sink doc). It is 0 for any net.Listener
// Attach did not return.
func Dropped(ln net.Listener) uint64 {
	rl, ok := ln.(*recordingListener)
	if !ok {
		return 0
	}
	return rl.rs.dropped.Load()
}

// Attach installs recording on srv and ln: every exchange on every
// connection Accept returns is captured and handed to sink. It must run
// after srv.Handler and any srv.ConnState are set and before Serve: it
// chains srv.ConnState so a hook already installed keeps firing, and it
// wraps srv.Handler with the annotation middleware, outermost, so the
// middleware sees every request before anything else in the chain. ln is
// not modified; Attach returns the listener to serve instead.
//
// Errors this package logs on its own (currently, a panicking Sink) go to
// srv.ErrorLog, or the standard logger if that is nil, matching net/http's
// own default.
func Attach(srv *http.Server, ln net.Listener, sink Sink) net.Listener {
	rs := newRecorderSet(sink, srv.ErrorLog)

	previous := srv.ConnState
	srv.ConnState = func(c net.Conn, state http.ConnState) {
		rs.observe(c, state)
		if previous != nil {
			previous(c, state)
		}
	}

	srv.Handler = rs.annotate(srv.Handler)

	return &recordingListener{inner: ln, rs: rs}
}

// annotate marks the exchange open on the request's connection as
// "a handler ran" before calling next. It reads no body and wraps nothing
// else: a handler that never touches the body still gets marked, and a
// handler that panics after this point still counts as having run.
func (rs *recorderSet) annotate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if rec := rs.byRemoteAddr(r.RemoteAddr); rec != nil {
			rec.markHandlerRan()
		}
		next.ServeHTTP(w, r)
	})
}
