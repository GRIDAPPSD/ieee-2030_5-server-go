package server

import (
	"net/http"

	"github.com/craig8/ieee-2030_5-go/internal/auth"
	"github.com/craig8/ieee-2030_5-go/internal/config"
	"github.com/craig8/ieee-2030_5-go/internal/handler"
)

// NewRouter creates the HTTP router for the protocol listener.
// Protocol endpoints get identity middleware (SFDI/LFDI extraction).
// Admin API endpoints (/api/) get admin auth middleware (mTLS OID or Bearer).
func NewRouter(cfg *config.Config, svc *handler.AdminCertService) http.Handler {
	top := http.NewServeMux()

	// IEEE 2030.5 protocol endpoints — identity middleware
	protocolMux := http.NewServeMux()
	protocolMux.Handle("GET /dcap", handler.HandleDeviceCapability())
	protocolMux.Handle("GET /tm", handler.HandleTime(cfg))
	top.Handle("/dcap", auth.IdentityMiddleware(protocolMux))
	top.Handle("/tm", auth.IdentityMiddleware(protocolMux))

	// Admin API on protocol port — admin auth middleware (mTLS with admin OID)
	if svc != nil {
		adminMux := http.NewServeMux()
		adminMux.HandleFunc("GET /api/certs/ca", svc.HandleGetCA())
		adminMux.HandleFunc("POST /api/certs/server", svc.HandleCreateServerCert())
		adminMux.HandleFunc("POST /api/certs/device", svc.HandleCreateDeviceCert())
		top.Handle("/api/", auth.AdminAuthMiddleware(cfg.AdminKey)(adminMux))
	}

	return top
}
