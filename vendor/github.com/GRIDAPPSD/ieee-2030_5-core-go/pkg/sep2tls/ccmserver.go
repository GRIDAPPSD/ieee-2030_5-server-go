package sep2tls

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"sync"
	"time"

	gotls "github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2tls/gotls"
)

type ccmStateKey struct{}

// NewCCMServerConfig creates a gotls.Config with CCM-8 as primary cipher
// and GCM as fallback for compatibility.
//
// Equivalent to NewCCMServerConfigWithExtraCAs with no extra roots.
func NewCCMServerConfig(certFile, keyFile, caFile string) (*gotls.Config, error) {
	return NewCCMServerConfigWithExtraCAs(certFile, keyFile, caFile, nil)
}

// NewCCMServerConfigWithExtraCAs is like NewCCMServerConfig but appends
// additional client-CA roots from extraCAFiles into the ClientCAs pool.
// See NewServerTLSConfigWithExtraCAs (config.go) for the multi-root
// rationale and slice semantics.
func NewCCMServerConfigWithExtraCAs(certFile, keyFile, caFile string, extraCAFiles []string) (*gotls.Config, error) {
	certPEM, err := os.ReadFile(certFile)
	if err != nil {
		return nil, fmt.Errorf("read cert: %w", err)
	}
	keyPEM, err := os.ReadFile(keyFile)
	if err != nil {
		return nil, fmt.Errorf("read key: %w", err)
	}

	cert, err := gotls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return nil, fmt.Errorf("parse cert: %w", err)
	}

	caPool, err := LoadClientCAs(caFile, extraCAFiles)
	if err != nil {
		return nil, fmt.Errorf("load client CAs: %w", err)
	}

	return &gotls.Config{
		Certificates: []gotls.Certificate{cert},
		ClientCAs:    caPool,
		// IEEE 2030.5 section 6.11 / CSIP section 6.2 device certs carry a critical
		// HardwareModuleName SAN that stdlib x509 leaves in
		// UnhandledCriticalExtensions, which would cause RequireAndVerify
		// to fail closed at handshake. RequireAnyClientCert is intentional,
		// not a weakening: the full chain walk (signature, expiry, basic
		// constraints, key usage, trust anchor) runs in VerifyPeerCertificate
		// below via VerifyPeerCertWithHardwareModuleSAN, after the HMN OID
		// is acknowledged. See pkg/sep2tls/verify.go and tests
		// TestVerifyRejectsCertSignedByDifferentCA, TestMutualTLSHandshake.
		ClientAuth: gotls.RequireAnyClientCert,
		VerifyPeerCertificate: func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
			return VerifyPeerCertWithHardwareModuleSAN(rawCerts, caPool)
		},
		// IEEE 2030.5-2018 clauses 6.1 and 6.4 (and IEEE 2030.5-2023) specify
		// TLS 1.2; no server configuration accepts TLS 1.3. No exported
		// field, option, or environment variable raises MaxVersion. CCM-8 is
		// ranked ahead of GCM in the fork's preference order (cipher_suites_ccm.go),
		// so it wins when a client offers both.
		MinVersion: gotls.VersionTLS12,
		MaxVersion: gotls.VersionTLS12,
		CipherSuites: []uint16{
			gotls.TLS_ECDHE_ECDSA_WITH_AES_128_CCM_8,
			0xC02B, // TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256 (fallback)
		},
		CurvePreferences: []gotls.CurveID{gotls.CurveP256},
		// Tickets off: a resumed session skips VerifyPeerCertificate above,
		// bypassing the HardwareModuleName SAN check.
		SessionTicketsDisabled: true,
	}, nil
}

// SetupCCMServer configures an http.Server to work with gotls listeners.
// It uses ConnContext to inject the gotls connection state into each request's
// context, and a middleware can then populate r.TLS from it.
func SetupCCMServer(srv *http.Server) {
	srv.ConnContext = func(ctx context.Context, c net.Conn) context.Context {
		if gc, ok := c.(*gotls.Conn); ok {
			return context.WithValue(ctx, ccmStateKey{}, gc)
		}
		return ctx
	}
}

