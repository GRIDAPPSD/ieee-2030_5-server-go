package server

import (
	"net/http"

	"github.com/craig8/ieee-2030_5-go/internal/auth"
	"github.com/craig8/ieee-2030_5-go/internal/handler"
)

// NewAdminRouter creates the admin API router with auth middleware.
// All routes are JSON endpoints protected by AdminAuthMiddleware.
// The admin port also serves the admin UI at / (future).
func NewAdminRouter(adminKey string, svc *handler.AdminCertService) http.Handler {
	mux := http.NewServeMux()

	// Certificate management API
	mux.HandleFunc("GET /api/certs/ca", svc.HandleGetCA())
	mux.HandleFunc("POST /api/certs/server", svc.HandleCreateServerCert())
	mux.HandleFunc("POST /api/certs/device", svc.HandleCreateDeviceCert())

	// Placeholder for admin UI (future)
	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte(`<!DOCTYPE html><html><head><title>IEEE 2030.5 Admin</title></head>
<body><h1>IEEE 2030.5 Server Admin</h1><p>Admin API available at /api/</p></body></html>`))
	})

	return auth.AdminAuthMiddleware(adminKey)(mux)
}
