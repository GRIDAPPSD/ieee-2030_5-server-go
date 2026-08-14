// Package sep2srv provides a Server lifecycle wrapper for the IEEE 2030.5
// protocol listener: mutual-TLS termination (CCM-8 primary, GCM fallback),
// server-identity (SFDI/LFDI) derivation from the leaf certificate, and
// graceful shutdown bound to a caller-supplied context.
//
// This is the GENERIC half of the reference server's internal/server.Run:
// admin router, dashboard, login, host-allowlist, connection banner, and
// metrics listener are consumer concerns and stay out of core. A consumer
// builds the protocol http.Handler (typically via
// github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/assembly.BuildProtocolRouter)
// inside the HandlerFunc passed to New, so the handler can be constructed
// with the derived Identity already known (needed for /sdev and /sdev/sdi).
package sep2srv

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/http"
	"time"

	sepTLS "github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2tls"
	gotls "github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2tls/gotls"
)

// HTTP server timeout defaults for the protocol listener. Zero means "no
// limit"; these non-zero values defend against Slowloris and slow-body
// exhaustion. ReadHeaderTimeout < ReadTimeout: header parsing has a tighter
// deadline than the full body read. Verbatim values from the reference
// server's internal/server.newProtocolServer.
const (
	DefaultReadHeaderTimeout = 10 * time.Second
	DefaultReadTimeout       = 30 * time.Second
	DefaultWriteTimeout      = 30 * time.Second
	DefaultIdleTimeout       = 120 * time.Second

	// DefaultShutdownTimeout bounds Server.Run's graceful drain after the
	// caller's context is cancelled. This is a deliberate hardening over the
	// reference server's Run, which calls Shutdown with an unbounded
	// context.Background(); a bounded drain here guarantees Run returns and
	// the Serve goroutine is never left running past a fixed grace period.
	DefaultShutdownTimeout = 5 * time.Second
)

// Options configures a protocol Server's mTLS listener.
type Options struct {
	// Addr is the "host:port" the protocol listener binds.
	Addr string

	// CertFile, KeyFile, CAFile name the server's leaf cert/key and the
	// trusted client-CA bundle. Required.
	CertFile string
	KeyFile  string
	CAFile   string

	// ExtraClientCAs names additional client-CA bundles trusted alongside
	// CAFile, for multi-root device-cert trust.
	ExtraClientCAs []string

	// EnableCCM selects the CCM-8 mandatory cipher suite via the forked
	// crypto/tls in pkg/sep2tls/gotls (IEEE 2030.5-2018 section 6.7). False
	// serves the stdlib GCM fallback.
	EnableCCM bool

	// ShutdownTimeout bounds Run's graceful drain after ctx is cancelled.
	// Zero uses DefaultShutdownTimeout.
	ShutdownTimeout time.Duration
}

// Identity is the server's SFDI and LFDI, derived from the leaf certificate
// named by Options.CertFile.
type Identity struct {
	SFDI string
	LFDI string
}

// HandlerFunc builds the protocol http.Handler once the server's mTLS
// Identity is known. Most callers pass a closure around
// assembly.BuildProtocolRouter that threads the identity into /sdev's
// self-device resource. build must not be nil and must return a non-nil
// Handler.
type HandlerFunc func(Identity) http.Handler

// Server is a mutual-TLS IEEE 2030.5 protocol listener with graceful
// shutdown. Construct with New; start with Run.
type Server struct {
	// Identity is the SFDI/LFDI derived from the leaf certificate during
	// New. Read-only after construction.
	Identity Identity

	listener        net.Listener
	httpSrv         *http.Server
	shutdownTimeout time.Duration
}