// CCMIdentityMiddleware extracts peer certificates from a gotls connection
// and populates r.TLS so standard identity middleware works.
// Must wrap handlers BEFORE the identity middleware.
func CCMIdentityMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.TLS != nil {
			// Already populated (crypto/tls path)
			next.ServeHTTP(w, r)
			return
		}

		gc, ok := r.Context().Value(ccmStateKey{}).(*gotls.Conn)
		if !ok {
			next.ServeHTTP(w, r)
			return
		}

		state := gc.ConnectionState()
		r.TLS = &tls.ConnectionState{
			Version:           state.Version,
			HandshakeComplete: state.HandshakeComplete,
			CipherSuite:       state.CipherSuite,
			ServerName:        state.ServerName,
		}

		r.TLS.PeerCertificates = append(r.TLS.PeerCertificates, state.PeerCertificates...)

		next.ServeHTTP(w, r)
	})
}

// ccmHandshakeTimeout bounds the per-connection handshake WrapCCMListener
// runs, so a peer that opens the TCP connection and never speaks TLS cannot
// hold a goroutine indefinitely. A var, not a const, so export_test.go can
// shrink it for a test; production code never assigns to it.
var ccmHandshakeTimeout = 10 * time.Second

// WrapCCMListener wraps a gotls listener so a handshake failure is logged
// the way net/http logs one for *tls.Conn (net/http's own "TLS handshake
// error" case in its Serve dispatch). That case never fires for *gotls.Conn:
// it is a different concrete type, so net/http leaves the handshake to run
// lazily on the connection's first Read, and a failure there reaches no log
// line. Pass the serving http.Server's ErrorLog and serve the returned
// listener in place of inner. A nil errorLog logs through the standard
// logger, as net/http does when ErrorLog is nil.
//
// A temporary Accept error is returned to the caller and accepting continues,
// so net/http's retry works. Close cancels handshakes in flight and returns
// once every goroutine the listener started has exited.
func WrapCCMListener(inner net.Listener, errorLog *log.Logger) net.Listener {
	if errorLog == nil {
		errorLog = log.Default()
	}
	ctx, cancel := context.WithCancel(context.Background())
	l := &ccmLoggingListener{
		Listener: inner,
		errorLog: errorLog,
		conns:    make(chan net.Conn),
		errs:     make(chan error),
		stopped:  make(chan struct{}),
		done:     ctx.Done(),
		cancel:   cancel,
	}
	l.wg.Add(1)
	go l.acceptLoop(ctx)
	return l
}

type ccmLoggingListener struct {
	net.Listener
	errorLog *log.Logger

	conns chan net.Conn
	errs  chan error // temporary Accept errors, for the caller to retry

	// stopped is closed when acceptLoop returns; acceptErr is written before.
	stopped   chan struct{}
	acceptErr error

	done   <-chan struct{}
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

func (l *ccmLoggingListener) acceptLoop(ctx context.Context) {
	defer l.wg.Done()
	defer close(l.stopped)
	for {
		c, err := l.Listener.Accept()
		if err == nil {
			l.wg.Add(1)
			go l.handshake(ctx, c)
			continue
		}
		if ctx.Err() == nil && isTemporary(err) {
			select {
			case l.errs <- err:
				continue
			case <-ctx.Done():
			}
		}
		if ctx.Err() != nil {
			err = net.ErrClosed
		}
		l.acceptErr = err
		return
	}
}

// isTemporary matches the errors net/http's Serve loop retries, such as
// EMFILE, using the same non-unwrapping check.
func isTemporary(err error) bool {
	te, ok := err.(interface{ Temporary() bool })
	return ok && te.Temporary()
}

func (l *ccmLoggingListener) handshake(ctx context.Context, c net.Conn) {
	defer l.wg.Done()
	if gc, ok := c.(*gotls.Conn); ok {
		hsCtx, cancel := context.WithTimeout(ctx, ccmHandshakeTimeout)
		err := gc.HandshakeContext(hsCtx)
		cancel()
		if err != nil {
			// A handshake cut short by Close is not the peer's failure.
			if ctx.Err() == nil {
				l.errorLog.Printf("http: TLS handshake error from %s: %v", c.RemoteAddr(), err)
			}
			_ = c.Close()
			return
		}
	}
	select {
	case l.conns <- c:
	case <-ctx.Done():
		_ = c.Close()
	}
}

func (l *ccmLoggingListener) Accept() (net.Conn, error) {
	select {
	case c := <-l.conns:
		// Both select cases can be ready after Close; never hand out a
		// connection once the listener is closed.
		select {
		case <-l.done:
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

// Close stops accepting, cancels handshakes in flight, closes connections
// not yet returned by Accept, and waits for the listener's goroutines.
func (l *ccmLoggingListener) Close() error {
	l.cancel()
	err := l.Listener.Close()
	l.wg.Wait()
	return err
}
