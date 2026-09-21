package sep2capture

import (
	"context"
	"crypto/tls"
	"net"
	"net/http"
	"sync"

	sepTLS "github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2tls"
	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2tls/gotls"
)

// connectionStater is implemented by *tls.Conn, this package's own Conn
// (PR 1), and anything else net/http already trusts for identity.
type connectionStater interface {
	ConnectionState() tls.ConnectionState
}

// gotlsStater is implemented by core's *gotls.Conn: its ConnectionState
// method returns the fork's own type, not tls.ConnectionState (P3), so a
// raw *gotls.Conn (WrapCCMListener's or a bare gotls.NewListener's output)
// does not satisfy connectionStater directly. stateOf checks both shapes so
// identityFrom and recordingConn.ConnectionState see one common type
// regardless of cipher mode.
type gotlsStater interface {
	ConnectionState() gotls.ConnectionState
}

// stateOf returns c's TLS state in the tls.ConnectionState shape net/http
// understands, converting a gotls.ConnectionState with the same five
// fields conn.go's convertGotlsState already uses for this package's own
// Listener. ok is false for a connection that is not TLS-capable at all.
func stateOf(c net.Conn) (tls.ConnectionState, bool) {
	switch cs := c.(type) {
	case connectionStater:
		return cs.ConnectionState(), true
	case gotlsStater:
		return convertGotlsState(cs.ConnectionState()), true
	default:
		return tls.ConnectionState{}, false
	}
}

// identityFrom derives LFDI and SFDI from a connection's peer leaf, the
// same derivation internal/auth.IdentityMiddleware uses per request. It
// returns ok=false for a connection with no TLS state or no peer
// certificate (an anonymous listener, or a handshake this package did not
// verify), so a recorder for such a connection simply carries no identity
// rather than a fabricated one.
func identityFrom(c net.Conn) (lfdi, sfdi string, ok bool) {
	state, isStater := stateOf(c)
	if !isStater {
		return "", "", false
	}
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

	handshakeOnce sync.Once
}

// ConnectionState is net/http's one-shot source of r.TLS for any connection
// that is not itself a *tls.Conn (server.go's own unexported
// connectionStater interface): it is called exactly once per connection,
// before the first read, on that connection's own goroutine (c.serve),
// never on the shared Accept loop. completeHandshake rides that same call
// to drive the handshake and derive identity, so a slow or silent peer's
// handshake blocks only its own connection's goroutine, the same way
// net/http's own *tls.Conn special case drives its handshake on the
// per-connection goroutine too, never inside Accept (silent-failure
// re-review at 24b5e1c, finding A: the server's real CCM-8 listener,
// gotls.NewListener, and a bare crypto/tls listener both hand Accept a
// connection whose handshake has not started).
func (c *recordingConn) ConnectionState() tls.ConnectionState {
	c.handshakeOnce.Do(c.completeHandshake)
	state, _ := stateOf(c.Conn)
	return state
}

// completeHandshake drives c's inner connection through its TLS handshake
// if it has one and it is not already complete (a no-op, safely, for an
// already-handshaken input such as sepTLS.WrapCCMListener's or this
// package's own Listener's output: both *tls.Conn and *gotls.Conn treat a
// second handshake call as a no-op once complete, the same fact PR 1's
// Listener already relies on), then derives the connection's identity from
// the resulting state. It runs at most once per connection (see
// ConnectionState).
//
// A connection whose handshake fails or times out is refused here, loudly:
// closed and logged, so it is never recorded with an empty identity. The
// handshake I/O runs directly against c.Conn, the wrapped connection,
// never through recordingConn's own Read/Write: handshake records are not
// exchange bytes, and driving them here must not recurse into
// recordInbound/recordOutbound.
func (c *recordingConn) completeHandshake() {
	if hs, ok := c.Conn.(handshaker); ok {
		hsCtx, cancel := context.WithTimeout(context.Background(), defaultHandshakeTimeout)
		err := hs.HandshakeContext(hsCtx)
		cancel()
		if err != nil {
			c.rec.rs.errorLog.Printf("sep2capture: TLS handshake error from %s: %v; refusing", c.RemoteAddr(), err)
			_ = c.Conn.Close()
			return
		}
	}
	if lfdi, sfdi, ok := identityFrom(c.Conn); ok {
		c.rec.clientLFDI = lfdi
		c.rec.clientSFDI = sfdi
	}
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

// recordingListener wraps ln so every accepted connection gets a
// connRecorder before net/http ever sees it. It does not itself drive or
// check any TLS handshake: that now happens lazily, per connection, in
// recordingConn.ConnectionState (see completeHandshake), which runs on
// that connection's own goroutine rather than here on the shared Accept
// loop. Accept therefore never blocks on one peer's handshake, whatever ln
// is: this package's own Listener, sepTLS.WrapCCMListener, a bare
// gotls.NewListener, a bare crypto/tls.NewListener, or an anonymous
// non-TLS net.Listener.
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

// Close closes the inner listener and stops rs's dispatch goroutine. It is
// safe to call more than once.
func (l *recordingListener) Close() error {
	l.rs.stop()
	return l.inner.Close()
}

func (l *recordingListener) Addr() net.Addr { return l.inner.Addr() }

var _ net.Listener = (*recordingListener)(nil)

// Dropped returns how many exchanges Attach's listener ln has dropped
// because the recorderSet's shared queue to its Sink was full or
// Sink.Record panicked (see exchange.go's Sink doc). It is 0 for any
// net.Listener Attach did not return.
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
// ln's Accept may return a connection whose TLS handshake, if any, is not
// yet complete: this package's own Listener and sepTLS.WrapCCMListener
// both already hand Attach a finished handshake, and a bare
// gotls.NewListener or crypto/tls.NewListener (the server's real CCM-8 and
// GCM listeners, sep2server.wrapMTLS) do not, since both handshake lazily
// on first Read. Attach supports all four: recordingConn.ConnectionState
// drives the handshake itself, once, on the connection's own goroutine,
// the first time net/http asks for it, and refuses (closes, logs) a
// connection whose handshake fails rather than recording it with an empty
// identity. Calling Attach more than once on the same srv is safe: an
// earlier Attach's hooks stay chained and keep firing, but each
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
