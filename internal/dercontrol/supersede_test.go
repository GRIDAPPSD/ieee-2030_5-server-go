package dercontrol

import (
	"context"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/handlers/sep2time"
)

// Acceptance criterion 7: issuing a control whose control set equals an
// overlapping Scheduled or Active control's in the same scope records that
// control as superseded at the new control's start. Overlapping controls
// with disjoint sets, adjacent intervals, and controls in other programs
// are not superseded.

func TestIssue_Supersede_SameSetOverlapping(t *testing.T) {
	h := newHarness(Config{PEN: testPEN(1)})
	h.seedProgram(t, "dev1", "p1", controlListHref("dev1", "0", "p1"))

	start := sep2time.Now().Unix() + 1000
	c, err := issueWith(t, h, func(r *CreateRequest) {
		r.Type = Connect
		r.Start = &start
		r.DurationSeconds = 1000
	})
	if err != nil {
		t.Fatalf("issue C: %v", err)
	}

	nStart := start + 500 // overlaps C's [start, start+1000)
	n, err := issueWith(t, h, func(r *CreateRequest) {
		r.Type = Disconnect // same control set (opModConnect+opModEnergize), different values
		r.Start = &nStart
		r.DurationSeconds = 1000
	})
	if err != nil {
		t.Fatalf("issue N: %v", err)
	}

	if len(n.Supersedes) != 1 || n.Supersedes[0] != c.Control.MRID {
		t.Fatalf("N.Supersedes = %v, want [%q]", n.Supersedes, c.Control.MRID)
	}
	lc, err := h.lifecycles.Get(context.Background(), "dev1/0/p1", c.ID)
	if err != nil {
		t.Fatalf("load C's lifecycle: %v", err)
	}
	if lc.SupersededAt == nil || *lc.SupersededAt != nStart {
		t.Fatalf("C.SupersededAt = %v, want %d (N's start)", lc.SupersededAt, nStart)
	}
	if lc.SupersededBy != n.Control.MRID {
		t.Fatalf("C.SupersededBy = %q, want %q", lc.SupersededBy, n.Control.MRID)
	}
}

func TestIssue_Supersede_SameSetAdjacentNotSuperseded(t *testing.T) {
	h := newHarness(Config{PEN: testPEN(1)})
	h.seedProgram(t, "dev1", "p1", controlListHref("dev1", "0", "p1"))

	start := sep2time.Now().Unix() + 1000
	c, err := issueWith(t, h, func(r *CreateRequest) {
		r.Type = Connect
		r.Start = &start
		r.DurationSeconds = 1000
	})
	if err != nil {
		t.Fatalf("issue C: %v", err)
	}

	nStart := start + 1000 // exactly C's end: adjacent, not overlapping
	n, err := issueWith(t, h, func(r *CreateRequest) {
		r.Type = Disconnect
		r.Start = &nStart
		r.DurationSeconds = 1000
	})
	if err != nil {
		t.Fatalf("issue N: %v", err)
	}

	if len(n.Supersedes) != 0 {
		t.Fatalf("N.Supersedes = %v, want none (adjacent intervals do not overlap)", n.Supersedes)
	}
	lc, err := h.lifecycles.Get(context.Background(), "dev1/0/p1", c.ID)
	if err != nil {
		t.Fatalf("load C's lifecycle: %v", err)
	}
	if lc.SupersededAt != nil {
		t.Fatalf("C.SupersededAt = %v, want nil", lc.SupersededAt)
	}
}

func TestIssue_Supersede_DisjointSetsNotSuperseded(t *testing.T) {
	h := newHarness(Config{PEN: testPEN(1)})
	h.seedProgram(t, "dev1", "p1", controlListHref("dev1", "0", "p1"))

	start := sep2time.Now().Unix() + 1000
	c, err := issueWith(t, h, func(r *CreateRequest) {
		r.Type = Connect
		r.Start = &start
		r.DurationSeconds = 1000
	})
	if err != nil {
		t.Fatalf("issue C: %v", err)
	}

	// Fully overlapping interval, but a disjoint control set (limit vs
	// connect): 2018 rule t says these are independent.
	n, err := issueWith(t, h, func(r *CreateRequest) {
		r.Type = MaxLimW
		r.MaxLimW = uint16ptr(5000)
		r.Start = &start
		r.DurationSeconds = 1000
	})
	if err != nil {
		t.Fatalf("issue N: %v", err)
	}

	if len(n.Supersedes) != 0 {
		t.Fatalf("N.Supersedes = %v, want none (disjoint control sets)", n.Supersedes)
	}
	lc, err := h.lifecycles.Get(context.Background(), "dev1/0/p1", c.ID)
	if err != nil {
		t.Fatalf("load C's lifecycle: %v", err)
	}
	if lc.SupersededAt != nil {
		t.Fatalf("C.SupersededAt = %v, want nil", lc.SupersededAt)
	}
}

