package commitment

import (
	"context"
	"errors"
	"slices"
	"testing"
)

// Tests for GRIDAPPSD/ieee-2030_5-server-go#762: finishing a Revise that
// stopped between its writes. Each starts from opsFixture (grant-1 with
// ctrl-b at 1000 and ctrl-a at 2000) plus a grant-2 stored as the tip.

func withTip(w *fakeWriters, mutate func(*Grant)) {
	w.grants.grants = append(w.grants.grants, revisedGrant(mutate))
}

func deleteTip(w *fakeWriters) func(context.Context) error {
	return w.replacement(revisedGrant(func(*Grant) {})).Delete
}

func (f *fakeWriters) grant(mrid string) (Grant, bool) {
	for _, g := range f.grants.grants {
		if g.MRID == mrid {
			return g, true
		}
	}
	return Grant{}, false
}

func TestCompleteRevision_RollsForwardWhenTheTipFits(t *testing.T) {
	t.Parallel()
	l, w := opsFixture(t)
	withTip(w, func(*Grant) {})

	forward, err := l.CompleteRevision(context.Background(), w.writers(), []string{"grant-1"}, "grant-2", 900, deleteTip(w))
	if err != nil || !forward {
		t.Fatalf("CompleteRevision() = %v, %v; want rolled forward", forward, err)
	}
	want := []string{"relink:ctrl-b->grant-2", "relink:ctrl-a->grant-2", "mark:grant-1"}
	if !slices.Equal(w.calls, want) {
		t.Fatalf("calls = %v, want %v", w.calls, want)
	}
	for _, id := range []string{"ctrl-a", "ctrl-b"} {
		if c := w.control(id); c.GrantMRID != "grant-2" || c.Cancelled {
			t.Errorf("%s = %+v, want live and linked to grant-2", id, *c)
		}
	}
	old, _ := w.grant("grant-1")
	if old.CancelledAt == nil || *old.CancelledAt != 900 {
		t.Errorf("grant-1 CancelledAt = %v, want 900", old.CancelledAt)
	}
	if tip, _ := w.grant("grant-2"); tip.CancelledAt != nil {
		t.Errorf("grant-2 CancelledAt = %v, want live", tip.CancelledAt)
	}
}

func TestCompleteRevision_FinishesARevisionWithSomeExecutionsAlreadyMoved(t *testing.T) {
	t.Parallel()
	l, w := opsFixture(t)
	withTip(w, func(*Grant) {})
	w.control("ctrl-b").GrantMRID = "grant-2"

	forward, err := l.CompleteRevision(context.Background(), w.writers(), []string{"grant-1"}, "grant-2", 900, deleteTip(w))
	if err != nil || !forward {
		t.Fatalf("CompleteRevision() = %v, %v; want rolled forward", forward, err)
	}
	if want := []string{"relink:ctrl-a->grant-2", "mark:grant-1"}; !slices.Equal(w.calls, want) {
		t.Fatalf("calls = %v, want %v", w.calls, want)
	}
}

func TestCompleteRevision_RollsBackWhenTheTipNoLongerFits(t *testing.T) {
	t.Parallel()
	l, w := opsFixture(t)
	// grant-2 ends at 2000, before ctrl-a starts.
	withTip(w, func(g *Grant) { g.Window = &Window{Start: 1000, Duration: 1000} })
	w.control("ctrl-b").GrantMRID = "grant-2"

	forward, err := l.CompleteRevision(context.Background(), w.writers(), []string{"grant-1"}, "grant-2", 900, deleteTip(w))
	if err != nil || forward {
		t.Fatalf("CompleteRevision() = %v, %v; want rolled back", forward, err)
	}
	if want := []string{"relink:ctrl-b->grant-1", "delete:grant-2"}; !slices.Equal(w.calls, want) {
		t.Fatalf("calls = %v, want %v", w.calls, want)
	}
	if _, ok := w.grant("grant-2"); ok {
		t.Error("grant-2 still stored after a roll back")
	}
	old, _ := w.grant("grant-1")
	if old.CancelledAt != nil {
		t.Errorf("grant-1 CancelledAt = %v, want live", old.CancelledAt)
	}
	for _, id := range []string{"ctrl-a", "ctrl-b"} {
		if c := w.control(id); c.GrantMRID != "grant-1" || c.Cancelled {
			t.Errorf("%s = %+v, want live and linked to grant-1", id, *c)
		}
	}
}

