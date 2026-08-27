package auth_test

import (
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

// TestSessionStoreEvictsOldestAtCapacity asserts the cap bounds memory without
// becoming a lockout. Issue keeps succeeding at the cap, the table stops
// growing, and it is the OLDEST session that goes: the newest mint, which is
// the one an operator just made, always survives.
//
// The failure this replaces is not hypothetical. A browser that discards the
// Secure cookie makes every login retry a successful mint that is never
// validated, so each retry held a slot for the whole idle window and, with no
// logout route, further logins failed until entries aged out.
func TestSessionStoreEvictsOldestAtCapacity(t *testing.T) {
	store := auth.NewSessionStore(30*time.Second, 5*time.Minute)

	// Mint enough to reach the cap and then well past it, recording order.
	// The count is deliberately larger than any plausible cap so the test
	// does not encode the cap's value.
	const mints = 300
	ids := make([]string, 0, mints)
	for i := 0; i < mints; i++ {
		id, err := store.Issue()
		if err != nil {
			t.Fatalf("Issue #%d failed: %v (capacity must not refuse)", i, err)
		}
		ids = append(ids, id)
	}

	capacity := store.Len()
	if capacity == 0 || capacity >= mints {
		t.Fatalf("session count = %d after %d mints: want a bound strictly between 0 and %d",
			capacity, mints, mints)
	}

	// The newest session must be live: that is the operator who just logged in.
	newest := ids[len(ids)-1]
	if !store.Validate(newest) {
		t.Error("the newest session is not live, so a login at the cap does not work")
	}

	// The oldest must be gone, and so must everything evicted before it.
	if store.Validate(ids[0]) {
		t.Error("the oldest session survived past the cap, so eviction is not by age")
	}
	evicted := mints - capacity
	for i := 0; i < evicted; i++ {
		if store.Validate(ids[i]) {
			t.Errorf("session %d of %d survived; eviction must take the oldest first", i, mints)
		}
	}

	// And the surviving window is exactly the newest `capacity` ids. Validate
	// slides deadlines but never mints, so the count cannot move here.
	for i := evicted; i < mints; i++ {
		if !store.Validate(ids[i]) {
			t.Errorf("session %d of %d was evicted while older ones remain", i, mints)
		}
	}
	if got := store.Len(); got != capacity {
		t.Errorf("session count = %d after validating every survivor, want %d", got, capacity)
	}
}

// TestSessionStoreEvictionPrefersExpiredOverLive pins the ordering between the
// two ways a slot is freed: an expired entry is swept first, so a live session
// is never evicted while a dead one occupies a slot.
func TestSessionStoreEvictionPrefersExpiredOverLive(t *testing.T) {
	// A very short idle window so the first batch expires on its own.
	store := auth.NewSessionStore(20*time.Millisecond, 5*time.Minute)

	stale, err := store.Issue()
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(40 * time.Millisecond)

	fresh, err := store.Issue()
	if err != nil {
		t.Fatal(err)
	}

	if store.Validate(stale) {
		t.Error("an expired session validated; the sweep did not run")
	}
	if !store.Validate(fresh) {
		t.Error("the fresh session was swept or evicted")
	}
	if got := store.Len(); got != 1 {
		t.Errorf("session count = %d, want 1 (the expired entry is gone)", got)
	}
}
