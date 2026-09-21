// Package sep2capture provides a TLS listener that completes the handshake
// before Accept returns, so a wrapped connection carries client identity the
// same way a bare *tls.Conn or *gotls.Conn would.
//
// net/http fills http.Request.TLS from an accepted connection in one of two
// ways: it runs the handshake itself when the connection is a *tls.Conn, or
// it calls ConnectionState() on any connection implementing that method,
// once, before the first read (net/http/server.go). A connection wrapped in
// a struct that merely embeds net.Conn satisfies neither: the concrete type
// is no longer *tls.Conn, and embedding an interface field promotes only
// that interface's own method set, so ConnectionState never surfaces. Every
// protocol request would then fail identity (internal/auth/identity.go)
// with no client ever reaching a handler. Listener exists to make that
// impossible: it drives the handshake itself and returns a connection whose
// ConnectionState is real.
//
// This package holds only the listener and connection. It records nothing
// and is not wired into any server; a future package attaches recording on
// top of it.
package sep2capture

import (
	"context"
	"crypto/tls"
	"log"
	"net"
	"sync"
	"time"
)

// handshaker is implemented by both *tls.Conn (the GCM listener's output)
// and core's *gotls.Conn (the CCM-8 listener's output). Both run the
// handshake lazily on first Read unless driven explicitly, and both treat a
// second call as a no-op once the handshake is complete, which is what
// makes an already-handshaken input (core's WrapCCMListener output) safe to
// pass through this listener unchanged.
type handshaker interface {
	HandshakeContext(ctx context.Context) error
}

// defaultHandshakeTimeout bounds the per-connection handshake this listener
// drives, so a peer that opens the TCP connection and never speaks TLS
// cannot hold a goroutine indefinitely. Matches core's own
// ccmHandshakeTimeout (vendor/.../sep2tls/ccmserver.go), whose accept-loop
// shape this listener mirrors.
const defaultHandshakeTimeout = 10 * time.Second

// Listener wraps a TLS listener, GCM (crypto/tls) or CCM-8 (core's gotls
// fork), and completes each connection's handshake in its own goroutine
// before handing it to Accept. The net.Conn Accept returns always
// implements ConnectionState() tls.ConnectionState, so net/http populates
// http.Request.TLS exactly as it would for a bare *tls.Conn.
//
// A handshake failure is reported through ErrorLog with the peer address
// and the error, then the connection is closed; Accept's caller never sees
// a failed connection. Close cancels handshakes in flight, closes the inner
// listener, and waits for every goroutine this Listener started before
// returning.
type Listener struct {
	inner    net.Listener
	errorLog *log.Logger
	timeout  time.Duration

	conns chan net.Conn
	errs  chan error // temporary Accept errors, for the caller to retry

	stopped   chan struct{} // closed when acceptLoop returns; acceptErr set before
	acceptErr error

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// NewListener wraps inner, a listener whose Accept returns *tls.Conn or
// *gotls.Conn (handshaken or not). errorLog receives handshake-failure
// reports; nil uses the standard logger, matching net/http's own ErrorLog
// default.
func NewListener(inner net.Listener, errorLog *log.Logger) *Listener {
	if errorLog == nil {
		errorLog = log.Default()
	}
	ctx, cancel := context.WithCancel(context.Background())
	l := &Listener{
		inner:    inner,
		errorLog: errorLog,
		timeout:  defaultHandshakeTimeout,
		conns:    make(chan net.Conn),
		errs:     make(chan error),
		stopped:  make(chan struct{}),
		ctx:      ctx,
		cancel:   cancel,
	}
	l.wg.Add(1)
	go l.acceptLoop()
	return l
}

func (l *Listener) acceptLoop() {
	defer l.wg.Done()
	defer close(l.stopped)
	for {
		c, err := l.inner.Accept()
		if err == nil {
			l.wg.Add(1)
			go l.handshake(c)
			continue
		}
		if l.ctx.Err() == nil && isTemporary(err) {
			select {
			case l.errs <- err:
				continue
			case <-l.ctx.Done():
			}
		}
		if l.ctx.Err() != nil {
			err = net.ErrClosed
		}
		l.acceptErr = err
		return
	}
}

// isTemporary matches the errors net/http's own Serve loop retries, such as
// EMFILE, using the same non-unwrapping check net/http uses.
func isTemporary(err error) bool {
	te, ok := err.(interface{ Temporary() bool })
	return ok && te.Temporary()
}

func (l *Listener) handshake(c net.Conn) {
	defer l.wg.Done()

	hs, ok := c.(handshaker)
	if !ok {
		l.errorLog.Printf("sep2capture: connection from %s is not a TLS connection (%T); refused", c.RemoteAddr(), c)
		_ = c.Close()
		return
	}

	hsCtx, cancel := context.WithTimeout(l.ctx, l.timeout)
	err := hs.HandshakeContext(hsCtx)
	cancel()
	if err != nil {
		// A handshake cut short by Close is not the peer's failure.
		if l.ctx.Err() == nil {
			l.errorLog.Printf("sep2capture: TLS handshake error from %s: %v", c.RemoteAddr(), err)
		}
		_ = c.Close()
		return
	}

	wrapped := &Conn{Conn: c}
	select {
	case l.conns <- wrapped:
	case <-l.ctx.Done():
		_ = c.Close()
	}
}

// Accept implements net.Listener. It blocks until a connection has
// completed its TLS handshake, a temporary Accept error occurs on the inner
// listener, or the Listener stops.
func (l *Listener) Accept() (net.Conn, error) {
	select {
	case c := <-l.conns:
		// Both cases can be ready after Close; never hand out a connection
		// once the listener is closed.
		select {
		case <-l.ctx.Done():
			_ = c.Close()
			return nil, net.ErrClosed
		default:
			return c, nil
		}
	case err := <-l.errs:
		return nil, err
	case <-l.stopped:
		return nil, l.acceptErr
	}
}

// Close stops accepting, cancels handshakes in flight, closes the inner
// listener, and waits for every goroutine this Listener started before
// returning. A connection already queued for Accept but never claimed is
// closed by its own handshake goroutine's ctx.Done() branch above, not
// here: Close never receives from conns, so that branch is always the one
// that fires once cancel has run.
func (l *Listener) Close() error {
	l.cancel()
	err := l.inner.Close()
	l.wg.Wait()
	return err
}

// Addr returns the inner listener's bound address.
func (l *Listener) Addr() net.Addr { return l.inner.Addr() }

var _ net.Listener = (*Listener)(nil)
var _ handshaker = (*tls.Conn)(nil)