func TestIssue_Supersede_OtherProgramNotSuperseded(t *testing.T) {
	h := newHarness(Config{PEN: testPEN(1)})
	h.seedProgram(t, "dev1", "p1", controlListHref("dev1", "0", "p1"))
	h.seedProgram(t, "dev1", "p2", controlListHref("dev1", "0", "p2"))

	start := sep2time.Now().Unix() + 1000
	c, err := h.issuer.Issue(context.Background(), CreateRequest{
		DERProgramHref:  programHref("dev1", "0", "p1"),
		Type:            Connect,
		Start:           &start,
		DurationSeconds: 1000,
	})
	if err != nil {
		t.Fatalf("issue C in p1: %v", err)
	}

	n, err := h.issuer.Issue(context.Background(), CreateRequest{
		DERProgramHref:  programHref("dev1", "0", "p2"),
		Type:            Disconnect,
		Start:           &start,
		DurationSeconds: 1000,
	})
	if err != nil {
		t.Fatalf("issue N in p2: %v", err)
	}

	if len(n.Supersedes) != 0 {
		t.Fatalf("N.Supersedes = %v, want none (different program)", n.Supersedes)
	}
	lc, err := h.lifecycles.Get(context.Background(), "dev1/0/p1", c.ID)
	if err != nil {
		t.Fatalf("load C's lifecycle: %v", err)
	}
	if lc.SupersededAt != nil {
		t.Fatalf("C.SupersededAt = %v, want nil (C is in a different program)", lc.SupersededAt)
	}
}

// Acceptance criterion 7, IEEE 2030.5-2018 lines 9808-9809: a server marks a
// control Superseded at the earliest Effective Start Time of any
// overlapping event, not merely the first one processed. A control already
// marked superseded at a later time must be re-marked when a later-issued
// same-set control overlaps it at an earlier start.
func TestIssue_Supersede_LaterIssuedEarlierOverlapRemarksAtEarlierStart(t *testing.T) {
	h := newHarness(t, Config{PEN: testPEN(1)})
	h.seedProgram(t, "dev1", "p1", controlListHref("dev1", "0", "p1"))

	start := sep2time.Now().Unix() + 1000
	c, err := issueWith(t, h, func(r *CreateRequest) {
		r.Type = Connect
		r.Start = &start
		r.DurationSeconds = 1000
	})
	if err != nil {
		t.Fatalf("issue C: %v", err)
	}

	// N1 overlaps C and marks it superseded at N1's start, which is later
	// than N2's start below. N1 and N2 do not overlap each other, so this
	// isolates C's re-mark from any direct N1/N2 interaction.
	n1Start := start + 800
	n1, err := issueWith(t, h, func(r *CreateRequest) {
		r.Type = Disconnect
		r.Start = &n1Start
		r.DurationSeconds = 1000
	})
	if err != nil {
		t.Fatalf("issue N1: %v", err)
	}
	if len(n1.Supersedes) != 1 || n1.Supersedes[0] != c.Control.MRID {
		t.Fatalf("N1.Supersedes = %v, want [%q]", n1.Supersedes, c.Control.MRID)
	}

	n2Start := start + 100 // overlaps C only, earlier than N1's mark
	n2, err := issueWith(t, h, func(r *CreateRequest) {
		r.Type = Disconnect
		r.Start = &n2Start
		r.DurationSeconds = 500
	})
	if err != nil {
		t.Fatalf("issue N2: %v", err)
	}
	if len(n2.Supersedes) != 1 || n2.Supersedes[0] != c.Control.MRID {
		t.Fatalf("N2.Supersedes = %v, want [%q] (C stays eligible: N2's start is earlier than N1's mark)", n2.Supersedes, c.Control.MRID)
	}

	lc, err := h.lifecycles.Get(context.Background(), "dev1/0/p1", c.ID)
	if err != nil {
		t.Fatalf("load C's lifecycle: %v", err)
	}
	if lc.SupersededAt == nil || *lc.SupersededAt != n2Start {
		t.Fatalf("C.SupersededAt = %v, want %d (N2's earlier start, not N1's)", lc.SupersededAt, n2Start)
	}
	if lc.SupersededBy != n2.Control.MRID {
		t.Fatalf("C.SupersededBy = %q, want %q (N2, not N1)", lc.SupersededBy, n2.Control.MRID)
	}
}

