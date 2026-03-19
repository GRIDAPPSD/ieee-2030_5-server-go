package server

import (
	"context"
	"crypto/tls"
	"fmt"
	"log"
	"net"
	"net/http"

	"github.com/craig8/ieee-2030_5-go/internal/certs"
	"github.com/craig8/ieee-2030_5-go/internal/config"
	"github.com/craig8/ieee-2030_5-go/internal/handler"
	sepTLS "github.com/craig8/ieee-2030_5-go/internal/tls"
	"github.com/craig8/ieee-2030_5-go/pkg/store/memory"
)

// Run starts the IEEE 2030.5 server with mutual TLS and optionally
// an admin HTTPS server on a separate port.
func Run(ctx context.Context, cfg *config.Config, svc *handler.AdminCertService) error {
	tlsCfg, err := sepTLS.NewServerTLSConfig(cfg.CertFile, cfg.KeyFile, cfg.CAFile)
	if err != nil {
		return fmt.Errorf("TLS config: %w", err)
	}

	// Compute server identity from its own certificate
	serverSFDI, serverLFDI := "", ""
	if len(tlsCfg.Certificates) > 0 {
		leaf := tlsCfg.Certificates[0]
		if leaf.Leaf != nil {
			serverSFDI = sepTLS.SFDI(leaf.Leaf)
			serverLFDI = sepTLS.LFDI(leaf.Leaf)
		}
	}

	// Initialize stores
	stores := &Stores{
		EndDevices: memory.NewEndDeviceStore(),
	}

	router := NewRouter(cfg, stores, svc, serverSFDI, serverLFDI)

	listener, err := net.Listen("tcp", cfg.Addr)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	defer listener.Close()

	tlsListener := tls.NewListener(listener, tlsCfg)
	protocolSrv := &http.Server{Handler: router}

	errCh := make(chan error, 2)

	go func() {
		log.Printf("IEEE 2030.5 protocol server listening on %s (mTLS)", cfg.Addr)
		errCh <- protocolSrv.Serve(tlsListener)
	}()

	var adminSrv *http.Server
	if cfg.AdminAddr != "" && svc != nil {
		adminSrv, err = startAdminServer(cfg, svc, errCh)
		if err != nil {
			protocolSrv.Close()
			return fmt.Errorf("admin server: %w", err)
		}
	}

	select {
	case <-ctx.Done():
		log.Println("shutting down servers...")
		protocolSrv.Shutdown(context.Background())
		if adminSrv != nil {
			adminSrv.Shutdown(context.Background())
		}
		return nil
	case err := <-errCh:
		return err
	}
}

func startAdminServer(cfg *config.Config, svc *handler.AdminCertService, errCh chan error) (*http.Server, error) {
	adminCertPEM, adminKeyPEM, err := certs.GenerateSelfSignedTLS([]string{"localhost", "127.0.0.1", "::1"})
	if err != nil {
		return nil, fmt.Errorf("generate admin TLS cert: %w", err)
	}

	adminTLSCert, err := tls.X509KeyPair(adminCertPEM, adminKeyPEM)
	if err != nil {
		return nil, fmt.Errorf("parse admin TLS cert: %w", err)
	}

	adminTLSCfg := &tls.Config{
		Certificates: []tls.Certificate{adminTLSCert},
		MinVersion:   tls.VersionTLS12,
	}

	adminRouter := NewAdminRouter(cfg.AdminKey, svc)

	adminListener, err := net.Listen("tcp", cfg.AdminAddr)
	if err != nil {
		return nil, fmt.Errorf("admin listen: %w", err)
	}

	adminTLSListener := tls.NewListener(adminListener, adminTLSCfg)
	adminSrv := &http.Server{Handler: adminRouter}

	go func() {
		log.Printf("Admin HTTPS server listening on %s (self-signed TLS)", cfg.AdminAddr)
		if cfg.AdminKey != "" {
			log.Println("Admin API key configured")
		} else {
			log.Println("WARNING: No admin API key set (SEP2_ADMIN_KEY). Bearer auth disabled.")
		}
		errCh <- adminSrv.Serve(adminTLSListener)
	}()

	return adminSrv, nil
}
