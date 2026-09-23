package server_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestAuthTicketMintRequiresRealCredential is #579 HIGH-1, reported
// independently by the security and error-handling review lanes: POST
// /auth/ticket sits on the authed mux (admin_router.go) but was absent from
// sensitiveAdminPatterns, so the loopback bypass admitted it with no
// credential and minted a real one-time ticket. That ticket then satisfied
// RequireRealCredential's own recheck on the routes it exists to protect,
// walking the refusal in exactly two requests. The fix gates issuance
// itself rather than trying to patch the recheck: every admin write route
// not on the nonSensitiveAdminWrites exemption list requires a real
// credential by default (admin_sensitive_routes.go), so the mint route goes
// through the same bypass-only refusal as GET /api/certs/ca without needing
// its own name on any list.
func TestAuthTicketMintRequiresRealCredential(t *testing.T) {
	router := newSensitiveRoutesRouter(t)

	// Control: the recheck this class exists to feed still refuses a bare
	// sensitive call with no ticket at all.
	controlRec := httptest.NewRecorder()
	router.ServeHTTP(controlRec, bypassOnlyRequest(http.MethodGet, "/api/certs/ca"))
	if controlRec.Code != http.StatusUnauthorized {
		t.Fatalf("control, bare sensitive call: status = %d, want 401", controlRec.Code)
	}

	// The walk this test guards against: mint with no credential at all.
	mintRec := httptest.NewRecorder()
	router.ServeHTTP(mintRec, bypassOnlyRequest(http.MethodPost, "/auth/ticket"))
	if mintRec.Code != http.StatusUnauthorized {
		t.Fatalf("POST /auth/ticket bypass-only: status = %d, want 401; body = %q (a ticket was minted with no credential)",
			mintRec.Code, mintRec.Body.String())
	}
	if mintRec.Body.String() != sensitiveRefusalBody {
		t.Fatalf("POST /auth/ticket bypass-only: body = %q, want %q", mintRec.Body.String(), sensitiveRefusalBody)
	}

	// Control the other way: a real credential can still mint, proving the
	// route is guarded rather than removed.
	credRec := httptest.NewRecorder()
	router.ServeHTTP(credRec, bearerFromLoopbackRequest(http.MethodPost, "/auth/ticket", "", ""))
	if credRec.Code != http.StatusOK {
		t.Fatalf("POST /auth/ticket with a valid Bearer from loopback: status = %d, want 200; body = %q", credRec.Code, credRec.Body.String())
	}
}

// mintTicketFromLoopback mints a ticket the only way one is meant to come
// into existence: a real credential presented to POST /auth/ticket. It
// fatals the test if the mint itself fails, since every case below depends
// on starting from a genuinely valid ticket.
func mintTicketFromLoopback(t *testing.T, router http.Handler) string {
	t.Helper()
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, bearerFromLoopbackRequest(http.MethodPost, "/auth/ticket", "", ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("mint ticket with a valid Bearer: status = %d, want 200; body = %q", rec.Code, rec.Body.String())
	}
	const marker = `"ticket":"`
	body := rec.Body.String()
	i := strings.Index(body, marker)
	if i < 0 {
		t.Fatalf("mint response %q carries no ticket field", body)
	}
	rest := body[i+len(marker):]
	j := strings.IndexByte(rest, '"')
	if j < 0 {
		t.Fatalf("mint response %q has an unterminated ticket field", body)
	}
	ticket := rest[:j]
	if ticket == "" {
		t.Fatal("minted ticket is empty")
	}
	return ticket
}

// newTicketOnlyRequest is the shape ticketOnlyRequest and its twins share:
// ticket as the sole credential on the query string, from loopback, with
// body left to the caller. RemoteAddr is loopback so the request also
// exercises RequireRealCredential's bypass-only recheck (the path a browser
// tab with no other credential would take), not only AdminAuthMiddleware's
// own initial admission.
func newTicketOnlyRequest(method, target, ticket, body string) *http.Request {
	req := httptest.NewRequest(method, target+"?ticket="+ticket, strings.NewReader(body))
	req.Host = "127.0.0.1"
	req.RemoteAddr = "127.0.0.1:54321"
	return req
}