// New builds the mTLS listener, derives the server Identity from the leaf
// certificate, and calls build to obtain the protocol http.Handler.
//
// The ordering is load-bearing: Identity must be known BEFORE build runs,
// because /sdev and /sdev/sdi need non-empty SFDI/LFDI values and those are
// not available until the leaf certificate has been parsed. On any error,
// New closes any listener it opened; callers never need to clean up a
// partially-constructed Server.
func New(opts Options, build HandlerFunc) (*Server, error) {
	if opts.Addr == "" {
		return nil, errors.New("sep2srv: Options.Addr is required")
	}
	if build == nil {
		return nil, errors.New("sep2srv: build is required")
	}

	listener, err := net.Listen("tcp", opts.Addr)
	if err != nil {
		return nil, fmt.Errorf("sep2srv: listen: %w", err)
	}

	tlsListener, identity, err := wrapMTLS(listener, opts)
	if err != nil {
		_ = listener.Close()
		return nil, err
	}

	handler := build(identity)
	if handler == nil {
		_ = tlsListener.Close()
		return nil, errors.New("sep2srv: build returned a nil Handler")
	}

	httpSrv := &http.Server{
		ReadHeaderTimeout: DefaultReadHeaderTimeout,
		ReadTimeout:       DefaultReadTimeout,
		WriteTimeout:      DefaultWriteTimeout,
		IdleTimeout:       DefaultIdleTimeout,
	}

	if opts.EnableCCM {
		// Bridge: inject gotls connection state into request context so
		// standard identity middleware (and handlers reading r.TLS) work
		// the same under CCM as under GCM.
		sepTLS.SetupCCMServer(httpSrv)
		httpSrv.Handler = sepTLS.CCMIdentityMiddleware(handler)
	} else {
		httpSrv.Handler = handler
	}

	shutdownTimeout := opts.ShutdownTimeout
	if shutdownTimeout <= 0 {
		shutdownTimeout = DefaultShutdownTimeout
	}

	return &Server{
		Identity:        identity,
		listener:        tlsListener,
		httpSrv:         httpSrv,
		shutdownTimeout: shutdownTimeout,
	}, nil
}

// Addr returns the listener's actual bound address. Useful when Options.Addr
// used a ":0" port and the caller needs to know which port the OS assigned.
func (s *Server) Addr() string {
	return s.listener.Addr().String()
}

// wrapMTLS builds the CCM or GCM TLS config per opts.EnableCCM, wraps
// listener in the corresponding TLS listener, and derives the server
// Identity from the resulting leaf certificate. listener is never closed
// here; the caller owns that on error.
func wrapMTLS(listener net.Listener, opts Options) (net.Listener, Identity, error) {
	if opts.EnableCCM {
		ccmCfg, err := sepTLS.NewCCMServerConfigWithExtraCAs(opts.CertFile, opts.KeyFile, opts.CAFile, opts.ExtraClientCAs)
		if err != nil {
			return nil, Identity{}, fmt.Errorf("sep2srv: CCM TLS config: %w", err)
		}
		identity, err := deriveIdentity(ccmCfg.Certificates[0].Certificate)
		if err != nil {
			return nil, Identity{}, fmt.Errorf("sep2srv: derive server identity (CCM): %w", err)
		}
		return gotls.NewListener(listener, ccmCfg), identity, nil
	}

	tlsCfg, err := sepTLS.NewServerTLSConfigWithExtraCAs(opts.CertFile, opts.KeyFile, opts.CAFile, opts.ExtraClientCAs)
	if err != nil {
		return nil, Identity{}, fmt.Errorf("sep2srv: TLS config: %w", err)
	}
	identity, err := deriveIdentity(tlsCfg.Certificates[0].Certificate)
	if err != nil {
		return nil, Identity{}, fmt.Errorf("sep2srv: derive server identity (GCM): %w", err)
	}
	return tls.NewListener(listener, tlsCfg), identity, nil
}

// deriveIdentity parses the leaf certificate from a raw DER chain (as found
// in tls.Certificate.Certificate / gotls.Certificate.Certificate) and
// returns the server SFDI and LFDI. Mode-agnostic: works for both the
// stdlib crypto/tls path (GCM) and the forked gotls path (CCM).
func deriveIdentity(rawChain [][]byte) (Identity, error) {
	if len(rawChain) == 0 {
		return Identity{}, errors.New("empty certificate chain")
	}
	leaf, err := x509.ParseCertificate(rawChain[0])
	if err != nil {
		return Identity{}, fmt.Errorf("parse server leaf: %w", err)
	}
	return Identity{
		SFDI: sepTLS.SFDI(leaf),
		LFDI: sepTLS.LFDI(leaf),
	}, nil
}

// Run serves the protocol listener until ctx is cancelled or Serve fails,
// then shuts the server down gracefully within the configured
// ShutdownTimeout. Run blocks until shutdown completes (or the listener
// fails) and returns nil on a clean ctx-triggered shutdown. The listener
// goroutine always exits before Run returns: Shutdown closes the listener,
// which unblocks Serve, which sends on the buffered error channel and
// exits.
func (s *Server) Run(ctx context.Context) error {
	errCh := make(chan error, 1)
	go func() {
		errCh <- s.httpSrv.Serve(s.listener)
	}()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), s.shutdownTimeout)
		defer cancel()
		if err := s.httpSrv.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("sep2srv: shutdown: %w", err)
		}
		return nil
	case err := <-errCh:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("sep2srv: serve: %w", err)
		}
		return err
	}
}
