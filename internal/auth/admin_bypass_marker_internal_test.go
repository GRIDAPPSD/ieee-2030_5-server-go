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
func TestWithCredentialAdmissionSetsCredentialMarker(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	marked := withCredentialAdmission(r)
	outcome, ok := marked.Context().Value(admissionOutcomeContextKey{}).(admissionOutcome)
	if !ok || outcome != admissionOutcomeCredential {
		t.Fatalf("withCredentialAdmission context value = %v (ok=%v), want admissionOutcomeCredential", outcome, ok)
	}
}
