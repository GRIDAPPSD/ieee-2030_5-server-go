package server

import (
	"net/http"

	"github.com/GRIDAPPSD/ieee-2030_5-go/internal/auth"
	"github.com/GRIDAPPSD/ieee-2030_5-go/internal/handler"
)

// NewAdminRouter creates the admin router.
//
// Two layers:
//  1. Public outer mux — /login, /auth/login (login form + submit). These
//     routes are unauthenticated by design: the operator cannot reach the
//     dashboard without first hitting them.
//  2. Authenticated inner mux — everything else (dashboard, /api/*, SSE,
//     ticket exchange). Guarded by AdminAuthMiddleware which supports mTLS,
//     Bearer, query-param ticket, and the IEEE-095 admin_ticket cookie.
func NewAdminRouter(adminKey string, svc *handler.AdminCertService, stores *Stores, tlsMode string, tickets *auth.TicketStore) http.Handler {
	authed := http.NewServeMux()

	// Certificate management API
	if svc != nil {
		authed.HandleFunc("GET /api/certs/ca", svc.HandleGetCA())
		authed.HandleFunc("POST /api/certs/server", svc.HandleCreateServerCert())
		authed.HandleFunc("POST /api/certs/device", svc.HandleCreateDeviceCert())
	}

	// IEEE-095 registration-assistant API
	authed.HandleFunc("POST /api/certs/info", handler.HandleCertInfo())
	if stores != nil {
		authed.HandleFunc("GET /api/devices/by-lfdi/{lfdi}", handler.HandleDeviceLookupByLFDI(stores.EndDevices))
		if stores.Registrations != nil {
			authed.HandleFunc("POST /api/devices", handler.HandleAddEndDevice(stores.EndDevices, stores.Registrations))
		}
	}

	// IEEE-096 FSA hierarchy management API.
	if fsaH := newAdminFSAHandler(stores); fsaH != nil {
		authed.HandleFunc("POST /api/fsas", fsaH.HandleCreateAdminFSA())
		authed.HandleFunc("GET /api/fsas", fsaH.HandleListAdminFSAs())
		authed.HandleFunc("GET /api/fsas/{id}", fsaH.HandleGetAdminFSA())
		authed.HandleFunc("DELETE /api/fsas/{id}", fsaH.HandleDeleteAdminFSA())
		authed.HandleFunc("POST /api/fsas/{id}/programs", fsaH.HandleAttachProgram())
		authed.HandleFunc("DELETE /api/fsas/{id}/programs", fsaH.HandleDetachProgram())
		authed.HandleFunc("POST /api/devices/{id}/fsa-assignment", fsaH.HandleAssignDeviceFSA())
		authed.HandleFunc("DELETE /api/devices/{id}/fsa-assignment", fsaH.HandleUnassignDeviceFSA())
		authed.HandleFunc("GET /api/topology", handler.HandleTopology(stores.AdminFSAs, stores.EndDevices))
	}

	// Admin dashboard
	if stores != nil {
		dashboard := NewDashboardHandler(stores, tlsMode)
		dashboard.RegisterRoutes(authed)
	}

	// Auth ticket endpoint — exchanges valid admin auth for a short-lived ticket
	if tickets != nil {
		authed.HandleFunc("POST /auth/ticket", handleIssueTicket(tickets))
	}

	authedWithMiddleware := auth.AdminAuthMiddleware(adminKey, tickets)(authed)

	// Outer mux: login routes are public; everything else is authed.
	outer := http.NewServeMux()
	outer.HandleFunc("GET /login", HandleLoginPage(""))
	outer.HandleFunc("POST /auth/login", HandleLoginSubmit(adminKey, tickets))
	outer.Handle("/", authedWithMiddleware)

	return outer
}

func handleIssueTicket(tickets *auth.TicketStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ticket, err := tickets.Issue()
		if err != nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error":"failed to issue ticket"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ticket":"` + ticket + `"}`))
	}
}
