package server

import (
	"log"
	"net/http"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/auth"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/handler"
)

// BuildAdminRouter creates the admin router AND returns the canonical
// pattern list mounted under it. Three layers, outermost first:
//  1. Host-header allowlist (#270) — rejects any request whose Host
//     header isn't a hostname this server claims (loopback, localhost, the
//     #246 mDNS hostname, plus operator-extended entries from
//     SEP2_ADMIN_ALLOWED_HOSTS). DNS-rebinding defense-in-depth at the
//     admin boundary; runs BEFORE auth so a wrong-Host request never
//     reaches the auth chain. allowedHosts nil/empty disables the gate
//     (test paths only — the production caller in startAdminServer
//     always supplies the resolved defaults; an empty allowlist logs a
//     loud WARNING at construction time).
//  2. Public outer mux — /login, /auth/login (login form + submit). These
//     routes are unauthenticated by design: the operator cannot reach the
//     dashboard without first hitting them.
//  3. Authenticated inner mux — everything else (dashboard, /api/*, SSE,
//     ticket exchange). Guarded by AdminAuthMiddleware which supports mTLS,
//     Bearer, the query-param ticket from tickets, and the #159
//     admin_ticket cookie session from sessions. Those last two are
//     separate stores: see AdminAuthMiddleware for why.
//
// Patterns from BOTH the public outer mux (login routes) and the authed
// inner mux are merged into one sorted, deduplicated list — callers
// (the boot-time route enumerator) want a single flat view of every
// admin-listener route. The #270 host-header allowlist wraps the
// outer mux when allowedHosts is non-empty; the returned pattern list
// reflects routes mounted under the listener regardless of host gating.
//
// Test callers that don't need the pattern list discard the second
// return value with `_`.
func BuildAdminRouter(adminKey string, svc *handler.AdminCertService, stores *Stores, tlsMode string, tickets *auth.TicketStore, sessions *auth.SessionStore, allowedHosts []string) (http.Handler, []string) {
	authed := newRecordingMux()

	// Certificate management API
	if svc != nil {
		authed.HandleFunc("GET /api/certs/ca", svc.HandleGetCA())
		authed.HandleFunc("POST /api/certs/server", svc.HandleCreateServerCert())
		authed.HandleFunc("POST /api/certs/device", svc.HandleCreateDeviceCert())
	}

	// #159 registration-assistant API
	authed.HandleFunc("POST /api/certs/info", handler.HandleCertInfo())
	if stores != nil {
		authed.HandleFunc("GET /api/devices/by-lfdi/{lfdi}", handler.HandleDeviceLookupByLFDI(stores.EndDevices))
		if stores.Registrations != nil {
			authed.HandleFunc("POST /api/devices", handler.HandleAddEndDevice(stores.EndDevices, stores.Registrations))
		}
	}

	// #163 FSA hierarchy management API.
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

	// Admin UI shell (embedded Svelte SPA, internal/server/web). Mounted at
	// "/ui/", a more specific pattern than the dashboard's catch-all "GET
	// /" above, so the two coexist: this phase adds the shell alongside
	// the existing dashboard rather than replacing it.
	authed.Handle("GET /ui/", http.StripPrefix("/ui", spaHandler()))

	// Auth ticket endpoint — exchanges valid admin auth for a short-lived ticket
	if tickets != nil {
		authed.HandleFunc("POST /auth/ticket", handleIssueTicket(tickets))
	}

	authedWithMiddleware := auth.AdminAuthMiddleware(adminKey, tickets, sessions)(authed)

	// Outer mux: login routes are public; everything else is authed.
	// #270 (bundle B) wraps authedWithMiddleware with a Host-allowlist
	// middleware at the `outer.Handle("/", ...)` line — leave that wrap
	// point clean.
	outer := http.NewServeMux()
	outer.HandleFunc("GET /login", HandleLoginPage(""))
	outer.HandleFunc("POST /auth/login", HandleLoginSubmit(adminKey, sessions))
	outer.Handle("/", authedWithMiddleware)

	// #272: assemble the final pattern list. The two public outer
	// routes (login form + login submit) join the inner authed routes
	// so the boot-time enumerator sees a single flat list per listener.
	// Sort + dedup runs through sortDedupePatterns at the end of the
	// merge.
	merged := append([]string{
		"GET /login",
		"POST /auth/login",
	}, authed.Patterns()...)
	sortDedupePatterns(&merged)

	// #270: wrap the entire outer mux in the host-header allowlist
	// when the caller supplied one. The gate runs BEFORE login routes so
	// /login and /auth/login are protected from DNS-rebinding too. The
	// returned route list reflects what is mounted under the listener
	// regardless of host gating — boot-log enumeration is independent
	// of which Host headers reach the handlers.
	var h http.Handler = outer
	if len(allowedHosts) > 0 {
		h = HostAllowlistMiddleware(allowedHosts)(outer)
	} else {
		// Wren MED-5: empty allowlist disables the DNS-rebinding gate.
		// The production caller in startAdminServer always supplies the
		// resolved defaults; this branch is reachable only by direct
		// callers (test fixtures, future unscoped callers). Log loudly
		// at construction so the only opt-out path leaves a tripwire in
		// the boot log.
		log.Printf("WARNING: admin: host-allowlist gate DISABLED (BuildAdminRouter called with empty allowedHosts) — DNS-rebinding defense is OFF for this admin router")
	}
	return h, merged
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
