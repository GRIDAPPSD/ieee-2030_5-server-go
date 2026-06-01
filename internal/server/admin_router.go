package server

import (
	"net/http"
	"sort"

	"github.com/GRIDAPPSD/ieee-2030_5-go/internal/auth"
	"github.com/GRIDAPPSD/ieee-2030_5-go/internal/handler"
)

// NewAdminRouter creates the admin router.
//
// Three layers, outermost first:
//  1. Host-header allowlist (IEEE-138) — rejects any request whose Host
//     header isn't a hostname this server claims (loopback, localhost, the
//     IEEE-133 mDNS hostname, plus operator-extended entries from
//     SEP2_ADMIN_ALLOWED_HOSTS). DNS-rebinding defense-in-depth at the
//     admin boundary; runs BEFORE auth so a wrong-Host request never
//     reaches the auth chain. allowedHosts nil/empty disables the gate
//     (test paths and the few callers that pre-IEEE-138 didn't supply
//     hosts).
//  2. Public outer mux — /login, /auth/login (login form + submit). These
//     routes are unauthenticated by design: the operator cannot reach the
//     dashboard without first hitting them.
//  3. Authenticated inner mux — everything else (dashboard, /api/*, SSE,
//     ticket exchange). Guarded by AdminAuthMiddleware which supports mTLS,
//     Bearer, query-param ticket, and the IEEE-095 admin_ticket cookie.
func NewAdminRouter(adminKey string, svc *handler.AdminCertService, stores *Stores, tlsMode string, tickets *auth.TicketStore, allowedHosts []string) http.Handler {
	h, _ := BuildAdminRouter(adminKey, svc, stores, tlsMode, tickets, allowedHosts)
	return h
}

// BuildAdminRouter is the IEEE-140 sibling that returns the admin
// router AND the canonical pattern list mounted under it. Patterns
// from BOTH the public outer mux (login routes) and the authed inner
// mux are merged into one sorted list, prefixed appropriately —
// callers (the boot-time route enumerator) want a single flat view of
// every admin-listener route. NewAdminRouter delegates here. Pre-
// sorted and deduplicated; see recordingMux.Patterns. The IEEE-138
// host-header allowlist wraps the outer mux when allowedHosts is
// non-empty; the returned pattern list reflects routes mounted under
// the listener regardless of host gating.
func BuildAdminRouter(adminKey string, svc *handler.AdminCertService, stores *Stores, tlsMode string, tickets *auth.TicketStore, allowedHosts []string) (http.Handler, []string) {
	authed := newRecordingMux()

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
	// IEEE-138 (bundle B) wraps authedWithMiddleware with a Host-allowlist
	// middleware at the `outer.Handle("/", ...)` line — leave that wrap
	// point clean.
	outer := http.NewServeMux()
	outer.HandleFunc("GET /login", HandleLoginPage(""))
	outer.HandleFunc("POST /auth/login", HandleLoginSubmit(adminKey, tickets))
	outer.Handle("/", authedWithMiddleware)

	// IEEE-140: assemble the final pattern list. The two public outer
	// routes (login form + login submit) join the inner authed routes
	// so the boot-time enumerator sees a single flat list per listener.
	// Sort + dedup runs through sortDedupePatterns at the end of the
	// merge.
	merged := append([]string{
		"GET /login",
		"POST /auth/login",
	}, authed.Patterns()...)
	sortDedupePatterns(&merged)

	// IEEE-138: wrap the entire outer mux in the host-header allowlist
	// when the caller supplied one. The gate runs BEFORE login routes so
	// /login and /auth/login are protected from DNS-rebinding too. The
	// returned route list reflects what is mounted under the listener
	// regardless of host gating — boot-log enumeration is independent
	// of which Host headers reach the handlers.
	var h http.Handler = outer
	if len(allowedHosts) > 0 {
		h = HostAllowlistMiddleware(allowedHosts)(outer)
	}
	return h, merged
}

// sortDedupePatterns sorts in place and de-duplicates adjacent equal
// entries. Centralized so the protocol and admin path produce
// byte-identical output shapes.
func sortDedupePatterns(p *[]string) {
	if len(*p) == 0 {
		return
	}
	s := *p
	sort.Strings(s)
	w := 0
	for i, v := range s {
		if i == 0 || v != s[w-1] {
			s[w] = v
			w++
		}
	}
	*p = s[:w]
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
