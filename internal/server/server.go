package server

import (
	"context"
	"crypto/tls"
	"fmt"
	"log"
	"net"
	"net/http"

	"strconv"
	"strings"

	"github.com/craig8/ieee-2030_5-go/internal/certs"
	"github.com/craig8/ieee-2030_5-go/internal/config"
	"github.com/craig8/ieee-2030_5-go/internal/discovery"
	"github.com/craig8/ieee-2030_5-go/internal/handler"
	sepTLS "github.com/craig8/ieee-2030_5-go/internal/tls"
	gotls "github.com/craig8/ieee-2030_5-go/internal/tls/gotls"
	"github.com/craig8/ieee-2030_5-go/pkg/sep2"
	"github.com/craig8/ieee-2030_5-go/pkg/store/memory"
)

// Run starts the IEEE 2030.5 server with mutual TLS and optionally
// an admin HTTPS server on a separate port.
func Run(ctx context.Context, cfg *config.Config, svc *handler.AdminCertService) error {
	// Compute server identity from certificate
	serverSFDI, serverLFDI := "", ""

	// Initialize stores
	stores := &Stores{
		EndDevices:          memory.NewEndDeviceStore(),
		MirrorUsagePoints:   memory.NewStore[sep2.MirrorUsagePoint](),
		MirrorMeterReadings: memory.NewScopedStore[sep2.MirrorMeterReading](),
		DERs:               memory.NewScopedStore[sep2.DER](),
		DERCapabilities:    memory.NewScopedStore[sep2.DERCapability](),
		DERSettings:        memory.NewScopedStore[sep2.DERSettings](),
		DERStatuses:        memory.NewScopedStore[sep2.DERStatus](),
		DERAvailabilities:  memory.NewScopedStore[sep2.DERAvailability](),
		DERPrograms:        memory.NewScopedStore[sep2.DERProgram](),
		DERControls:        memory.NewScopedStore[sep2.DERControl](),
		DefaultDERControls: memory.NewScopedStore[sep2.DefaultDERControl](),
		DERCurves:          memory.NewStore[sep2.DERCurve](),
		FSAs:               memory.NewScopedStore[sep2.FunctionSetAssignments](),
		Subscriptions:      memory.NewSubscriptionStore(),
	}

	router := NewRouter(cfg, stores, svc, serverSFDI, serverLFDI)

	listener, err := net.Listen("tcp", cfg.Addr)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	defer listener.Close()

	var tlsListener net.Listener
	protocolSrv := &http.Server{}

	if cfg.EnableCCM {
		// CCM-8 mode: use forked crypto/tls with IEEE 2030.5 mandatory cipher
		ccmCfg, err := sepTLS.NewCCMServerConfig(cfg.CertFile, cfg.KeyFile, cfg.CAFile)
		if err != nil {
			return fmt.Errorf("CCM TLS config: %w", err)
		}
		tlsListener = gotls.NewListener(listener, ccmCfg)

		// Bridge: inject gotls connection state into request context
		sepTLS.SetupCCMServer(protocolSrv)
		protocolSrv.Handler = sepTLS.CCMIdentityMiddleware(router)

		log.Printf("IEEE 2030.5 server listening on %s (mTLS, CCM-8 primary)", cfg.Addr)
	} else {
		// GCM fallback mode: standard crypto/tls
		tlsCfg, err := sepTLS.NewServerTLSConfig(cfg.CertFile, cfg.KeyFile, cfg.CAFile)
		if err != nil {
			return fmt.Errorf("TLS config: %w", err)
		}

		// Compute server identity
		if len(tlsCfg.Certificates) > 0 {
			leaf := tlsCfg.Certificates[0]
			if leaf.Leaf != nil {
				serverSFDI = sepTLS.SFDI(leaf.Leaf)
				serverLFDI = sepTLS.LFDI(leaf.Leaf)
			}
		}

		tlsListener = tls.NewListener(listener, tlsCfg)
		protocolSrv.Handler = router

		log.Printf("IEEE 2030.5 server listening on %s (mTLS, GCM)", cfg.Addr)
	}

	errCh := make(chan error, 2)

	go func() {
		errCh <- protocolSrv.Serve(tlsListener)
	}()

	// Start mDNS if configured
	if cfg.EnableMDNS {
		mdnsReg, err := discovery.Register(discovery.Config{
			Hostname: cfg.MDNSHost,
			Port:     parsePort(cfg.Addr),
			Path:     "/dcap",
		})
		if err != nil {
			log.Printf("mDNS registration failed: %v (continuing without mDNS)", err)
		} else {
			defer mdnsReg.Close()
		}
	}

	// Start admin HTTPS server if configured
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

func parsePort(addr string) int {
	if idx := strings.LastIndex(addr, ":"); idx >= 0 {
		if port, err := strconv.Atoi(addr[idx+1:]); err == nil {
			return port
		}
	}
	return 443
}
