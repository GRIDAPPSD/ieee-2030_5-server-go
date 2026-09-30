package flowreservation

import (
	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/dercontrol"
)

// deriveEventStatus derives a FlowReservationResponse's EventStatus by
// calling dercontrol.DeriveStatus (#564), the one state machine issue
// #666's "status follows the DER control derivation rule" criterion asks
// for. lc is always the zero LifecycleRecord: a flow reservation response
// is never cancelled or superseded in this issue's scope (#667, #668), so
// DeriveStatus's Cancelled and Superseded branches never fire here, and it
// reduces to Scheduled before start, Active from then on.
//
// That reduction also carries #564's deliberate 2018 policy: DeriveStatus
// takes no duration or end, so it never returns Complete (a 2023-only
// value this server does not serve on any event-derived resource). A
// finished grant and a zero-duration denial whose start has passed both
// read Active, exactly as an ended DERControl does. That is accepted
// rather than worked around: inventing a Complete-based denial status
// here would leave two different status vocabularies for one server.
func deriveEventStatus(start, creationTime, now int64) sep2.EventStatus {
	return dercontrol.DeriveStatus(now, creationTime, start, dercontrol.LifecycleRecord{})
}
