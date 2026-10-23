package devicelog

import (
	"context"
	"errors"
	"math"
	"strings"
	"testing"
)

func TestNoteSetRefusesReservedAndUnlistedNames(t *testing.T) {
	t.Parallel()
	_, n := WithNote(context.Background())
	for _, name := range []string{"seq", "t", "kind", "actor", "method", "path", "status", "reason", "edev", "location", "nonsense", ""} {
		if err := n.Set(name, "x"); !errors.Is(err, ErrNoteField) {
			t.Errorf("Set(%q) = %v, want ErrNoteField", name, err)
		}
	}
	if got := n.Fields(); len(got) != 0 {
		t.Errorf("Fields after refused names = %v, want none", got)
	}
	// A nil note refuses the same names, so a decorator's mistake shows even
	// when the observer is off.
	if err := (*Note)(nil).Set("seq", 1); !errors.Is(err, ErrNoteField) {
		t.Errorf("nil Set(seq) = %v, want ErrNoteField", err)
	}
}

func TestNoteSetRecordsListedNameAndCapsStrings(t *testing.T) {
	t.Parallel()
	_, n := WithNote(context.Background())
	if err := n.Set("subject", strings.Repeat("a", maxNoteValue+50)); err != nil {
		t.Fatalf("Set(subject) = %v", err)
	}
	if err := n.Set("mmr", 3); err != nil {
		t.Fatalf("Set(mmr) = %v", err)
	}
	got := n.Fields()
	if s, _ := got["subject"].(string); len(s) != maxNoteValue {
		t.Errorf("subject length = %d, want %d", len(s), maxNoteValue)
	}
	if got["mmr"] != 3 {
		t.Errorf("mmr = %v, want 3", got["mmr"])
	}
	// Fields is a copy: changing it does not change the note.
	got["mmr"] = 99
	if n.Fields()["mmr"] != 3 {
		t.Error("Fields returned the note's own map")
	}
}

func TestNoteRefuseAndNilNote(t *testing.T) {
	t.Parallel()
	ctx, n := WithNote(context.Background())
	NoteFrom(ctx).Refuse("not-owner-or-manager")
	if got := n.Reason(); got != "not-owner-or-manager" {
		t.Errorf("Reason = %q, want the refused reason", got)
	}
	var none *Note = NoteFrom(context.Background())
	if none != nil {
		t.Fatalf("NoteFrom without an observer = %v, want nil", none)
	}
	none.Refuse("x")
	if none.Reason() != "" || none.Fields() != nil {
		t.Error("nil note must be an inert no-op")
	}
}

func TestNoteSetRefusesValuesThatCannotEncode(t *testing.T) {
	t.Parallel()
	_, n := WithNote(context.Background())
	for _, v := range []any{math.NaN(), math.Inf(1), math.Inf(-1), make(chan int)} {
		if err := n.Set("mmr", v); !errors.Is(err, ErrNoteValue) {
			t.Errorf("Set(mmr, %v) = %v, want ErrNoteValue", v, err)
		}
	}
	if got := n.Fields(); len(got) != 0 {
		t.Errorf("Fields after refused values = %v, want none", got)
	}
}
