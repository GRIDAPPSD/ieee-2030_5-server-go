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
// reports, mints a fresh one-time ticket for each, presents it alone, and
// requires the response never carry a new one. A minting route added later
// without RequireNonTicketAdmission fails its own subtest here, with nothing
// to add to any list.
//
// Deliberately not folded into sensitiveAdminPatterns / nonSensitiveAdminWrites
// (admin_sensitive_routes.go): that mechanism decides whether the #246
// loopback bypass may admit a route with NO credential at all. A ticket is
// always a real credential by its own measure (credentialAdmits, Path C), so
// folding this check in would still let a ticket mint its own successor -
// the class that mechanism exists to catch is orthogonal to this one.
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
			req := ticketOnlyRequest(method, target, ticket).WithContext(ctx)

			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)

			if strings.Contains(rec.Body.String(), `"ticket":"`) {
				t.Fatalf("%s admitted by a ticket-only credential minted a fresh ticket: status = %d body = %q",
					p, rec.Code, rec.Body.String())
			}
		})
	}
}