// ticketOnlyRequest presents ticket as the sole credential: no Bearer, no
// mTLS, no cookie, and no body.
func ticketOnlyRequest(method, target, ticket string) *http.Request {
	return newTicketOnlyRequest(method, target, ticket, "")
}

// ticketOnlyRequestFromRoutableAddress is ticketOnlyRequest's non-loopback
// twin (#641 fix round 1, item 4): RemoteAddr is a routable address, so
// AdminAuthMiddleware's own admission redeems and marks the ticket itself
// (isLoopbackRemote is false, Path 0 never runs) rather than
// RequireRealCredential's bypass-only recheck finding it. Host still names an
// allowed entry (DefaultAdminAllowedHosts) so the request reaches the auth
// chain at all; only RemoteAddr, which is what isLoopbackRemote reads, is
// routable.
func ticketOnlyRequestFromRoutableAddress(method, target, ticket string) *http.Request {
	req := httptest.NewRequest(method, target+"?ticket="+ticket, strings.NewReader(""))
	req.Host = "127.0.0.1"
	req.RemoteAddr = "203.0.113.5:54321"
	return req
}

// TestAuthTicketMintRefusesTicketOnlyAdmission is #641: a one-time ticket
// satisfied RequireRealCredential's own recheck exactly like a Bearer token
// or a cookie session, so a ticket presented to POST /auth/ticket was
// redeemed and a fresh ticket came back. The security lane measured 10 of 10
// renewals returning 200 after a single Bearer-authenticated mint. A ticket
// rides in a query string and lives 30 seconds; without this refusal, one
// leaked ticket is an unbounded credential rather than a 30-second one.
func TestAuthTicketMintRefusesTicketOnlyAdmission(t *testing.T) {
	router := newSensitiveRoutesRouter(t)

	ticket := mintTicketFromLoopback(t, router)

	// #641 fix round 1, item 2: this refusal is byte-identical to
	// LogSensitiveRouteRefusal's (same 401, same body), so the log line is
	// the only record distinguishing "a ticket tried to renew itself" from
	// "no credential at all". Pinned here the same way the sibling refusal
	// events are pinned (LogSensitiveRouteRefusal, LogFailedAdminCredential):
	// renaming admin_ticket_self_renewal_refused, or mislabelling
	// admission_path away from "ticket", fails this assertion.
	buf := captureSlogForSensitiveRoutes(t)
	renewRec := httptest.NewRecorder()
	router.ServeHTTP(renewRec, ticketOnlyRequest(http.MethodPost, "/auth/ticket", ticket))
	if renewRec.Code != http.StatusUnauthorized {
		t.Fatalf("POST /auth/ticket with only a ticket: status = %d, want 401; body = %q (a ticket minted its own successor)",
			renewRec.Code, renewRec.Body.String())
	}
	if renewRec.Body.String() != sensitiveRefusalBody {
		t.Fatalf("POST /auth/ticket with only a ticket: body = %q, want %q", renewRec.Body.String(), sensitiveRefusalBody)
	}
	if !strings.Contains(buf.String(), `"event":"admin_ticket_self_renewal_refused"`) {
		t.Errorf("POST /auth/ticket with only a ticket: no admin_ticket_self_renewal_refused log line; captured = %s", buf.String())
	}
	if !strings.Contains(buf.String(), `"admission_path":"ticket"`) {
		t.Errorf("POST /auth/ticket with only a ticket: log line missing admission_path=ticket; captured = %s", buf.String())
	}

	// The first refusal already consumed the ticket (RequireRealCredential's
	// recheck redeems it before RequireNonTicketAdmission ever runs), so a
	// replay is refused too, now for the ordinary reason of presenting an
	// already-spent ticket. Asserted here so a future change that skipped
	// consumption on this path - leaving the same ticket retriable forever -
	// would be caught.
	replayRec := httptest.NewRecorder()
	router.ServeHTTP(replayRec, ticketOnlyRequest(http.MethodPost, "/auth/ticket", ticket))
	if replayRec.Code != http.StatusUnauthorized {
		t.Fatalf("POST /auth/ticket, ticket replayed: status = %d, want 401", replayRec.Code)
	}

	// Control: a real credential still mints a ticket, proving the route is
	// guarded rather than broken outright.
	credRec := httptest.NewRecorder()
	router.ServeHTTP(credRec, bearerFromLoopbackRequest(http.MethodPost, "/auth/ticket", "", ""))
	if credRec.Code != http.StatusOK {
		t.Fatalf("POST /auth/ticket with a valid Bearer: status = %d, want 200; body = %q", credRec.Code, credRec.Body.String())
	}
}

