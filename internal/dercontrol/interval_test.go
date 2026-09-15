package dercontrol

import (
	"context"
	"testing"
	"time"

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

// TestIssue_Interval_DurationStoredMatchesRequest proves the stored
// Interval.Duration, a wire field, is exactly the requested duration: no
// other test in this file reads Interval.Duration, so a mutant storing a
// different value would survive without this.
func TestIssue_Interval_DurationStoredMatchesRequest(t *testing.T) {
	h := newHarness(t, Config{PEN: testPEN(1)})
	h.seedProgram(t, "dev1", "p1", controlListHref("dev1", "0", "p1"))

	res, err := issueWith(t, h, func(r *CreateRequest) { r.DurationSeconds = 4321 })
	if err != nil {
		t.Fatalf("Issue() error = %v", err)
	}
	if res.Control.Interval.Duration != 4321 {
		t.Fatalf("Interval.Duration = %d, want 4321 (the requested duration)", res.Control.Interval.Duration)
	}

	stored, err := h.controls.Get(context.Background(), "dev1/0/p1", res.ID)
	if err != nil {
		t.Fatalf("load stored control: %v", err)
	}
	if stored.Interval.Duration != 4321 {
		t.Fatalf("stored Interval.Duration = %d, want 4321", stored.Interval.Duration)
	}
}

// TestIssue_NonDefaultBounds proves StartLead, MinDuration and MaxDuration
// are the configured values Issue actually checks: every other bound
// test in this file builds the harness with a zero Config, so a
// mutant that ignores the configured bound in favor of the package
// default, or always takes the non-default branch, would survive without
// a test whose bounds differ from the defaults.
func TestIssue_NonDefaultBounds(t *testing.T) {
	cfg := Config{
		PEN:         testPEN(1),
		StartLead:   2 * time.Hour,
		MinDuration: 5 * time.Minute,
		MaxDuration: 30 * time.Minute,
	}
	h := newHarness(t, cfg)
	h.seedProgram(t, "dev1", "p1", controlListHref("dev1", "0", "p1"))

	cases := []struct {
		name    string
		mutate  func(*CreateRequest)
		wantErr RefusalCode // "" means accepted
	}{
		{"start at lead boundary accepted", func(r *CreateRequest) {
			s := sep2time.Now().Unix() + int64(cfg.StartLead.Seconds())
			r.Start = &s
		}, ""},
		{"start past lead boundary refused", func(r *CreateRequest) {
			s := sep2time.Now().Unix() + int64(cfg.StartLead.Seconds()) + 1
			r.Start = &s
		}, RefusalStartTooFarAhead},
		{"duration at min boundary accepted", func(r *CreateRequest) {
			r.DurationSeconds = uint32(cfg.MinDuration.Seconds())
		}, ""},
		{"duration below min refused", func(r *CreateRequest) {
			r.DurationSeconds = uint32(cfg.MinDuration.Seconds()) - 1
		}, RefusalDurationOutOfRange},
		{"duration at max boundary accepted", func(r *CreateRequest) {
			r.DurationSeconds = uint32(cfg.MaxDuration.Seconds())
		}, ""},
		{"duration above max refused", func(r *CreateRequest) {
			r.DurationSeconds = uint32(cfg.MaxDuration.Seconds()) + 1
		}, RefusalDurationOutOfRange},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			req := CreateRequest{
				DERProgramHref:  programHref("dev1", "0", "p1"),
				Type:            Connect,
				DurationSeconds: uint32(cfg.MinDuration.Seconds()),
			}
			c.mutate(&req)
			_, err := h.issuer.Issue(context.Background(), req)
			if c.wantErr == "" {
				if err != nil {
					t.Fatalf("Issue() error = %v, want accepted", err)
				}
				return
			}
			assertRefusal(t, err, c.wantErr)
		})
	}
}
