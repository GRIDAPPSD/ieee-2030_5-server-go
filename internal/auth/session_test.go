package auth_test

import (
	"errors"
	"testing"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/auth"
)

func TestSessionIssueAndValidateWithoutConsuming(t *testing.T) {
	store := auth.NewSessionStore(30*time.Second, 5*time.Minute)

	id, err := store.Issue()
	if err != nil {
		t.Fatal(err)
	}
	if id == "" {
		t.Fatal("expected non-empty session id")
	}

	for i := 1; i <= 3; i++ {
		if !store.Validate(id) {
			t.Fatalf("validation %d of 3 failed: a session must not be consumed by validation", i)
		}
	}
	if store.Len() != 1 {
		t.Errorf("session count = %d, want 1", store.Len())
	}
}

func TestSessionIdleTimeoutSlides(t *testing.T) {
	const idle = 40 * time.Millisecond
	store := auth.NewSessionStore(idle, 5*time.Minute)

	id, err := store.Issue()
	if err != nil {
		t.Fatal(err)
	}

	// Three validations spaced past half the idle window each: the session
	// outlives one idle window only because each validation slides it.
	for i := 1; i <= 3; i++ {
		time.Sleep(idle * 3 / 4)
		if !store.Validate(id) {
			t.Fatalf("validation %d of 3 at %v of continuous use failed: idle deadline did not slide", i, time.Duration(i)*idle*3/4)
		}
	}
}

func TestSessionIdleTimeoutExpires(t *testing.T) {
	store := auth.NewSessionStore(1*time.Millisecond, 5*time.Minute)

	id, err := store.Issue()
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(5 * time.Millisecond)

	if store.Validate(id) {
		t.Error("idle-expired session should not validate")
	}
}

// TestSessionAbsoluteExpiryIsNotExtendedByUse is the test that separates a
// sliding session from an immortal one: the session is used continuously,
// each use inside the idle window, and it must still die at the cap.
func TestSessionAbsoluteExpiryIsNotExtendedByUse(t *testing.T) {
	const idle = 20 * time.Millisecond
	const absolute = 60 * time.Millisecond
	store := auth.NewSessionStore(idle, absolute)

	id, err := store.Issue()
	if err != nil {
		t.Fatal(err)
	}

	start := time.Now()
	validations := 0
	for {
		elapsed := time.Since(start)
		ok := store.Validate(id)
		switch {
		case elapsed < absolute-idle:
			if !ok {
				t.Fatalf("validation %d at %v failed while inside both windows", validations+1, elapsed)
			}
			validations++
		case !ok:
			if elapsed < absolute {
				t.Fatalf("session refused at %v, before the %v absolute cap", elapsed, absolute)
			}
			if validations == 0 {
				t.Fatal("session expired before any successful validation: the fixture proved nothing about the cap")
			}
			if store.Len() != 0 {
				t.Errorf("expired session count = %d, want 0 (the sweep must reclaim it)", store.Len())
			}
			return
		}
		if time.Since(start) > 2*absolute {
			t.Fatalf("session still validating at %v with a %v absolute cap after %d validations: the cap was extended by use", time.Since(start), absolute, validations)
		}
		time.Sleep(idle / 2)
	}
}

func TestSessionDelete(t *testing.T) {
	store := auth.NewSessionStore(30*time.Second, 5*time.Minute)

	id, err := store.Issue()
	if err != nil {
		t.Fatal(err)
	}
	if !store.Validate(id) {
		t.Fatal("fresh session should validate")
	}

	store.Delete(id)

	if store.Validate(id) {
		t.Error("deleted session should not validate")
	}
	if store.Len() != 0 {
		t.Errorf("session count after delete = %d, want 0", store.Len())
	}
}

// TestSessionSweepRunsWithoutIssue pins the reclamation path that does not
// depend on new sessions being minted: a store that has stopped issuing must
// still drop what has expired.
func TestSessionSweepRunsWithoutIssue(t *testing.T) {
	store := auth.NewSessionStore(1*time.Millisecond, 5*time.Minute)

	for i := 0; i < 3; i++ {
		if _, err := store.Issue(); err != nil {
			t.Fatal(err)
		}
	}
	if store.Len() != 3 {
		t.Fatalf("session count = %d, want 3", store.Len())
	}

	time.Sleep(5 * time.Millisecond)

	// Validate is the only call made after the sleep, and it names a session
	// that never existed, so nothing but the sweep can change the count.
	if store.Validate("not-a-session") {
		t.Fatal("unknown session must not validate")
	}
	if store.Len() != 0 {
		t.Errorf("session count after a validate-only sweep = %d, want 0", store.Len())
	}
}

func TestSessionEmptyIDRefused(t *testing.T) {
	store := auth.NewSessionStore(30*time.Second, 5*time.Minute)
	if store.Validate("") {
		t.Error("empty session id should not validate")
	}
}

func TestSessionUniqueness(t *testing.T) {
	store := auth.NewSessionStore(30*time.Second, 5*time.Minute)
	seen := make(map[string]bool)
	for i := 0; i < 50; i++ {
		id, err := store.Issue()
		if err != nil {
			t.Fatal(err)
		}
		if seen[id] {
			t.Fatalf("duplicate session id on iteration %d", i)
		}
		seen[id] = true
		store.Delete(id)
	}
}

// TestSessionStoreIsBounded asserts the table is capped and that Issue fails
// closed at the cap instead of evicting a live session.
func TestSessionStoreIsBounded(t *testing.T) {
	store := auth.NewSessionStore(30*time.Second, 5*time.Minute)

	first, err := store.Issue()
	if err != nil {
		t.Fatal(err)
	}

	issued := 1
	for {
		if _, err := store.Issue(); err != nil {
			if !errors.Is(err, auth.ErrSessionStoreFull) {
				t.Fatalf("Issue at capacity returned %v, want ErrSessionStoreFull", err)
			}
			break
		}
		issued++
		if issued > 10000 {
			t.Fatal("Issue never reported a full store: the table is unbounded")
		}
	}

	if store.Len() != issued {
		t.Errorf("session count = %d, want %d (a refused Issue must not evict)", store.Len(), issued)
	}
	if !store.Validate(first) {
		t.Error("the oldest session was evicted by a refused Issue")
	}
}
