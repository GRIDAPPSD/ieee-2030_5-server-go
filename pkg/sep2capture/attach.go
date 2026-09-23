package sep2capture

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"net/http"
	"sync"
	"time"

	sepTLS "github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2tls"
	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2tls/gotls"
)

// connectionStater is implemented by *tls.Conn, this package's own Conn
// (PR 1), and anything else net/http already trusts for identity.
type connectionStater interface {
	ConnectionState() tls.ConnectionState
}

// gotlsStater is implemented by core's *gotls.Conn: its ConnectionState
// method returns the fork's own type, not tls.ConnectionState, so a raw
// *gotls.Conn (WrapCCMListener's or a bare gotls.NewListener's output) does
// not satisfy connectionStater directly. stateOf checks both shapes so
// identityFrom and the TLS-capable recording conn see one common type
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

// isTLSCapable reports whether c's own accepted type carries a
// ConnectionState method, without calling it: wrap uses this at Accept
// time to decide which recording conn type to return (Q4), before any
// handshake has necessarily run.
func isTLSCapable(c net.Conn) bool {
	switch c.(type) {
	case connectionStater, gotlsStater:
		return true
	default:
		return false
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

// wrappedConn is implemented by both recordingConn and plainRecordingConn
// (via the embedded recordingCore), so observe can reach the shared
// connRecorder without knowing which of the two Accept actually returned.
type wrappedConn interface {
	net.Conn
	recorderFor() *connRecorder
}

// recordingCore copies every byte a wrapped connection moves into the
// exchange its connRecorder currently has open, without changing what Read
// or Write return to their caller. recordingConn and plainRecordingConn
// each embed it and differ only in whether ConnectionState exists (Q4).
type recordingCore struct {
	net.Conn
	rec *connRecorder

	handshakeOnce sync.Once
}

func (c *recordingCore) recorderFor() *connRecorder { return c.rec }

// completeHandshake drives c's inner connection through its TLS handshake
// if it has one and it is not already complete (a no-op, safely, for an
// already-handshaken input such as sepTLS.WrapCCMListener's or this
// package's own Listener's output: both *tls.Conn and *gotls.Conn treat a
// second handshake call as a no-op once complete), then derives the
// connection's identity from the resulting state. It runs at most once per
// connection, from ConnectionState, which only the TLS-capable wrapper type
// implements: this method never runs at all for a plain connection.
//
// A connection whose handshake fails or times out is refused here, loudly:
// closed and logged, so it is never recorded with an empty identity. The
// handshake I/O runs directly against c.Conn, the wrapped connection, never
// through recordingCore's own Read/Write: handshake records are not
// exchange bytes, and driving them here must not recurse into
// recordInbound/recordOutbound.
func (c *recordingCore) completeHandshake() {
	if hs, ok := c.Conn.(handshaker); ok {
		hsCtx, cancel := context.WithTimeout(context.Background(), handshakeBoundFor(c.rec.att.srv))
		err := hs.HandshakeContext(hsCtx)
		cancel()
		if err != nil {
			c.rec.att.r.errorLog.Printf("sep2capture: TLS handshake error from %s: %v; refusing", c.RemoteAddr(), err)
			_ = c.Conn.Close()
			return
		}
	}
	if lfdi, sfdi, ok := identityFrom(c.Conn); ok {
		c.rec.clientLFDI = lfdi
		c.rec.clientSFDI = sfdi
	}
}

func (c *recordingCore) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	isPeek := len(p) == 1
	if n > 0 {
		c.rec.recordInbound(p[:n], isPeek)
	}
	// abortPendingRead's manufactured timeout was never a real error (as
	// before). A plain io.EOF on this peek is the other exclusion: it is
	// "an ordinary client disconnect" (Mark's own doc), the routine end of
	// a connection whose exchange already succeeded, not a failure of it.
	// net/http's own http.Client produces exactly this EOF whenever a
	// caller closes a response body without draining it first
	// (net/http/transport.go's bodyEOFSignal.earlyCloseFn) - common client
	// code, not a transport fault. Any other error (a reset, a corrupt TLS
	// record) still counts, per the TestRecordingConnRead* tests below.
	if err != nil && !(isPeek && (isTimeout(err) || errors.Is(err, io.EOF))) {
		c.rec.noteError(err)
	}
	return n, err
}

func (c *recordingCore) Write(p []byte) (int, error) {
	n, err := c.Conn.Write(p)
	if n > 0 {
		c.rec.recordOutbound(p[:n])
	}
	c.rec.noteError(err)
	return n, err
}

// recordingConn is the wrapper Accept returns for a TLS-capable accepted
// connection: it forwards ConnectionState explicitly, since embedding
// net.Conn alone promotes only net.Conn's own methods, so without this
// net/http would not see it and every request would fail identity.
type recordingConn struct {
	recordingCore
}

// ConnectionState is net/http's one-shot source of r.TLS for any connection
// that is not itself a *tls.Conn: it is called exactly once per connection,
// before the first read, on that connection's own goroutine (c.serve),
// never on the shared Accept loop. completeHandshake rides that same call
// to drive the handshake and derive identity, so a slow or silent peer's
// handshake blocks only its own connection's goroutine, the same way
// net/http's own *tls.Conn special case drives its handshake on the
// per-connection goroutine too, never inside Accept.
func (c *recordingConn) ConnectionState() tls.ConnectionState {
	c.handshakeOnce.Do(c.completeHandshake)
	state, _ := stateOf(c.Conn)
	return state
}

var _ net.Conn = (*recordingConn)(nil)
var _ connectionStater = (*recordingConn)(nil)

// plainRecordingConn is the wrapper Accept returns for a connection that is
// not TLS-capable at all (Q4): it records the same bytes recordingConn
// does but has no ConnectionState method, so net/http leaves r.TLS nil for
// it, the same as for any other plaintext connection.
type plainRecordingConn struct {
	recordingCore
}

var _ net.Conn = (*plainRecordingConn)(nil)

// recordingListener wraps ln so every accepted connection gets a
// connRecorder before net/http ever sees it. It does not itself drive or
// check any TLS handshake: that happens lazily, per connection, in
// recordingConn.ConnectionState (see completeHandshake), which runs on
// that connection's own goroutine rather than here on the shared Accept
// loop. Accept therefore never blocks on one peer's handshake, whatever ln
// is: this package's own Listener, sepTLS.WrapCCMListener, a bare
// gotls.NewListener, a bare crypto/tls.NewListener, or an anonymous
// non-TLS net.Listener.
type recordingListener struct {
	inner net.Listener
	att   *attachment
}

func (l *recordingListener) Accept() (net.Conn, error) {
	c, err := l.inner.Accept()
	if err != nil {
		return nil, err
	}
	return l.att.wrap(c), nil
}

// Close closes the inner listener. It does not touch the Recorder or its
// dispatch goroutine: srv.Shutdown and srv.Close both call this while
// holding their own lock, and neither may wait on a Sink (Q2). The caller
// stops recording afterward, with Recorder.Close (Q1).
func (l *recordingListener) Close() error {
	return l.inner.Close()
}

func (l *recordingListener) Addr() net.Addr { return l.inner.Addr() }

var _ net.Listener = (*recordingListener)(nil)

// handshakeBoundFor computes the bound net/http itself applies to a bare
// *tls.Conn (GOROOT server.go's tlsHandshakeTimeout): the smallest positive
// of ReadHeaderTimeout, ReadTimeout and WriteTimeout, or
// defaultHandshakeTimeout when all three are zero or srv is nil. Unlike
// net/http, this package has no other bound on an unhandshaken peer, so
// falling back to unlimited would reopen the Slowloris surface
// pkg/sep2server/server.go's own ReadHeaderTimeout comment rules out.
func handshakeBoundFor(srv *http.Server) time.Duration {
	var bound time.Duration
	if srv != nil {
		for _, v := range [...]time.Duration{srv.ReadHeaderTimeout, srv.ReadTimeout, srv.WriteTimeout} {
			if v <= 0 {
				continue
			}
			if bound == 0 || v < bound {
				bound = v
			}
		}
	}
	if bound == 0 {
		return defaultHandshakeTimeout
	}
	return bound
}

// Attach installs recording on srv and ln: every exchange on every
// connection Accept returns is captured and handed to r's sink. It must run
// after srv.Handler and any srv.ConnState are set and before Serve: it
// chains srv.ConnState so a hook already installed keeps firing, and it
// wraps srv.Handler with the annotation middleware, outermost, so the
// middleware sees every request before anything else in the chain. ln is
// not modified; Attach returns the listener to serve instead.
//
// ln's Accept may return a connection whose TLS handshake, if any, is not
// yet complete: this package's own Listener and sepTLS.WrapCCMListener both
// already hand Attach a finished handshake, and a bare gotls.NewListener or
// crypto/tls.NewListener (the server's real CCM-8 and GCM listeners,
// sep2server.wrapMTLS) do not, since both handshake lazily on first Read.
// Attach supports all four, plus a plain, non-TLS listener (Q4):
// recordingConn.ConnectionState drives the handshake itself, once, on the
// connection's own goroutine, the first time net/http asks for it, bounded
// by handshakeBoundFor(srv) (Q3), and refuses (closes, logs) a connection
// whose handshake fails rather than recording it with an empty identity.
// Calling Attach more than once on the same srv is safe: an earlier
// Attach's hooks stay chained and keep firing, but each attachment only
// acts on the connections its own listener actually wrapped.
//
// A ConnState or ConnContext hook installed on srv, before or after Attach,
// must not call ConnectionState on StateNew: that is the one hook net/http
// runs on the shared accept loop, and calling it there would drive this
// connection's handshake on that loop instead of its own goroutine,
// stalling Accept for up to the handshake bound.
//
// Errors this package logs on its own (a refused unhandshaken connection, a
// panicking Sink) go to r's own errorLog, set once at NewRecorder, not to
// srv.ErrorLog: one Recorder can be attached to more than one srv, so it
// cannot take its log target from any single one of them.
func (r *Recorder) Attach(srv *http.Server, ln net.Listener) net.Listener {
	att := newAttachment(r, srv)

	previous := srv.ConnState
	srv.ConnState = func(c net.Conn, state http.ConnState) {
		att.observe(c, state)
		if previous != nil {
			previous(c, state)
		}
	}

	srv.Handler = att.annotate(srv.Handler)

	return &recordingListener{inner: ln, att: att}
}