// The converse of the above: a control already superseded at an earlier
// time is not re-marked by a later-starting overlap.
func TestIssue_Supersede_AlreadySupersededAtEarlierTimeStaysIneligible(t *testing.T) {
	h := newHarness(t, Config{PEN: testPEN(1)})
	h.seedProgram(t, "dev1", "p1", controlListHref("dev1", "0", "p1"))

	start := sep2time.Now().Unix() + 1000
	c, err := issueWith(t, h, func(r *CreateRequest) {
		r.Type = Connect
		r.Start = &start
		r.DurationSeconds = 1000
	})
	if err != nil {
		t.Fatalf("issue C: %v", err)
	}

	n1Start := start + 100 // marks C at the earlier time
	n1, err := issueWith(t, h, func(r *CreateRequest) {
		r.Type = Disconnect
		r.Start = &n1Start
		r.DurationSeconds = 500
	})
	if err != nil {
		t.Fatalf("issue N1: %v", err)
	}

	n2Start := start + 800 // overlaps C, but later than N1's mark
	n2, err := issueWith(t, h, func(r *CreateRequest) {
		r.Type = Disconnect
		r.Start = &n2Start
		r.DurationSeconds = 1000
	})
	if err != nil {
		t.Fatalf("issue N2: %v", err)
	}
	if len(n2.Supersedes) != 0 {
		t.Fatalf("N2.Supersedes = %v, want none (C is already superseded at an earlier time by N1)", n2.Supersedes)
	}

	lc, err := h.lifecycles.Get(context.Background(), "dev1/0/p1", c.ID)
	if err != nil {
		t.Fatalf("load C's lifecycle: %v", err)
	}
	if lc.SupersededAt == nil || *lc.SupersededAt != n1Start {
		t.Fatalf("C.SupersededAt = %v, want still %d (N1's earlier mark, not overwritten by N2)", lc.SupersededAt, n1Start)
	}
	if lc.SupersededBy != n1.Control.MRID {
		t.Fatalf("C.SupersededBy = %q, want still %q", lc.SupersededBy, n1.Control.MRID)
	}
}

// A cancelled control stays ineligible for supersede regardless of the new
// control's start.
func TestIssue_Supersede_CancelledStaysIneligible(t *testing.T) {
	h := newHarness(t, Config{PEN: testPEN(1)})
	h.seedProgram(t, "dev1", "p1", controlListHref("dev1", "0", "p1"))

	start := sep2time.Now().Unix() + 1000
	c, err := issueWith(t, h, func(r *CreateRequest) {
		r.Type = Connect
		r.Start = &start
		r.DurationSeconds = 1000
	})
	if err != nil {
		t.Fatalf("issue C: %v", err)
	}
	if _, err := h.issuer.Cancel(context.Background(), c.Scope, c.ID, "operator stop"); err != nil {
		t.Fatalf("Cancel(C): %v", err)
	}

	nStart := start + 500
	n, err := issueWith(t, h, func(r *CreateRequest) {
		r.Type = Disconnect
		r.Start = &nStart
		r.DurationSeconds = 1000
	})
	if err != nil {
		t.Fatalf("issue N: %v", err)
	}
	if len(n.Supersedes) != 0 {
		t.Fatalf("N.Supersedes = %v, want none (C is cancelled, not superseded)", n.Supersedes)
	}
}
