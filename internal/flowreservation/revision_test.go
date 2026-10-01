package flowreservation_test

import (
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/flowreservation"
)

func TestRevisionID(t *testing.T) {
	t.Parallel()
	cases := []struct{ prev, want string }{
		{"frq-1700000000000000000", "frq-1700000000000000000-r1"},
		{"frq-17-r1", "frq-17-r2"},
		{"frq-17-r9", "frq-17-r10"},
		{"frq-17-r41", "frq-17-r42"},
		// Not a revision suffix: a leading zero, no digits, a zero count.
		{"frq-17-r01", "frq-17-r01-r1"},
		{"frq-17-r", "frq-17-r-r1"},
		{"frq-17-r0", "frq-17-r0-r1"},
	}
	for _, tc := range cases {
		if got := flowreservation.RevisionID(tc.prev); got != tc.want {
			t.Errorf("RevisionID(%q) = %q, want %q", tc.prev, got, tc.want)
		}
	}
}

// A revision's href must parse back to its id, as the grant source reads it.
func TestRevisionID_IsAResponseID(t *testing.T) {
	t.Parallel()
	id := flowreservation.RevisionID("frq-17")
	got, ok := flowreservation.ResponseID("e1", "/edev/e1/frp/"+id)
	if !ok || got != id {
		t.Fatalf("ResponseID of a revision href = (%q, %v), want (%q, true)", got, ok, id)
	}
}

// RequestIDOf undoes every RevisionID step, and leaves an id without a
// canonical revision suffix as it is.
func TestRequestIDOf(t *testing.T) {
	t.Parallel()
	id := "frq-17"
	for range 12 {
		id = flowreservation.RevisionID(id)
		if got := flowreservation.RequestIDOf(id); got != "frq-17" {
			t.Errorf("RequestIDOf(%q) = %q, want frq-17", id, got)
		}
	}
	for _, id := range []string{"frq-17", "frq-17-r01", "frq-17-r", "frq-17-r0"} {
		if got := flowreservation.RequestIDOf(id); got != id {
			t.Errorf("RequestIDOf(%q) = %q, want it unchanged", id, got)
		}
	}
}
