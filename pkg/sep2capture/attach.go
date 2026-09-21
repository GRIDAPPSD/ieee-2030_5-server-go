package sep2capture

import (
	"crypto/tls"
	"net"
	"net/http"

	sepTLS "github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2tls"
)

// connectionStater is implemented by *tls.Conn, core's *gotls.Conn (via
// this package's own Conn from PR 1), and anything else net/http already
// trusts for identity. identityFrom and recordingListener.Accept both use it
// rather than a concrete type, so Attach works with this package's own
// Listener, sepTLS.WrapCCMListener, or any other listener whose Accept
// already returns a connection with a complete handshake.
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

// recordingListener wraps a listener whose Accept already returns
// handshaken connections, so every one gets a connRecorder before net/http
// ever sees it. A connection that is TLS-capable but has not finished its
// handshake is refused rather than wrapped: see Accept.
type recordingListener struct {
	inner net.Listener
	rs    *recorderSet
}

// Accept requires ln's connections to have already completed any TLS
// handshake before Accept returns them: identity is derived once, in wrap,
// from ConnectionState, and a connection whose handshake is still pending
// would record an empty identity there and only ever pick one up later, if
// at all, once something else drives the handshake. Attach does not drive
// the handshake itself: this package's own Listener already does that job
// correctly, off the accept loop's own goroutine and bounded by a timeout,
// and duplicating that inside a synchronous Accept here would either
// serialize every handshake behind this call or reimplement Listener's
// concurrent machinery a second time. So a connection that implements
// connectionStater but has not finished handshaking is refused instead:
// closed, logged, and skipped, rather than silently recorded with no
// identity. A connection that is not TLS-capable at all (does not
// implement connectionStater) is not refused; identityFrom already handles
// that case by recording no identity, which is not this bug.
func (l *recordingListener) Accept() (net.Conn, error) {
	for {
		c, err := l.inner.Accept()
		if err != nil {
			return nil, err
		}
		if cs, ok := c.(connectionStater); ok && !cs.ConnectionState().HandshakeComplete {
			l.rs.errorLog.Printf("sep2capture: connection from %s has not completed its TLS handshake before Accept; Attach requires a listener that handshakes first (sep2capture.NewListener or an equivalent); refusing", c.RemoteAddr())
			_ = c.Close()
			continue
		}
		return l.rs.wrap(c), nil
	}
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
// ln's Accept must already return connections whose TLS handshake, if any,
// is complete: this package's own Listener and sepTLS.WrapCCMListener both
// do; a bare tls.NewListener or gotls.NewListener does not, since both
// handshake lazily on first Read, and Attach refuses a connection that is
// not yet handshaken rather than recording it with no identity (see
// recordingListener.Accept). Calling Attach more than once on the same srv
// is safe: an earlier Attach's hooks stay chained and keep firing, but each
// recorderSet only acts on the connections its own listener actually
// wrapped.
//
// Errors this package logs on its own (a refused unhandshaken connection, a
// panicking Sink) go to srv.ErrorLog, or the standard logger if that is nil,
// matching net/http's own default.
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
//
// next is srv.Handler as Attach found it, nil included: a nil Handler is
// ordinary net/http usage (server.go's serverHandler substitutes
// DefaultServeMux at serve time), but Attach always replaces srv.Handler
// with this wrapper, so that substitution must happen here instead.
func (rs *recorderSet) annotate(next http.Handler) http.Handler {
	if next == nil {
		next = http.DefaultServeMux
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if rec := rs.byRemoteAddr(r.RemoteAddr); rec != nil {
			rec.markHandlerRan()
		}
		next.ServeHTTP(w, r)
	})
}