// TestAuthTicketMintRefusesTicketOnlyAdmissionFromRoutableAddress is #641 fix
// round 1, item 4: every router-level assertion above drives RemoteAddr
// loopback, so it reaches the refusal only through RequireRealCredential's
// bypass-only recheck. The arrival path where AdminAuthMiddleware's own
// admission redeems and marks the ticket - isLoopbackRemote false, Path 0
// never runs - was covered only by the hand-built middleware chain in
// internal/auth (TestRequireNonTicketAdmissionRefusesTicketOnlyFromNonLoopback),
// never by the real router with its cross-origin, host-allowlist, and
// body-type layers in front. This drives the same property through
// BuildAdminRouter.
func TestAuthTicketMintRefusesTicketOnlyAdmissionFromRoutableAddress(t *testing.T) {
	router := newSensitiveRoutesRouter(t)

	ticket := mintTicketFromLoopback(t, router)

	buf := captureSlogForSensitiveRoutes(t)
	renewRec := httptest.NewRecorder()
	router.ServeHTTP(renewRec, ticketOnlyRequestFromRoutableAddress(http.MethodPost, "/auth/ticket", ticket))
	if renewRec.Code != http.StatusUnauthorized {
		t.Fatalf("POST /auth/ticket with only a ticket, routable address: status = %d, want 401; body = %q (a ticket minted its own successor)",
			renewRec.Code, renewRec.Body.String())
	}
	if renewRec.Body.String() != sensitiveRefusalBody {
		t.Fatalf("POST /auth/ticket with only a ticket, routable address: body = %q, want %q", renewRec.Body.String(), sensitiveRefusalBody)
	}
	if !strings.Contains(buf.String(), `"event":"admin_ticket_self_renewal_refused"`) {
		t.Errorf("POST /auth/ticket with only a ticket, routable address: no admin_ticket_self_renewal_refused log line; captured = %s", buf.String())
	}
	if !strings.Contains(buf.String(), `"admission_path":"ticket"`) {
		t.Errorf("POST /auth/ticket with only a ticket, routable address: log line missing admission_path=ticket; captured = %s", buf.String())
	}

	// The ticket was consumed by AdminAuthMiddleware's own admission this
	// time, not by RequireRealCredential's recheck; a replay is refused for
	// the ordinary reason of presenting an already-spent ticket either way.
	replayRec := httptest.NewRecorder()
	router.ServeHTTP(replayRec, ticketOnlyRequestFromRoutableAddress(http.MethodPost, "/auth/ticket", ticket))
	if replayRec.Code != http.StatusUnauthorized {
		t.Fatalf("POST /auth/ticket, ticket replayed, routable address: status = %d, want 401", replayRec.Code)
	}

	// Control: a real credential from the same routable address still mints.
	credRec := httptest.NewRecorder()
	credReq := httptest.NewRequest(http.MethodPost, "/auth/ticket", strings.NewReader(""))
	credReq.Host = "127.0.0.1"
	credReq.RemoteAddr = "203.0.113.5:54321"
	credReq.Header.Set("Authorization", "Bearer the-key")
	router.ServeHTTP(credRec, credReq)
	if credRec.Code != http.StatusOK {
		t.Fatalf("POST /auth/ticket with a valid Bearer, routable address: status = %d, want 200; body = %q", credRec.Code, credRec.Body.String())
	}
}
