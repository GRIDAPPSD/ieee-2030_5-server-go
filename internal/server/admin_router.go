package server

import (
	"net/http"

	"github.com/craig8/ieee-2030_5-go/internal/auth"
	"github.com/craig8/ieee-2030_5-go/internal/handler"
)

// NewAdminRouter creates the admin API router with auth middleware.
// Serves the HTML dashboard at /, JSON API at /api/, and SSE at /dashboard/.
func NewAdminRouter(adminKey string, svc *handler.AdminCertService, stores *Stores, tlsMode string) http.Handler {
	mux := http.NewServeMux()

	// Certificate management API
	mux.HandleFunc("GET /api/certs/ca", svc.HandleGetCA())
	mux.HandleFunc("POST /api/certs/server", svc.HandleCreateServerCert())
	mux.HandleFunc("POST /api/certs/device", svc.HandleCreateDeviceCert())

	// Admin dashboard
	if stores != nil {
		dashboard := NewDashboardHandler(stores, tlsMode)
		dashboard.RegisterRoutes(mux)
	}

	return auth.AdminAuthMiddleware(adminKey)(mux)
}
