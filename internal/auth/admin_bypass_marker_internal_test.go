package auth

import (
	"net/http/httptest"
	"testing"
)

// TestWithBypassAdmissionSetsBypassMarker is #579 fix round 2, item 3: the
// table test in admin_bypass_marker_test.go drives requests through
// AdminAuthMiddleware and reads AdmittedByLoopbackBypassOnly, which folds
// "explicitly marked bypass" and "never marked at all" into the same true
// (the #579 MEDIUM-3 fail-closed default). Reducing withBypassAdmission to
// `return r` leaves that table test green, since an unmarked request already
// reads the same way. This test reads the raw context value instead of the
// folded bool, so it distinguishes the two.
//
// Mutant: change withBypassAdmission's body to `return r`. This test goes
// RED (ok is false, since no value is set at all).
func TestWithBypassAdmissionSetsBypassMarker(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	marked := withBypassAdmission(r)
	outcome, ok := marked.Context().Value(admissionOutcomeContextKey{}).(admissionOutcome)
	if !ok || outcome != admissionOutcomeBypass {
		t.Fatalf("withBypassAdmission context value = %v (ok=%v), want admissionOutcomeBypass", outcome, ok)
	}
}

// TestWithCredentialAdmissionSetsCredentialMarker is the mirror case: it
// pins that withCredentialAdmission sets its own distinct constant, so the
// pair proves each function sets the value it claims to rather than the
// pass being inferred from AdmittedByLoopbackBypassOnly's folded reading.
// The path argument (credentialPathBearer here) is asserted separately by
// TestWithCredentialAdmissionSetsCredentialPathMarker (#641); this test's
// concern is only the outcome marker, so any path value would do.
func TestWithCredentialAdmissionSetsCredentialMarker(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	marked := withCredentialAdmission(r, credentialPathBearer)
	outcome, ok := marked.Context().Value(admissionOutcomeContextKey{}).(admissionOutcome)
	if !ok || outcome != admissionOutcomeCredential {
		t.Fatalf("withCredentialAdmission context value = %v (ok=%v), want admissionOutcomeCredential", outcome, ok)
	}
}

// TestWithCredentialAdmissionSetsCredentialPathMarker is #641: the ticket
// route's refusal (AdmittedViaTicket) reads a second marker that
// withCredentialAdmission must also set, distinct from and alongside the
// outcome marker above. A regression that sets the outcome but drops the
// path (or sets the wrong one) would leave AdmittedViaTicket unable to tell
// a ticket-admitted request from a Bearer-admitted one.
func TestWithCredentialAdmissionSetsCredentialPathMarker(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	marked := withCredentialAdmission(r, credentialPathTicket)
	path, ok := marked.Context().Value(credentialPathContextKey{}).(credentialPath)
	if !ok || path != credentialPathTicket {
		t.Fatalf("withCredentialAdmission context value = %v (ok=%v), want credentialPathTicket", path, ok)
	}
}
