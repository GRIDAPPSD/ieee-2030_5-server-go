package sep2server

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2srv"
	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2srv/assembly"
	sepTLS "github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2tls"
	gotls "github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2tls/gotls"
)

// BuildHandler assembles the protocol handler and returns it alongside the
// canonical list of patterns mounted on the protocol mux.
//
// It binds nothing, so a caller that already owns a listener, or that wants
// to drive the routes over plain HTTP in a test, can use it directly. identity
// is threaded into /sdev and /sdev/sdi; [New] derives it from the leaf
// certificate, and a caller here supplies it.
//
// The composition order, outermost first, is:
//
//	Config.Middleware        (when non-nil)
//	CCM identity middleware  (when Config.EnableCCM)
//	the assembled protocol router
//
// The CCM layer populates r.TLS from the forked connection, so anything that
// needs peer certificates must sit inside it, which is where the router and
// therefore the auth policy already are.
func BuildHandler(cfg Config, identity sep2srv.Identity) (http.Handler, []string) {
	stores := cfg.Stores
	if stores == nil {
		stores = NewStores()
	}

	handler, patterns := assembly.BuildProtocolRouter(
		cfg.Router,
		stores,
		cfg.Auth,
		identity.SFDI, identity.LFDI,
		cfg.Notifier,
	)

	if cfg.EnableCCM {
		handler = sepTLS.CCMIdentityMiddleware(handler)
	}
	if cfg.Middleware != nil {
		handler = cfg.Middleware(handler)
	}
	return handler, patterns
}

// Server is an embedded IEEE 2030.5 protocol listener: mutual TLS, the
// assembled protocol routes, and a graceful drain. Construct with [New], start
// with [Server.Run].
type Server struct {
	identity sep2srv.Identity
	stores   *assembly.Stores
	patterns []string

	listener        net.Listener
	httpSrv         *http.Server
	shutdownTimeout time.Duration
}

// New binds the mutual-TLS listener, derives the server's identity from the
// leaf certificate, and assembles the protocol handler. It does NOT begin
// serving; call [Server.Run] for that.
//
// The ordering is load-bearing and matches core's own: the identity must be
// known BEFORE the handler is built, because /sdev and /sdev/sdi close over
// it and would otherwise serve empty elements.
//
// On any error New closes whatever it opened, so a caller never has to clean
// up a partially constructed Server. A Server that is constructed and then
// never Run still holds its listener; Run is what releases it.
func New(cfg Config) (*Server, error) {
	if cfg.Addr == "" {
		return nil, errors.New("sep2server: Config.Addr is required")
	}
	if cfg.CertFile == "" || cfg.KeyFile == "" || cfg.CAFile == "" {
		return nil, errors.New("sep2server: Config.CertFile, Config.KeyFile and Config.CAFile are all required")
	}
	// Fail closed. Core tolerates a nil Wrap with a log line because its
	// tests need that, but a deployment reaching it by omission would serve
	// the whole protocol surface with no identity extraction and no ACL.
	if cfg.Auth.Wrap == nil {
		return nil, errors.New("sep2server: Config.Auth.Wrap is nil: that would serve the protocol surface with no identity extraction and no ACL enforcement; pass DefaultAuthPolicy or supply your own")
	}
	if cfg.Stores == nil {
		cfg.Stores = NewStores()
	}

	listener, err := net.Listen("tcp", cfg.Addr)
	if err != nil {
		return nil, fmt.Errorf("sep2server: listen: %w", err)
	}

	tlsListener, identity, err := wrapMTLS(listener, cfg)
	if err != nil {
		_ = listener.Close()
		return nil, err
	}

	handler, patterns := BuildHandler(cfg, identity)

	httpSrv := newProtocolServer(handler)

	if cfg.EnableCCM {
		// Threads the forked connection into the request context, which is
		// what CCM identity middleware reads back out.
		sepTLS.SetupCCMServer(httpSrv)
	}
	// Chain rather than clobber: core's CCM setup is free to install its own
	// hook here in a future release, and a consumer's observation must not
	// silently displace it.
	if cfg.ConnState != nil {
		observe, previous := cfg.ConnState, httpSrv.ConnState
		httpSrv.ConnState = func(c net.Conn, state http.ConnState) {
			observe(c, state)
			if previous != nil {
				previous(c, state)
			}
		}
	}

	return &Server{
		identity:        identity,
		stores:          cfg.Stores,
		patterns:        patterns,
		listener:        tlsListener,
		httpSrv:         httpSrv,
		shutdownTimeout: cfg.ShutdownTimeout,
	}, nil
}

// Handler returns the handler the listener serves: the protocol router with
// whatever Config.EnableCCM and Config.Middleware composed around it.
//
// This is the seam for a consumer that wants to mount its own surface
// alongside the protocol routes, or to drive them from a test without
// standing up TLS.
func (s *Server) Handler() http.Handler { return s.httpSrv.Handler }

// Patterns returns the protocol patterns mounted on the router, sorted.
//
// The route surface is part of what this server promises, so it is readable
// rather than something a consumer has to infer by probing. A consumer can
// assert at boot that the routes it depends on are still mounted instead of
// discovering a rename as a 404 in the field.
func (s *Server) Patterns() []string { return s.patterns }

