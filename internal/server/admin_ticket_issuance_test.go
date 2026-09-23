package server_test

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestAuthTicketMintRequiresRealCredential is #579 HIGH-1, reported
// independently by the security and error-handling review lanes: POST
// /auth/ticket sits on the authed mux (admin_router.go) but was absent from
// sensitiveAdminPatterns, so the loopback bypass admitted it with no
// credential and minted a real one-time ticket. That ticket then satisfied
// RequireRealCredential's own recheck on the routes it exists to protect,
// walking the refusal in exactly two requests. The fix gates issuance
// itself rather than trying to patch the recheck: the mint route is now a
// member of sensitiveAdminPatterns, so it goes through the same
// bypass-only refusal as GET /api/certs/ca.
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
