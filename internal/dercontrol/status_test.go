package dercontrol

import (
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
)

// TestDeriveStatusScheduledToActiveBoundary asserts acceptance criterion 1's
// Scheduled/Active transition at the exact second and one second before it,
// for a control whose creationTime is before its own interval.start (the
// ordinary case: no lifecycle record).
func TestDeriveStatusScheduledToActiveBoundary(t *testing.T) {
	t.Parallel()

	const creationTime int64 = 1000
	const start int64 = 2000
	lc := LifecycleRecord{}

	oneBefore := DeriveStatus(start-1, creationTime, start, lc)
	if oneBefore.CurrentStatus != sep2.EventStatusScheduled || oneBefore.DateTime != creationTime {
		t.Errorf("one second before start: got {status %d, dateTime %d}, want {status %d, dateTime %d}",
			oneBefore.CurrentStatus, oneBefore.DateTime, sep2.EventStatusScheduled, creationTime)
	}

	atStart := DeriveStatus(start, creationTime, start, lc)
	if atStart.CurrentStatus != sep2.EventStatusActive || atStart.DateTime != start {
		t.Errorf("at start: got {status %d, dateTime %d}, want {status %d, dateTime %d}",
			atStart.CurrentStatus, atStart.DateTime, sep2.EventStatusActive, start)
	}
}

// TestDeriveStatusEffectiveStartIsLaterOfStartAndCreationTime asserts the
// "later of start and creationTime" rule (issuer.go's creationTime bump can
// push it past a start validated against an earlier clock read): the
// transition instant is creationTime, not start, when creationTime is later.
func TestDeriveStatusEffectiveStartIsLaterOfStartAndCreationTime(t *testing.T) {
	t.Parallel()

	const start int64 = 1000
	const creationTime int64 = 1005 // bumped past start by the issuer
	lc := LifecycleRecord{}

	oneBefore := DeriveStatus(creationTime-1, creationTime, start, lc)
	if oneBefore.CurrentStatus != sep2.EventStatusScheduled {
		t.Errorf("one second before effective start (creationTime): got status %d, want %d",
			oneBefore.CurrentStatus, sep2.EventStatusScheduled)
	}

	atEffectiveStart := DeriveStatus(creationTime, creationTime, start, lc)
	if atEffectiveStart.CurrentStatus != sep2.EventStatusActive || atEffectiveStart.DateTime != creationTime {
		t.Errorf("at effective start: got {status %d, dateTime %d}, want {status %d, dateTime %d}",
			atEffectiveStart.CurrentStatus, atEffectiveStart.DateTime, sep2.EventStatusActive, creationTime)
	}
}

// TestDeriveStatusActiveStaysActivePastEnd asserts the 2018-only rule: no
// currentStatus exists past Active for a non-cancelled, non-superseded
// control, so a long-ended control still reads Active.
func TestDeriveStatusActiveStaysActivePastEnd(t *testing.T) {
	t.Parallel()

	got := DeriveStatus(1_000_000, 1000, 2000, LifecycleRecord{})
	if got.CurrentStatus != sep2.EventStatusActive || got.DateTime != 2000 {
		t.Errorf("long after end: got {status %d, dateTime %d}, want {status %d, dateTime 2000}",
			got.CurrentStatus, got.DateTime, sep2.EventStatusActive)
	}
}

// TestDeriveStatusCancelledIsPermanentAndOverridesTiming asserts a cancelled
// control reads Cancelled with the cancellation dateTime regardless of where
// now sits relative to its own interval, and regardless of a stale
// SupersededAt a caller might still carry (LifecycleRecord.supersedeEligible
// already refuses to supersede a cancelled control, so a real record never
// carries both, but the derivation must not depend on the caller enforcing
// that).
func TestDeriveStatusCancelledIsPermanentAndOverridesTiming(t *testing.T) {
	t.Parallel()

	cancelledAt := int64(1500)
	supersededAt := int64(1600)
	lc := LifecycleRecord{CancelledAt: &cancelledAt, SupersededAt: &supersededAt}

	for _, now := range []int64{500, 1500, 1600, 1_000_000} {
		got := DeriveStatus(now, 1000, 2000, lc)
		if got.CurrentStatus != sep2.EventStatusCancelled || got.DateTime != cancelledAt {
			t.Errorf("now=%d: got {status %d, dateTime %d}, want {status %d, dateTime %d}",
				now, got.CurrentStatus, got.DateTime, sep2.EventStatusCancelled, cancelledAt)
		}
	}
}

