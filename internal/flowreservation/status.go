package flowreservation

import "github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"

// deriveEventStatus derives EventStatus from interval and now, applying the
// 2018/2023 Event state machine (Scheduled before the earliest effective
// start, Active for the live span, Complete once duration has elapsed) that
// issue #666's "status follows the DER control derivation rule" criterion
// names. #564 has not landed a shared implementation of that rule for
// DERControl yet; this is a standalone application of the same spec state
// machine to the flow reservation response, computed once when the response
// is built (not re-derived on every later GET, which #564's "derived at
// serve time" pattern will need once it exists).
//
// A zero-duration interval (a denial) has an empty active span: start equals
// start+duration, so "now" is never inside [start, start+duration) and the
// state machine only ever reports Scheduled or Complete for it, never
// Active. That is deliberate: an Active denial would misstate a refusal as
// a live grant.
func deriveEventStatus(interval sep2.DateTimeInterval, now int64) sep2.EventStatus {
	start := interval.Start
	end := interval.Start + int64(interval.Duration)
	switch {
	case now < start:
		return sep2.EventStatus{CurrentStatus: sep2.EventStatusScheduled, DateTime: now}
	case now < end:
		return sep2.EventStatus{CurrentStatus: sep2.EventStatusActive, DateTime: start}
	default:
		return sep2.EventStatus{CurrentStatus: sep2.EventStatusComplete, DateTime: end}
	}
}