func TestCompleteRevision_RollsForwardADenialWithoutACheck(t *testing.T) {
	t.Parallel()
	l, w := opsFixture(t)
	withTip(w, func(g *Grant) { g.Window = &Window{Start: 1000, Duration: 0} })

	forward, err := l.CompleteRevision(context.Background(), w.writers(), []string{"grant-1"}, "grant-2", 900, deleteTip(w))
	if err != nil || !forward {
		t.Fatalf("CompleteRevision() = %v, %v; want rolled forward", forward, err)
	}
	if want := []string{"cancel:ctrl-b", "cancel:ctrl-a", "mark:grant-1"}; !slices.Equal(w.calls, want) {
		t.Fatalf("calls = %v, want %v", w.calls, want)
	}
}

func TestCompleteRevision_AFailedWriteIsFinishedByASecondCall(t *testing.T) {
	t.Parallel()
	l, w := opsFixture(t)
	withTip(w, func(*Grant) {})
	w.failOn = map[string]bool{"relink:ctrl-a->grant-2": true}

	_, err := l.CompleteRevision(context.Background(), w.writers(), []string{"grant-1"}, "grant-2", 900, deleteTip(w))
	if !errors.Is(err, errWrite) {
		t.Fatalf("CompleteRevision() error = %v, want wrapping errWrite", err)
	}
	if slices.Contains(w.calls, "mark:grant-1") {
		t.Fatalf("calls = %v: the old grant was marked after an execution failed to move", w.calls)
	}

	w.failOn = nil
	w.calls = nil
	forward, err := l.CompleteRevision(context.Background(), w.writers(), []string{"grant-1"}, "grant-2", 900, deleteTip(w))
	if err != nil || !forward {
		t.Fatalf("second CompleteRevision() = %v, %v; want rolled forward", forward, err)
	}
	if want := []string{"relink:ctrl-a->grant-2", "mark:grant-1"}; !slices.Equal(w.calls, want) {
		t.Fatalf("second calls = %v, want %v", w.calls, want)
	}
}

func TestCompleteRevision_Refusals(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	t.Run("no older grant", func(t *testing.T) {
		l, w := opsFixture(t)
		withTip(w, func(*Grant) {})
		if _, err := l.CompleteRevision(ctx, w.writers(), nil, "grant-2", 900, deleteTip(w)); err == nil {
			t.Fatal("CompleteRevision() with no older grant returned nil")
		}
		if len(w.calls) != 0 {
			t.Errorf("calls = %v, want none", w.calls)
		}
	})
	t.Run("no way to delete the tip", func(t *testing.T) {
		l, w := opsFixture(t)
		withTip(w, func(*Grant) {})
		if _, err := l.CompleteRevision(ctx, w.writers(), []string{"grant-1"}, "grant-2", 900, nil); err == nil {
			t.Fatal("CompleteRevision() with no delete returned nil")
		}
	})
	t.Run("an older grant already cancelled", func(t *testing.T) {
		l, w := opsFixture(t)
		withTip(w, func(*Grant) {})
		at := int64(500)
		w.grants.grants[0].CancelledAt = &at
		_, err := l.CompleteRevision(ctx, w.writers(), []string{"grant-1"}, "grant-2", 900, deleteTip(w))
		wantConflict(t, err, ConflictGrantNotLive, "grant-1")
		if len(w.calls) != 0 {
			t.Errorf("calls = %v, want none", w.calls)
		}
	})
	t.Run("absent tip", func(t *testing.T) {
		l, w := opsFixture(t)
		_, err := l.CompleteRevision(ctx, w.writers(), []string{"grant-1"}, "grant-2", 900, deleteTip(w))
		if !errors.Is(err, ErrNoGrant) {
			t.Fatalf("CompleteRevision() error = %v, want ErrNoGrant", err)
		}
	})
}
