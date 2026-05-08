// Package server is the public IEEE 2030.5 server lifecycle API.
//
// Use New to construct a Server from a Config. Start blocks the caller
// until the server stops, either because Shutdown was called or because
// the context passed to Start was cancelled. Shutdown initiates a
// graceful stop and returns when both the protocol listener and the
// optional admin listener have closed.
//
// This package is the embedding surface consumed by the
// gridappsd-2030_5-go bridge. The internal building blocks
// (router, dashboard, schedulers) live in internal/server and are
// reachable only through this Server type.
package server

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"net/http"
	"os"
	"sync"

	"github.com/GRIDAPPSD/ieee-2030_5-go/internal/config"
	"github.com/GRIDAPPSD/ieee-2030_5-go/internal/handler"
	intsrv "github.com/GRIDAPPSD/ieee-2030_5-go/internal/server"
	"github.com/GRIDAPPSD/ieee-2030_5-go/pkg/certs"
)

// Config configures an IEEE 2030.5 Server.
//
// Required fields: Addr, CertFile, KeyFile, CAFile.
// Optional: AdminAddr (enables admin HTTPS listener), AdminKey,
// EnableCCM, EnableMDNS, MDNSHost, time-related fields.
type Config struct {
	// Addr is the protocol listener bind address (e.g. ":443" or
	// "127.0.0.1:8443").
	Addr string
	// CertFile is the path to the server certificate PEM.
	CertFile string
	// KeyFile is the path to the server private-key PEM.
	KeyFile string
	// CAFile is the path to the CA certificate PEM used to verify
	// client certs (mTLS).
	CAFile string

	// AdminAddr is the optional admin HTTPS bind address. Empty
	// disables the admin listener.
	AdminAddr string
	// AdminKey is the bearer token for admin API auth. Empty
	// disables bearer auth on the admin listener.
	AdminKey string

	// CAKeyFile is the optional CA private-key PEM. When set together
	// with CAFile, the admin cert API is enabled to mint device,
	// server, and admin certs against the loaded CA.
	CAKeyFile string

	// EnableCCM selects the CCM-8 cipher (IEEE 2030.5 §6.7) instead
	// of the GCM fallback.
	EnableCCM bool
	// EnableMDNS turns on mDNS service advertisement.
	EnableMDNS bool
	// MDNSHost is the hostname advertised over mDNS.
	MDNSHost string

	// TZOffset is the timezone offset from UTC in seconds, served
	// from /tm.
	TZOffset int32
	// DSTOffset is the DST offset in seconds.
	DSTOffset int32
	// DSTStart is the DST start in unix seconds.
	DSTStart int64
	// DSTEnd is the DST end in unix seconds.
	DSTEnd int64
	// TimeQuality is the IEEE 2030.5 TimeQualityType per spec §9.2.
	TimeQuality uint8
}

// Server is an IEEE 2030.5 protocol server with optional admin HTTPS
// listener. The zero value is not usable; construct with New.
//
// A Server is single-shot: after Start returns or Shutdown is called,
// construct a new Server via New to restart.
type Server struct {
	cfg Config
	svc *handler.AdminCertService

	mu       sync.Mutex
	cancel   context.CancelFunc
	doneCh   chan struct{}
	startErr error
}

// New constructs a Server from cfg. It validates that the cert files
// exist and parses the optional CA key, but does not bind listeners.
// Listeners bind on Start.
//
// CA-key handling:
//   - cfg.CAKeyFile == "": admin cert minting is silently disabled.
//   - cfg.CAKeyFile != "": the file MUST load and parse cleanly. A
//     load failure returns an error rather than silently disabling the
//     admin cert API (which would mask misconfiguration).
func New(cfg Config) (*Server, error) {
	if err := validateConfig(cfg); err != nil {
		return nil, err
	}

	var svc *handler.AdminCertService
	if cfg.CAKeyFile != "" {
		s, err := loadAdminCertService(cfg)
		if err != nil {
			return nil, fmt.Errorf("admin cert service: %w", err)
		}
		svc = s
	}

	return &Server{
		cfg: cfg,
		svc: svc,
	}, nil
}

