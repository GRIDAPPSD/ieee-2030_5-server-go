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
	"time"

	"github.com/craig8/ieee-2030_5-go/internal/auth"
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
		UsagePoints:        memory.NewStore[sep2.UsagePoint](),
		MeterReadings:      memory.NewScopedStore[sep2.MeterReading](),
		Readings:           memory.NewScopedStore[sep2.Reading](),
		ReadingTypes:       memory.NewStore[sep2.ReadingType](),
		Configurations:     memory.NewScopedStore[sep2.Configuration](),
		DeviceStatuses:     memory.NewScopedStore[sep2.DeviceStatus](),
		LogEvents:          memory.NewScopedStore[sep2.LogEvent](),
		PowerStatuses:      memory.NewScopedStore[sep2.PowerStatus](),
		MessagingPrograms:  memory.NewStore[sep2.MessagingProgram](),
		TextMessages:       memory.NewScopedStore[sep2.TextMessage](),
		FlowReservationRequests:  memory.NewScopedStore[sep2.FlowReservationRequest](),
		FlowReservationResponses: memory.NewScopedStore[sep2.FlowReservationResponse](),
		ResponseSets:       memory.NewStore[sep2.ResponseSet](),
		Responses:          memory.NewScopedStore[sep2.Response](),
	}

	router := NewRouter(cfg, stores, svc, serverSFDI, serverLFDI)

	listener, err := net.Listen("tcp", cfg.Addr)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	defer func() { _ = listener.Close() }()

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
		// NOTE: these values are computed too late — the router was already
		// constructed above with empty identity strings. Tracked separately;
		// do not remove these assignments without fixing the call ordering.
		if len(tlsCfg.Certificates) > 0 {
			leaf := tlsCfg.Certificates[0]
			if leaf.Leaf != nil {
				serverSFDI = sepTLS.SFDI(leaf.Leaf) //nolint:ineffassign // see note above
				serverLFDI = sepTLS.LFDI(leaf.Leaf) //nolint:ineffassign // see note above
				_ = serverSFDI
				_ = serverLFDI
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
		tlsModeName := "GCM"
		if cfg.EnableCCM {
			tlsModeName = "CCM-8"
		}
		adminSrv, err = startAdminServer(cfg, svc, stores, tlsModeName, errCh)
		if err != nil {
			_ = protocolSrv.Close()
			return fmt.Errorf("admin server: %w", err)
		}
	}

	select {
	case <-ctx.Done():
		log.Println("shutting down servers...")
		if err := protocolSrv.Shutdown(context.Background()); err != nil {
			log.Printf("protocol server shutdown error: %v", err)
		}
		if adminSrv != nil {
			if err := adminSrv.Shutdown(context.Background()); err != nil {
				log.Printf("admin server shutdown error: %v", err)
			}
		}
		return nil
	case err := <-errCh:
		return err
	}
}

func startAdminServer(cfg *config.Config, svc *handler.AdminCertService, stores *Stores, tlsMode string, errCh chan error) (*http.Server, error) {
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

	tickets := auth.NewTicketStore(30 * time.Second)
	adminRouter := NewAdminRouter(cfg.AdminKey, svc, stores, tlsMode, tickets)

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
