package handler

import (
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/dercontrol"
)

// A start exactly 60 s in the past starts now; 61 s is refused.
func TestValidateRows_StartGraceBoundary(t *testing.T) {
	pen := uint32(0xA0B1)
	issuer, err := dercontrol.NewIssuer(nil, nil, nil, dercontrol.Config{PEN: &pen})
	if err != nil {
		t.Fatal(err)
	}
	h := &AdminGridRequestHandler{Controls: &AdminFSAControlHandler{Controls: &AdminDERControlHandler{Issuer: issuer}}}
	now := int64(2_000_000_000)
	dir, x, y, dur := "discharge", int64(1), int64(3), int64(600)
	row := func(start int64) gridRowBody {
		return gridRowBody{Direction: &dir, X: &x, Y: &y, DurationSeconds: &dur, Start: &start}
	}
	rows, refused := h.validateRows([]gridRowBody{row(now - startGraceSeconds), row(now - startGraceSeconds - 1)}, now)
	if !rows[0].relative {
		t.Errorf("row at now-60 = %+v, want it to start now", rows[0])
	}
	if len(refused) != 1 || refused[0].Row != 2 || refused[0].Code != string(dercontrol.RefusalStartInPast) {
		t.Errorf("refused = %+v, want row 2 start_in_past", refused)
	}
}

// A block with no share has no status to read: the guard answers error rather
// than indexing an empty list.
func TestBlockStatus(t *testing.T) {
	for name, tc := range map[string]struct {
		in   []string
		want string
	}{
		"none":              {nil, "error"},
		"agree":             {[]string{"active", "active"}, "active"},
		"some cancelled":    {[]string{"scheduled", "cancelled"}, "partly cancelled"},
		"differ not cancel": {[]string{"active", "scheduled"}, "mixed"},
	} {
		if got := blockStatus(tc.in); got != tc.want {
			t.Errorf("%s: blockStatus(%q) = %q, want %q", name, tc.in, got, tc.want)
		}
	}
}