// Start binds the listeners and serves traffic. It blocks until ctx
// is cancelled, Shutdown is called, or the underlying server returns
// an error.
//
// Start returns nil on clean shutdown (ctx cancelled or Shutdown
// called); a non-nil return means the listener failed unexpectedly.
// context.Canceled and http.ErrServerClosed are treated as clean
// shutdown sentinels and filtered out.
//
// Start is not safe to call concurrently; it returns an error if the
// server has already been started.
func (s *Server) Start(ctx context.Context) error {
	s.mu.Lock()
	if s.cancel != nil {
		s.mu.Unlock()
		return errors.New("server already started")
	}
	internalCtx, cancel := context.WithCancel(ctx)
	s.cancel = cancel
	s.doneCh = make(chan struct{})
	s.mu.Unlock()

	defer func() {
		s.mu.Lock()
		close(s.doneCh)
		s.mu.Unlock()
	}()

	err := intsrv.Run(internalCtx, s.toInternalConfig(), s.svc)
	if err != nil &&
		!errors.Is(err, context.Canceled) &&
		!errors.Is(err, http.ErrServerClosed) {
		s.mu.Lock()
		s.startErr = err
		s.mu.Unlock()
		return err
	}
	return nil
}

// Shutdown initiates a graceful stop and returns when Start has
// returned or shutCtx is cancelled. It is safe to call Shutdown before
// Start (it becomes a no-op) and from any goroutine.
func (s *Server) Shutdown(shutCtx context.Context) error {
	s.mu.Lock()
	cancel := s.cancel
	doneCh := s.doneCh
	s.mu.Unlock()

	if cancel == nil {
		return nil
	}

	cancel()

	if doneCh == nil {
		return nil
	}

	select {
	case <-doneCh:
		s.mu.Lock()
		err := s.startErr
		s.mu.Unlock()
		if err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	case <-shutCtx.Done():
		return shutCtx.Err()
	}
}

func (s *Server) toInternalConfig() *config.Config {
	return &config.Config{
		Addr:        s.cfg.Addr,
		CertFile:    s.cfg.CertFile,
		KeyFile:     s.cfg.KeyFile,
		CAFile:      s.cfg.CAFile,
		AdminAddr:   s.cfg.AdminAddr,
		AdminKey:    s.cfg.AdminKey,
		TZOffset:    s.cfg.TZOffset,
		DSTOffset:   s.cfg.DSTOffset,
		DSTStart:    s.cfg.DSTStart,
		DSTEnd:      s.cfg.DSTEnd,
		TimeQuality: s.cfg.TimeQuality,
		EnableCCM:   s.cfg.EnableCCM,
		EnableMDNS:  s.cfg.EnableMDNS,
		MDNSHost:    s.cfg.MDNSHost,
	}
}

func validateConfig(cfg Config) error {
	if cfg.Addr == "" {
		return errors.New("addr is required")
	}
	for label, path := range map[string]string{
		"certFile": cfg.CertFile,
		"keyFile":  cfg.KeyFile,
		"caFile":   cfg.CAFile,
	} {
		if path == "" {
			return fmt.Errorf("%s is required", label)
		}
		if _, err := os.Stat(path); err != nil {
			return fmt.Errorf("%s %q: %w", label, path, err)
		}
	}
	return nil
}

func loadAdminCertService(cfg Config) (*handler.AdminCertService, error) {
	if cfg.CAFile == "" || cfg.CAKeyFile == "" {
		return nil, errors.New("CA key not configured")
	}
	caCert, caKey, err := certs.LoadCA(cfg.CAFile, cfg.CAKeyFile)
	if err != nil {
		return nil, err
	}
	caCertPEM, err := os.ReadFile(cfg.CAFile)
	if err != nil {
		return nil, err
	}
	// Sanity check: confirm the loaded cert parses as ECDSA P-256.
	if caCert.PublicKeyAlgorithm != x509.ECDSA {
		return nil, errors.New("CA is not ECDSA")
	}
	return handler.NewAdminCertService(caCert, caKey, caCertPEM), nil
}
