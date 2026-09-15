package dercontrol

import (
	"context"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/handlers/sep2time"
)

// Acceptance criterion 4: interval.start is the requested start or
// creationTime when omitted; a start in the past, a start beyond the
// configured lead limit, and a duration outside its configured range are
// refused, each at the boundary and one past it. Defaults: 7-day lead,
// 60s-to-24h duration.

func issueWith(t *testing.T, h *testHarness, mutate func(*CreateRequest)) (Result, error) {
	t.Helper()
	req := CreateRequest{
		DERProgramHref:  programHref("dev1", "0", "p1"),
		Type:            Connect,
		DurationSeconds: 3600,
	}
	mutate(&req)
	return h.issuer.Issue(context.Background(), req)
}

func TestIssue_Interval_StartOmittedEqualsCreationTime(t *testing.T) {
	h := newHarness(t, Config{PEN: testPEN(1)})
	h.seedProgram(t, "dev1", "p1", controlListHref("dev1", "0", "p1"))

	res, err := issueWith(t, h, func(r *CreateRequest) { r.Start = nil })
	if err != nil {
		t.Fatalf("Issue() error = %v", err)
	}
	if res.Control.Interval.Start != res.Control.CreationTime {
		t.Fatalf("Interval.Start = %d, want CreationTime %d", res.Control.Interval.Start, res.Control.CreationTime)
	}
}

func TestIssue_Interval_StartAtNowAccepted(t *testing.T) {
	h := newHarness(t, Config{PEN: testPEN(1)})
	h.seedProgram(t, "dev1", "p1", controlListHref("dev1", "0", "p1"))

	now := sep2time.Now().Unix()
	res, err := issueWith(t, h, func(r *CreateRequest) { r.Start = &now })
	if err != nil {
		t.Fatalf("Issue() error = %v, want accepted at start == now", err)
	}
	if res.Control.Interval.Start != now {
		t.Fatalf("Interval.Start = %d, want %d", res.Control.Interval.Start, now)
	}
}

func TestIssue_Interval_StartBeforeNowRefused(t *testing.T) {
	h := newHarness(t, Config{PEN: testPEN(1)})
	h.seedProgram(t, "dev1", "p1", controlListHref("dev1", "0", "p1"))

	past := sep2time.Now().Unix() - 1
	_, err := issueWith(t, h, func(r *CreateRequest) { r.Start = &past })
	assertRefusal(t, err, RefusalStartInPast)
	assertNoNewControl(t, h)
}

func TestIssue_Interval_StartAtLeadBoundaryAccepted(t *testing.T) {
	h := newHarness(t, Config{PEN: testPEN(1)})
	h.seedProgram(t, "dev1", "p1", controlListHref("dev1", "0", "p1"))

	atLead := sep2time.Now().Unix() + int64(DefaultStartLead.Seconds())
	_, err := issueWith(t, h, func(r *CreateRequest) { r.Start = &atLead })
	if err != nil {
		t.Fatalf("Issue() error = %v, want accepted at start == now+lead", err)
	}
}

func TestIssue_Interval_StartPastLeadBoundaryRefused(t *testing.T) {
	h := newHarness(t, Config{PEN: testPEN(1)})
	h.seedProgram(t, "dev1", "p1", controlListHref("dev1", "0", "p1"))

	pastLead := sep2time.Now().Unix() + int64(DefaultStartLead.Seconds()) + 1
	_, err := issueWith(t, h, func(r *CreateRequest) { r.Start = &pastLead })
	assertRefusal(t, err, RefusalStartTooFarAhead)
	assertNoNewControl(t, h)
}

func TestIssue_Interval_DurationAtMinBoundaryAccepted(t *testing.T) {
	h := newHarness(t, Config{PEN: testPEN(1)})
	h.seedProgram(t, "dev1", "p1", controlListHref("dev1", "0", "p1"))

	_, err := issueWith(t, h, func(r *CreateRequest) { r.DurationSeconds = uint32(DefaultMinDuration.Seconds()) })
	if err != nil {
		t.Fatalf("Issue() error = %v, want accepted at duration == min", err)
	}
}

func TestIssue_Interval_DurationBelowMinRefused(t *testing.T) {
	h := newHarness(t, Config{PEN: testPEN(1)})
	h.seedProgram(t, "dev1", "p1", controlListHref("dev1", "0", "p1"))

	_, err := issueWith(t, h, func(r *CreateRequest) { r.DurationSeconds = uint32(DefaultMinDuration.Seconds()) - 1 })
	assertRefusal(t, err, RefusalDurationOutOfRange)
	assertNoNewControl(t, h)
}

func TestIssue_Interval_DurationAtMaxBoundaryAccepted(t *testing.T) {
	h := newHarness(t, Config{PEN: testPEN(1)})
	h.seedProgram(t, "dev1", "p1", controlListHref("dev1", "0", "p1"))

	_, err := issueWith(t, h, func(r *CreateRequest) { r.DurationSeconds = uint32(DefaultMaxDuration.Seconds()) })
	if err != nil {
		t.Fatalf("Issue() error = %v, want accepted at duration == max", err)
	}
}

func TestIssue_Interval_DurationAboveMaxRefused(t *testing.T) {
	h := newHarness(t, Config{PEN: testPEN(1)})
	h.seedProgram(t, "dev1", "p1", controlListHref("dev1", "0", "p1"))

	_, err := issueWith(t, h, func(r *CreateRequest) { r.DurationSeconds = uint32(DefaultMaxDuration.Seconds()) + 1 })
	assertRefusal(t, err, RefusalDurationOutOfRange)
	assertNoNewControl(t, h)
}
