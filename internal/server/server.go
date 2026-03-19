package server

import (
	"context"
	"crypto/tls"
	"fmt"
	"log"
	"net"
	"net/http"

	"github.com/craig8/ieee-2030_5-go/internal/config"
	sepTLS "github.com/craig8/ieee-2030_5-go/internal/tls"
)

// Run starts the IEEE 2030.5 server with mutual TLS.
func Run(ctx context.Context, cfg *config.Config) error {
	tlsCfg, err := sepTLS.NewServerTLSConfig(cfg.CertFile, cfg.KeyFile, cfg.CAFile)
	if err != nil {
		return fmt.Errorf("TLS config: %w", err)
	}

	router := NewRouter(cfg)

	listener, err := net.Listen("tcp", cfg.Addr)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	defer listener.Close()

	tlsListener := tls.NewListener(listener, tlsCfg)

	srv := &http.Server{
		Handler: router,
	}

	errCh := make(chan error, 1)
	go func() {
		log.Printf("IEEE 2030.5 server listening on %s (TLS)", cfg.Addr)
		errCh <- srv.Serve(tlsListener)
	}()

	select {
	case <-ctx.Done():
		log.Println("shutting down server...")
		return srv.Shutdown(context.Background())
	case err := <-errCh:
		return err
	}
}
