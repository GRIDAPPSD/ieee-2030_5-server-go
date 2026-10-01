package dercontrol

import "github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"

// DeriveStatus computes the EventStatus IEEE 2030.5-2018 requires an
// admin-issued DERControl to carry at instant now (Unix seconds), from its
// own creationTime and interval.start plus its lifecycle record.
//
// Priority matches the issuer's own eligibility rules. Cancellation is
// permanent and checked first: LifecycleRecord.supersedeEligible already
// refuses to supersede a cancelled control, so the two marks never compete.
// Supersede is checked next, and only once its recorded time is reached
// (LifecycleRecord.supersededAsOf); before that instant a control still
// reads by its own timing, because Issue can mark a control superseded
// before that control's own start (an overlapping event issued early).
//
// effectiveStart is the later of start and creationTime: the issuer bumps
// creationTime past an existing control's in the same scope to keep it
// strictly increasing (see Issue), which can push it past a start that was
// validated against an earlier clock read in the same call. Without taking
// the later of the two, a control could read Active before its own
// recorded creationTime.
//
// This server declares no status past Active for a non-cancelled,
// non-superseded control whose interval has ended: IEEE 2030.5-2018 defines
// no "Completed" currentStatus (that value is a 2023 addition), so an ended
// 2018-edition control keeps reading Active indefinitely, exactly as it did
// on the wire before this derivation existed.
func DeriveStatus(now, creationTime, start int64, lc LifecycleRecord) sep2.EventStatus {
	if lc.CancelledAt != nil {
		// dateTime never precedes creationTime. creationTime is bumped past
		// an existing sibling's, one second per item (a revise in the same
		// second as its predecessor, 10.2.2.3 e; a same-second burst of
		// controls in one scope, see Issue), so it can run ahead of the clock
		// and a cancel read before it would carry an earlier dateTime. The
		// result runs ahead of the cancel time only as far as creationTime
		// itself does: 1 s after a same-second revise, N-1 s after a burst of
		// N. Operator choice (#798) over the 2023 dateTime text "not a time
		// in the future or past".
		return sep2.EventStatus{CurrentStatus: sep2.EventStatusCancelled, DateTime: max(*lc.CancelledAt, creationTime)}
	}
	if lc.SupersededAt != nil && now >= *lc.SupersededAt {
		return sep2.EventStatus{CurrentStatus: sep2.EventStatusSuperseded, DateTime: *lc.SupersededAt}
	}

	effectiveStart := start
	if creationTime > effectiveStart {
		effectiveStart = creationTime
	}
	if now < effectiveStart {
		return sep2.EventStatus{CurrentStatus: sep2.EventStatusScheduled, DateTime: creationTime}
	}
	return sep2.EventStatus{CurrentStatus: sep2.EventStatusActive, DateTime: effectiveStart}
}
