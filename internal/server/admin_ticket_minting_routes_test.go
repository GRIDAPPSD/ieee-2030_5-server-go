package server_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/auth"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/server"
)

// TestNoAuthedRouteMintsATicketForTicketOnlyAdmission is #641 fix round 1,
// item 1: RequireNonTicketAdmission (internal/auth/admin.go) wraps exactly
// one route by hand, admin_router.go:125. A second route that calls
// tickets.Issue() without that wrap is refused by nothing: RequireRealCredential
// treats a ticket as an ordinary real credential (that is its job), so a
// ticket-only request reaches the handler and mints a fresh one. Reproduced
// by registering such a route: the existing coverage test
// (TestEveryAdminWriteRouteIsCovered) stayed green after adding the route's
// name to its two literals, and the route renewed a ticket 10 of 10 times.
//
// This test names no route. It walks every pattern AuthedAdminPatterns
// reports, mints a fresh one-time ticket for each, presents it alone with
// the Content-Type the route itself declares (server.AdminBodyTypes), and
// requires the response never carry a new one. A minting route added later
// without RequireNonTicketAdmission fails its own subtest here, with nothing
// to add to any list.
//
// #641 fix round 2, item 1: the walk used to send no Content-Type at all, so
// requireAdminBodyTypes (admin_body_type.go) answered 415 before the handler
// ran on every POST route that declares a media type, and the subtest passed
// whether or not the handler behind that gate would have minted. Measured: a
// second minting route wired the way a real route would be - declared in
// adminBodyTypes, not wrapped in RequireNonTicketAdmission - passed this walk
// green while renewing a ticket 10 of 10 times through the real router.
// Sending the declared type closes that: the subtest below also fails
// outright if a POST or DELETE route is still refused for its content type,
// since that means this walk never reached the route's own handler at all.
func TestNoAuthedRouteMintsATicketForTicketOnlyAdmission(t *testing.T) {
	patterns := server.AuthedAdminPatterns(
		"the-key", newScopeTestCertService(t), newTestStores(), "GCM",
		auth.NewTicketStore(30*time.Second), auth.NewSessionStore(30*time.Minute, 8*time.Hour),
		false, http.NotFoundHandler(),
	)
	// Control: the walk must find the one route known to mint, or every
	// assertion below passes vacuously (the shape #641 itself, and #579
	// HIGH-1 before it).
	if !slices.Contains(patterns, "POST /auth/ticket") {
		t.Fatalf("AuthedAdminPatterns = %q, want it to include POST /auth/ticket", patterns)
	}

	for _, p := range patterns {
		method, path, ok := strings.Cut(p, " ")
		if !ok {
			t.Fatalf("pattern %q names no method", p)
		}
		target := strings.ReplaceAll(path, "{id}", "x")
		t.Run(p, func(t *testing.T) {
			router := newSensitiveRoutesRouter(t)
			ticket := mintTicketFromLoopback(t, router)

			// GET /dashboard/events is a long-lived SSE stream: it writes one
			// chunk, then blocks until its request context is done. A bounded
			// context lets this walk drive it, and every other handler here
			// returns long before the deadline, so the bound changes nothing
			// for them.
			ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
			defer cancel()
			req := ticketOnlyRequestDeclaringBody(method, target, ticket, p).WithContext(ctx)

			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)

			// POST /auth/ticket is the one route this walk EXPECTS to be
			// refused (RequireNonTicketAdmission, 401), not 415; every other
			// route is declared with a body a ticket-only caller can send, so
			// a 415 here means the walk stalled at the gate instead of
			// reaching the route's own handler.
			if rec.Code == http.StatusUnsupportedMediaType && p != "POST /auth/ticket" {
				t.Fatalf("%s: refused for content type (415) instead of reaching its handler, so this subtest cannot see whether a ticket-only credential would have minted there; body = %q",
					p, rec.Body.String())
			}

			if strings.Contains(rec.Body.String(), `"ticket":"`) {
				t.Fatalf("%s admitted by a ticket-only credential minted a fresh ticket: status = %d body = %q",
					p, rec.Code, rec.Body.String())
			}
		})
	}
}

// ticketOnlyRequestDeclaringBody is ticketOnlyRequest's twin for this walk
// (#641 fix round 2, item 1): it sends the Content-Type and a body for
// pattern's own entry in server.AdminBodyTypes, so a state-changing route
// reaches its handler instead of being refused 415 by requireAdminBodyTypes
// before ticketOnlyRequest's type-less request ever gets there. A pattern
// with no declared type, or a nil entry (a GET, or a DELETE that reads no
// body), gets no Content-Type and no body, same as ticketOnlyRequest.
func ticketOnlyRequestDeclaringBody(method, target, ticket, pattern string) *http.Request {
	types, ok := server.AdminBodyTypes[pattern]
	if !ok || len(types) == 0 {
		return ticketOnlyRequest(method, target, ticket)
	}
	req := newTicketOnlyRequest(method, target, ticket, "{}")
	req.Header.Set("Content-Type", types[0])
	return req
}
