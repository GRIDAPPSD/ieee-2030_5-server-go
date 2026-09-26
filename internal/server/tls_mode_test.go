package server

import "testing"

// TestCCMOnlyTLSMode is #709 fix round 2, item 3: the coverage lane found
// that setting Run's tlsModeName back to the retired "GCM" literal left
// ./internal/... ./pkg/... green, because every existing banner and admin
// wiring test feeds "CCM-8" in directly rather than reading what Run itself
// produces. Asserted against the literal, not a self-referential copy of the
// constant, so a revert of ccmOnlyTLSMode is what reddens this.
func TestCCMOnlyTLSMode(t *testing.T) {
	if ccmOnlyTLSMode != "CCM-8" {
		t.Errorf("ccmOnlyTLSMode = %q, want %q", ccmOnlyTLSMode, "CCM-8")
	}
}
