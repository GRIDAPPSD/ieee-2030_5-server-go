//go:build csip_test_hooks

package dercontrol

import (
	"context"
	"testing"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/handlers/sep2time"
)

// TestIssue_CreationTime_FollowsTestHookOffset proves Issue reads the
// clock through sep2time.Now, including its csip_test_hooks offset
// (#563): if issuer.go called time.Now() directly, CreationTime would sit
// near the unshifted wall clock, 48 hours outside the window below.
func TestIssue_CreationTime_FollowsTestHookOffset(t *testing.T) {
	sep2time.ResetClockOffset()
	defer sep2time.ResetClockOffset()
	sep2time.AdvanceClock(48 * time.Hour)

	h := newHarness(t, Config{PEN: testPEN(1)})
	h.seedProgram(t, "dev1", "p1", controlListHref("dev1", "0", "p1"))

	before := sep2time.Now().Unix()
	res, err := h.issuer.Issue(context.Background(), CreateRequest{
		DERProgramHref:  programHref("dev1", "0", "p1"),
		Type:            Connect,
		DurationSeconds: 3600,
	})
	after := sep2time.Now().Unix()
	if err != nil {
		t.Fatalf("Issue() error = %v", err)
	}
	if res.Control.CreationTime < before || res.Control.CreationTime > after {
		t.Fatalf("CreationTime = %d, want between %d and %d (the test-hook offset clock)", res.Control.CreationTime, before, after)
	}
}

// TestCancel_CancelledAt_FollowsTestHookOffset is Cancel's counterpart:
// CancelledAt must track the same offset clock Issue uses, not the
// unshifted wall clock.
func TestCancel_CancelledAt_FollowsTestHookOffset(t *testing.T) {
	sep2time.ResetClockOffset()
	defer sep2time.ResetClockOffset()
	sep2time.AdvanceClock(48 * time.Hour)

	h := newHarness(t, Config{PEN: testPEN(1)})
	h.seedProgram(t, "dev1", "p1", controlListHref("dev1", "0", "p1"))

	start := sep2time.Now().Unix() + 100
	res, err := h.issuer.Issue(context.Background(), CreateRequest{
		DERProgramHref:  programHref("dev1", "0", "p1"),
		Type:            Connect,
		Start:           &start,
		DurationSeconds: 3600,
	})
	if err != nil {
		t.Fatalf("issue: %v", err)
	}

	sep2time.AdvanceClock(200 * time.Second) // now inside [start, start+3600)
	before := sep2time.Now().Unix()
	lc, err := h.issuer.Cancel(context.Background(), res.Scope, res.ID, "")
	after := sep2time.Now().Unix()
	if err != nil {
		t.Fatalf("Cancel() error = %v", err)
	}
	if lc.CancelledAt == nil || *lc.CancelledAt < before || *lc.CancelledAt > after {
		t.Fatalf("CancelledAt = %v, want between %d and %d (the test-hook offset clock)", lc.CancelledAt, before, after)
	}
}