// Stores returns the resource-store handle: the one a consumer seeds a fleet
// through, and injects control through.
//
// This is the WRITE handle. The read-only half of the privilege split, the
// narrowed view a telemetry reader or an administrative read surface should
// hold, is a follow-up: a second accessor returning reader interfaces from
// core's store package. core's ResourceReader and ScopedReader already
// exist, so nothing here has to change to admit it, and no consumer of
// this accessor breaks when it does.
func (s *Server) Stores() *assembly.Stores { return s.stores }

// Identity returns the server's SFDI and LFDI, derived from the leaf
// certificate during [New].
//
// Named fields rather than two strings on purpose: SFDI and LFDI are both
// opaque digit strings, so a positional swap between them is invisible at the
// call site and wrong on the wire.
func (s *Server) Identity() sep2srv.Identity { return s.identity }

// Addr returns the listener's bound address, which is how a caller that asked
// for ":0" learns the port the OS assigned.
func (s *Server) Addr() string { return s.listener.Addr().String() }

// Run serves the protocol listener until ctx is cancelled or the listener
// fails on its own, then drains gracefully.
//
// A cancelled ctx is a clean shutdown and returns nil. Config.ShutdownTimeout
// bounds the drain; zero drains without a bound. A listener that fails for its
// own reasons returns that failure, so a caller that treats a nil return as
// "asked to stop" and a non-nil return as "broke" is reading it correctly.
//
// The Serve goroutine always exits before Run returns on both paths: Shutdown
// closes the listener, which unblocks Serve, which sends on the buffered
// channel and exits.
func (s *Server) Run(ctx context.Context) error {
	errCh := make(chan error, 1)
	go func() {
		errCh <- s.httpSrv.Serve(s.listener)
	}()

	select {
	case <-ctx.Done():
		shutdownCtx := context.Background()
		if s.shutdownTimeout > 0 {
			var cancel context.CancelFunc
			shutdownCtx, cancel = context.WithTimeout(shutdownCtx, s.shutdownTimeout)
			defer cancel()
		}
		if err := s.httpSrv.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("sep2server: shutdown: %w", err)
		}
		return nil
	case err := <-errCh:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("sep2server: serve: %w", err)
		}
		return err
	}
}

// newProtocolServer builds the protocol http.Server with the timeout values
// this project has always used. They are named by core, which lifted them from
// this repository verbatim, so taking them back from core keeps one definition
// rather than two that can drift.
//
// Every one is non-zero on purpose: a zero timeout means no limit, which is a
// Slowloris and slow-body exhaustion surface. ReadHeaderTimeout is tighter
// than ReadTimeout because header parsing should not get the full body budget.
func newProtocolServer(handler http.Handler) *http.Server {
	return &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: sep2srv.DefaultReadHeaderTimeout,
		ReadTimeout:       sep2srv.DefaultReadTimeout,
		WriteTimeout:      sep2srv.DefaultWriteTimeout,
		IdleTimeout:       sep2srv.DefaultIdleTimeout,
	}
}

// wrapMTLS builds the CCM or GCM TLS config, wraps listener in the matching
// TLS listener, and derives the server identity from the resulting leaf.
// listener is never closed here; the caller owns that on error.
func wrapMTLS(listener net.Listener, cfg Config) (net.Listener, sep2srv.Identity, error) {
	if cfg.EnableCCM {
		ccmCfg, err := sepTLS.NewCCMServerConfigWithExtraCAs(cfg.CertFile, cfg.KeyFile, cfg.CAFile, cfg.ExtraClientCAs)
		if err != nil {
			return nil, sep2srv.Identity{}, fmt.Errorf("sep2server: CCM TLS config: %w", err)
		}
		if len(ccmCfg.Certificates) == 0 {
			return nil, sep2srv.Identity{}, errors.New("sep2server: CCM TLS config carries no certificate")
		}
		identity, err := deriveIdentity(ccmCfg.Certificates[0].Certificate)
		if err != nil {
			return nil, sep2srv.Identity{}, fmt.Errorf("sep2server: derive server identity (CCM): %w", err)
		}
		return gotls.NewListener(listener, ccmCfg), identity, nil
	}

	tlsCfg, err := sepTLS.NewServerTLSConfigWithExtraCAs(cfg.CertFile, cfg.KeyFile, cfg.CAFile, cfg.ExtraClientCAs)
	if err != nil {
		return nil, sep2srv.Identity{}, fmt.Errorf("sep2server: TLS config: %w", err)
	}
	if len(tlsCfg.Certificates) == 0 {
		return nil, sep2srv.Identity{}, errors.New("sep2server: TLS config carries no certificate")
	}
	identity, err := deriveIdentity(tlsCfg.Certificates[0].Certificate)
	if err != nil {
		return nil, sep2srv.Identity{}, fmt.Errorf("sep2server: derive server identity (GCM): %w", err)
	}
	return tls.NewListener(listener, tlsCfg), identity, nil
}

// deriveIdentity parses the leaf from a raw DER chain and returns the server
// SFDI and LFDI. Mode-agnostic: the chain has the same shape whether it came
// from crypto/tls or from the fork.
func deriveIdentity(rawChain [][]byte) (sep2srv.Identity, error) {
	if len(rawChain) == 0 {
		return sep2srv.Identity{}, errors.New("empty certificate chain")
	}
	leaf, err := x509.ParseCertificate(rawChain[0])
	if err != nil {
		return sep2srv.Identity{}, fmt.Errorf("parse server leaf: %w", err)
	}
	return sep2srv.Identity{SFDI: sepTLS.SFDI(leaf), LFDI: sepTLS.LFDI(leaf)}, nil
}
