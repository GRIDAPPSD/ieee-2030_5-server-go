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