// TestDeriveStatusSupersededBoundary asserts acceptance criterion 1's
// Superseded transition at the exact second and one second before it. Before
// SupersededAt the control reads by its own timing (here already Active,
// since start is in the past); superseded controls with a still-future own
// start are covered by TestDeriveStatusSupersededBeforeOwnStart.
func TestDeriveStatusSupersededBoundary(t *testing.T) {
	t.Parallel()

	const creationTime int64 = 1000
	const start int64 = 1000
	supersededAt := int64(5000)
	lc := LifecycleRecord{SupersededAt: &supersededAt}

	oneBefore := DeriveStatus(supersededAt-1, creationTime, start, lc)
	if oneBefore.CurrentStatus != sep2.EventStatusActive {
		t.Errorf("one second before superseded: got status %d, want %d (still Active)",
			oneBefore.CurrentStatus, sep2.EventStatusActive)
	}

	atSuperseded := DeriveStatus(supersededAt, creationTime, start, lc)
	if atSuperseded.CurrentStatus != sep2.EventStatusSuperseded || atSuperseded.DateTime != supersededAt {
		t.Errorf("at superseded: got {status %d, dateTime %d}, want {status %d, dateTime %d}",
			atSuperseded.CurrentStatus, atSuperseded.DateTime, sep2.EventStatusSuperseded, supersededAt)
	}
}

// TestDeriveStatusSupersededBeforeOwnStart asserts a control can be marked
// Superseded before its own interval.start is reached (Issue's overlap scan
// allows superseding a still-Scheduled control), and that before the
// recorded SupersededAt it correctly reads Scheduled rather than Superseded.
func TestDeriveStatusSupersededBeforeOwnStart(t *testing.T) {
	t.Parallel()

	const creationTime int64 = 1000
	const start int64 = 5000 // not yet reached when superseded
	supersededAt := int64(2000)
	lc := LifecycleRecord{SupersededAt: &supersededAt}

	beforeSupersede := DeriveStatus(1500, creationTime, start, lc)
	if beforeSupersede.CurrentStatus != sep2.EventStatusScheduled || beforeSupersede.DateTime != creationTime {
		t.Errorf("before supersede, before own start: got {status %d, dateTime %d}, want {status %d, dateTime %d}",
			beforeSupersede.CurrentStatus, beforeSupersede.DateTime, sep2.EventStatusScheduled, creationTime)
	}

	atSupersede := DeriveStatus(supersededAt, creationTime, start, lc)
	if atSupersede.CurrentStatus != sep2.EventStatusSuperseded || atSupersede.DateTime != supersededAt {
		t.Errorf("at supersede: got {status %d, dateTime %d}, want {status %d, dateTime %d}",
			atSupersede.CurrentStatus, atSupersede.DateTime, sep2.EventStatusSuperseded, supersededAt)
	}
}

// #798 fix round: creationTime + 1 accumulates across a same-second burst of
// controls in one scope (issuer.go), so a cancelled status dateTime runs
// ahead of the cancel time by as far as the last creationTime does, not by
// 1 s. It never precedes creationTime.
func TestDeriveStatus_CancelDateTimeAfterSameSecondBurstEqualsLastCreationTime(t *testing.T) {
	t.Parallel()
	const burst = 6
	now := int64(1_000_000)
	creation := now
	for i := 1; i < burst; i++ {
		creation++ // the issuer's creationTime + 1 for each further control in the second
	}
	cancelledAt := now
	got := DeriveStatus(now, creation, creation, LifecycleRecord{CancelledAt: &cancelledAt})
	if got.CurrentStatus != sep2.EventStatusCancelled || got.DateTime != now+burst-1 {
		t.Fatalf("EventStatus = %+v, want Cancelled at creationTime %d (cancel time + %d)", got, now+burst-1, burst-1)
	}
}
