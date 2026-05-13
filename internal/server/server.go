package server

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"log"
	"net"
	"net/http"

	"strconv"
	"strings"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-go/internal/auth"
	"github.com/GRIDAPPSD/ieee-2030_5-go/internal/bootfixture"
	"github.com/GRIDAPPSD/ieee-2030_5-go/internal/certs"
	"github.com/GRIDAPPSD/ieee-2030_5-go/internal/config"
	"github.com/GRIDAPPSD/ieee-2030_5-go/internal/discovery"
	"github.com/GRIDAPPSD/ieee-2030_5-go/internal/handler"
	"github.com/GRIDAPPSD/ieee-2030_5-go/internal/subscription"
	sepTLS "github.com/GRIDAPPSD/ieee-2030_5-go/internal/tls"
	gotls "github.com/GRIDAPPSD/ieee-2030_5-go/internal/tls/gotls"
	"github.com/GRIDAPPSD/ieee-2030_5-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-go/pkg/store/memory"
)

const (
	subscriptionWorkers   = 4
	subscriptionQueueSize = 256
)

// Run starts the IEEE 2030.5 server with mutual TLS and optionally
// an admin HTTPS server on a separate port.
func Run(ctx context.Context, cfg *config.Config, svc *handler.AdminCertService) error {
	// Build TLS config and derive server identity (SFDI/LFDI) from the leaf
	// cert BEFORE constructing the router, so /sdev and /sdev/sdi see
	// non-empty values under both GCM and CCM modes (IEEE-001).
	var (
		tlsListener net.Listener
		serverSFDI  string
		serverLFDI  string
	)
	protocolSrv := &http.Server{}

	listener, err := net.Listen("tcp", cfg.Addr)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	defer func() { _ = listener.Close() }()

	if cfg.EnableCCM {
		// CCM-8 mode: use forked crypto/tls with IEEE 2030.5 mandatory cipher
		ccmCfg, err := sepTLS.NewCCMServerConfigWithExtraCAs(cfg.CertFile, cfg.KeyFile, cfg.CAFile, cfg.ExtraClientCAs)
		if err != nil {
			return fmt.Errorf("CCM TLS config: %w", err)
		}
		serverSFDI, serverLFDI, err = deriveServerIdentity(ccmCfg.Certificates[0].Certificate)
		if err != nil {
			return fmt.Errorf("derive server identity (CCM): %w", err)
		}
		tlsListener = gotls.NewListener(listener, ccmCfg)
		log.Printf("IEEE 2030.5 server listening on %s (mTLS, CCM-8 primary)", cfg.Addr)
	} else {
		// GCM fallback mode: standard crypto/tls
		tlsCfg, err := sepTLS.NewServerTLSConfigWithExtraCAs(cfg.CertFile, cfg.KeyFile, cfg.CAFile, cfg.ExtraClientCAs)
		if err != nil {
			return fmt.Errorf("TLS config: %w", err)
		}
		serverSFDI, serverLFDI, err = deriveServerIdentity(tlsCfg.Certificates[0].Certificate)
		if err != nil {
			return fmt.Errorf("derive server identity (GCM): %w", err)
		}
		tlsListener = tls.NewListener(listener, tlsCfg)
		log.Printf("IEEE 2030.5 server listening on %s (mTLS, GCM)", cfg.Addr)
	}

	if len(cfg.ExtraClientCAs) > 0 {
		log.Printf("trusted extra client CAs: %v", cfg.ExtraClientCAs)
	}

	// IEEE-077: build the subscription store with optional durable
	// persistence. Empty cfg.SubscriptionStorePath = in-memory only
	// (historical behavior). Non-empty path = JSON file behind atomic
	// rename, loaded at startup and rewritten on every Create/Delete.
	subStore, subErr := memory.NewSubscriptionStoreWithPersistence(cfg.SubscriptionStorePath)
	if subErr != nil {
		return fmt.Errorf("subscription store: %w", subErr)
	}
	if cfg.SubscriptionStorePath != "" {
		log.Printf("subscription persistence enabled: %s", cfg.SubscriptionStorePath)
	}

	// Initialize stores
	stores := &Stores{
		EndDevices:          memory.NewEndDeviceStore(),
		Registrations:       memory.NewStore[sep2.Registration](),
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
		AdminFSAs:          memory.NewAdminFSAStore(),
		Subscriptions:      subStore,
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

	if cfg.BootFixtureFile != "" {
		target := &bootfixture.Target{
			EndDevices:         stores.EndDevices,
			FSAs:               stores.FSAs,
			DERPrograms:        stores.DERPrograms,
			DERControls:        stores.DERControls,
			DefaultDERControls: stores.DefaultDERControls,
			DERCurves:          stores.DERCurves,
		}
		if err := bootfixture.Load(ctx, target, cfg.BootFixtureFile); err != nil {
			return fmt.Errorf("load boot fixture %q: %w", cfg.BootFixtureFile, err)
		}
		log.Printf("boot fixture loaded: %s", cfg.BootFixtureFile)
	}

	// Subscription notification dispatcher. Owns its own bounded worker pool
	// and exits when ctx is cancelled (see shutdown branch below). The
	// router takes it as a handler.ResourceNotifier so DELETE/UPDATE
	// handlers can fan out notifications without depending on the
	// subscription package directly.
	notifier := subscription.NewManager(stores.Subscriptions, subscriptionWorkers, subscriptionQueueSize)
	go notifier.Start(ctx)

	router := NewRouter(cfg, stores, svc, serverSFDI, serverLFDI, notifier)

	if cfg.EnableCCM {
		// Bridge: inject gotls connection state into request context
		sepTLS.SetupCCMServer(protocolSrv)
		protocolSrv.Handler = sepTLS.CCMIdentityMiddleware(router)
	} else {
		protocolSrv.Handler = router
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

	// Start admin server if configured. IEEE-094: admin runs on its own
	// listener (SEP2_ADMIN_LISTEN, falling back to SEP2_ADMIN_ADDR for
	// back-compat) with a weaker TLS posture than the SEP2 wire, so the
	// browser login + cert-paste flows can use Bearer/cookie auth without
	// weakening the SEP2 mTLS requirement. AdminTLS=false serves plain HTTP
	// (Caddy mode); AdminTLS=true serves HTTPS (operator-supplied cert or
	// self-signed fallback).
	var adminSrv *http.Server
	if cfg.EffectiveAdminListen() != "" && svc != nil {
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

// startAdminServer brings up the admin listener on its own port. IEEE-094:
// the listener selection matrix is
//
//	AdminListen empty  → admin disabled (caller gates this case)
//	AdminTLS = false   → plain HTTP (Caddy reverse-proxy mode)
//	AdminTLS = true    → HTTPS with operator cert (AdminCert/AdminKeyFile)
//	                     or self-signed fallback if neither is set
//
// HTTPS modes use VerifyClientCertIfGiven so AdminAuthMiddleware's mTLS
// Path A still works for cert-bearing operators while Bearer/cookie clients
// can connect without presenting a cert. The SEP2 protocol listener keeps
// its own RequireAnyClientCert + manual-verify posture untouched.
func startAdminServer(cfg *config.Config, svc *handler.AdminCertService, stores *Stores, tlsMode string, errCh chan error) (*http.Server, error) {
	addr := cfg.EffectiveAdminListen()

	tickets := auth.NewTicketStore(30 * time.Second)
	adminRouter := NewAdminRouter(cfg.AdminKey, svc, stores, tlsMode, tickets)

	adminListener, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("admin listen: %w", err)
	}

	serveListener := adminListener
	tlsModeDescription := "plain HTTP (Caddy mode)"
	if cfg.AdminTLS {
		tlsCfg, desc, err := buildAdminTLSConfig(cfg)
		if err != nil {
			_ = adminListener.Close()
			return nil, fmt.Errorf("admin TLS config: %w", err)
		}
		serveListener = tls.NewListener(adminListener, tlsCfg)
		tlsModeDescription = desc
	}

	adminSrv := &http.Server{Handler: adminRouter}

	go func() {
		log.Printf("Admin server listening on %s (%s)", addr, tlsModeDescription)
		if cfg.AdminKey != "" {
			log.Println("Admin API key configured")
		} else {
			log.Println("WARNING: No admin API key set (SEP2_ADMIN_KEY). Bearer auth disabled.")
		}
		errCh <- adminSrv.Serve(serveListener)
	}()

	return adminSrv, nil
}

// buildAdminTLSConfig assembles the admin listener's *tls.Config from the
// admin-cert env vars. Operator-supplied cert/key wins; otherwise a fresh
// self-signed cert covering localhost is generated (current default). The
// resulting config uses VerifyClientCertIfGiven so mTLS-bearing operators
// flow through AdminAuthMiddleware Path A while browser clients without a
// cert still complete the handshake and authenticate via Bearer/cookie.
//
// The returned description string is human-readable for the startup log.
func buildAdminTLSConfig(cfg *config.Config) (*tls.Config, string, error) {
	var (
		cert tls.Certificate
		desc string
	)
	switch {
	case cfg.AdminCert != "" && cfg.AdminKeyFile != "":
		c, err := tls.LoadX509KeyPair(cfg.AdminCert, cfg.AdminKeyFile)
		if err != nil {
			return nil, "", fmt.Errorf("load admin cert/key: %w", err)
		}
		cert = c
		desc = "HTTPS, operator cert"
	case cfg.AdminCert != "" || cfg.AdminKeyFile != "":
		return nil, "", fmt.Errorf("admin cert/key must be set together (got AdminCert=%q AdminKeyFile=%q)", cfg.AdminCert, cfg.AdminKeyFile)
	default:
		certPEM, keyPEM, err := certs.GenerateSelfSignedTLS([]string{"localhost", "127.0.0.1", "::1"})
		if err != nil {
			return nil, "", fmt.Errorf("generate admin TLS cert: %w", err)
		}
		c, err := tls.X509KeyPair(certPEM, keyPEM)
		if err != nil {
			return nil, "", fmt.Errorf("parse admin TLS cert: %w", err)
		}
		cert = c
		desc = "HTTPS, self-signed"
	}

	return &tls.Config{
		Certificates: []tls.Certificate{cert},
		MinVersion:   tls.VersionTLS12,
		ClientAuth:   tls.VerifyClientCertIfGiven,
	}, desc, nil
}

func parsePort(addr string) int {
	if idx := strings.LastIndex(addr, ":"); idx >= 0 {
		if port, err := strconv.Atoi(addr[idx+1:]); err == nil {
			return port
		}
	}
	return 443
}

// deriveServerIdentity parses the leaf certificate from a raw DER chain
// (as found in tls.Certificate.Certificate / gotls.Certificate.Certificate)
// and returns the server SFDI and LFDI. Mode-agnostic — works for both
// the stdlib crypto/tls path (GCM) and the forked gotls path (CCM).
func deriveServerIdentity(rawChain [][]byte) (sfdi, lfdi string, err error) {
	if len(rawChain) == 0 {
		return "", "", fmt.Errorf("empty certificate chain")
	}
	leaf, err := x509.ParseCertificate(rawChain[0])
	if err != nil {
		return "", "", fmt.Errorf("parse server leaf: %w", err)
	}
	return sepTLS.SFDI(leaf), sepTLS.LFDI(leaf), nil
}
