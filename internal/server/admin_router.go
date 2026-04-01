package server

import (
	"net/http"

	"github.com/craig8/ieee-2030_5-go/internal/auth"
	"github.com/craig8/ieee-2030_5-go/internal/handler"
)

// NewAdminRouter creates the admin API router with auth middleware.
// Serves the HTML dashboard at /, JSON API at /api/, and SSE at /dashboard/.
func NewAdminRouter(adminKey string, svc *handler.AdminCertService, stores *Stores, tlsMode string, tickets *auth.TicketStore) http.Handler {
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

	// Auth ticket endpoint — exchanges valid admin auth for a short-lived ticket
	if tickets != nil {
		mux.HandleFunc("POST /auth/ticket", handleIssueTicket(tickets))
	}

	return auth.AdminAuthMiddleware(adminKey, tickets)(mux)
}

func handleIssueTicket(tickets *auth.TicketStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ticket, err := tickets.Issue()
		if err != nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusInternalServerError)
			w.Write([]byte(`{"error":"failed to issue ticket"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"ticket":"` + ticket + `"}`))
	}
}
