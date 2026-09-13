package server

import (
	"log"
	"net/http"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/auth"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/handler"
)

// BuildAdminRouter creates the admin router AND returns the canonical
// pattern list mounted under it. Layers, outermost first:
//  1. Host-header allowlist (#270) - rejects any request whose Host
//     header isn't a hostname this server claims (loopback, localhost, the
//     #246 mDNS hostname, plus operator-extended entries from
//     SEP2_ADMIN_ALLOWED_HOSTS). DNS-rebinding defense-in-depth at the
//     admin boundary; runs BEFORE auth so a wrong-Host request never
//     reaches the auth chain. allowedHosts nil/empty disables the gate
//     (test paths only - the production caller in startAdminServer
//     always supplies the resolved defaults; an empty allowlist logs a
//     loud WARNING at construction time).
//  2. Cross-origin refusal (#416): a state-changing request a browser marks
//     as from another origin is refused before any credential is consulted.
//  3. Public outer mux - /login, /auth/login (login form + submit). These
//     routes are unauthenticated by design: the operator cannot reach the
//     dashboard without first hitting them.
//  4. Authenticated inner mux - everything else (dashboard, /api/*, SSE,
//     ticket exchange). Guarded by AdminAuthMiddleware which supports mTLS,
//     Bearer, the query-param ticket from tickets, and the #159
//     admin_ticket cookie session from sessions. Those last two are
//     separate stores: see AdminAuthMiddleware for why. After auth, a write
//     whose Content-Type its route does not decode is refused (adminBodyTypes).
//
// Patterns from BOTH the public outer mux (login routes) and the authed
// inner mux are merged into one sorted, deduplicated list - callers
// (the boot-time route enumerator) want a single flat view of every
// admin-listener route. The #270 host-header allowlist wraps the
// outer mux when allowedHosts is non-empty; the returned pattern list
// reflects routes mounted under the listener regardless of host gating.
//
// Test callers that don't need the pattern list discard the second
// return value with `_`.
func BuildAdminRouter(adminKey string, svc *handler.AdminCertService, stores *Stores, tlsMode string, tickets *auth.TicketStore, sessions *auth.SessionStore, allowedHosts []string, legacyDashboard bool) (http.Handler, []string) {
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

	// Admin dashboard. legacyDashboard decides which page GET / returns
	// (see handleDashboardPage); the route pattern is the same either way,
	// so the boot-time route list does not change with the flag.
	if stores != nil {
		dashboard := NewDashboardHandler(stores, tlsMode, legacyDashboard)
		dashboard.RegisterRoutes(authed)
	}

	// Admin UI (embedded Svelte SPA, internal/server/web). Mounted at
	// "/ui/", a more specific pattern than the dashboard's catch-all "GET
	// /" above. The SPA is reachable at both paths: "/ui/" always, and "/"
	// unless the legacy flag routes that one to the old page.
	authed.Handle("GET /ui/", http.StripPrefix("/ui", spaHandler()))

	// Auth ticket endpoint - exchanges valid admin auth for a short-lived ticket
	if tickets != nil {
		authed.HandleFunc("POST /auth/ticket", handleIssueTicket(tickets))
	}

	authedWithMiddleware := auth.AdminAuthMiddleware(adminKey, tickets, sessions)(requireAdminBodyTypes(authed))

	// Outer mux: login routes are public; everything else is authed.
	// #270 (bundle B) wraps authedWithMiddleware with a Host-allowlist
	// middleware at the `outer.Handle("/", ...)` line - leave that wrap
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
	// regardless of host gating - boot-log enumeration is independent
	// of which Host headers reach the handlers.
	// Outside both muxes so the login submit is covered too; inside the Host
	// gate so a misdirected request is still answered 421.
	var h http.Handler = auth.AdminCrossOriginMiddleware()(outer)
	if len(allowedHosts) > 0 {
		h = HostAllowlistMiddleware(allowedHosts)(h)
	} else {
		// Wren MED-5: empty allowlist disables the DNS-rebinding gate.
		// The production caller in startAdminServer always supplies the
		// resolved defaults; this branch is reachable only by direct
		// callers (test fixtures, future unscoped callers). Log loudly
		// at construction so the only opt-out path leaves a tripwire in
		// the boot log.
		log.Printf("WARNING: admin: host-allowlist gate DISABLED (BuildAdminRouter called with empty allowedHosts) - DNS-rebinding defense is OFF for this admin router")
	}
	// no-store wraps everything, including the host-gate and login-route
	// refusals. A cookie-authenticated GET gets none of the shared-cache
	// suppression RFC 9111 gives an Authorization-bearing one, so without this
	// an intermediary fronting the listener may store the admin shell or a
	// topology response and serve it to a client that presented no credential.
	// adminSecurityHeaders wraps it, adding the framing/sniff/referrer
	// refusal to the same everything. Admin plane only: the protocol
	// listener's bytes are a conformance surface and are built elsewhere
	// (BuildProtocolRouter).
	return adminSecurityHeaders(adminNoStore(h)), merged
}

// adminNoStore sets Cache-Control: no-store on every admin response. The header
// is written before the wrapped handler runs, so a handler that calls
// WriteHeader before setting its own headers still emits it.
func adminNoStore(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}

// adminSecurityHeaders sets the admin plane's browser-facing response
// headers: a framing refusal, a MIME-sniff refusal, and a referrer policy
// that stops a URL-borne credential (the SSE ticket) from reaching a
// cross-origin Referer. Composed around adminNoStore rather than folded into
// it, since the no-store wrapper's own comment is about caching and this is
// a different concern; both set their headers before the wrapped handler
// runs, for the same reason adminNoStore does (#413).
//
// Both a CSP frame-ancestors and X-Frame-Options are set for the framing
// refusal: CSP is the normative control and the only one a future
// per-origin relaxation could target, but X-Frame-Options: ALLOW-FROM was
// never interoperably implemented, so X-Frame-Options stays DENY-or-nothing.
// A relaxation must edit both, or the second header keeps silently refusing.
func adminSecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", "frame-ancestors 'none'")
		h.Set("X-Frame-Options", "DENY")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		next.ServeHTTP(w, r)
	})
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
