package flowreservation

import (
	"strings"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/dercontrol"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
)

// deriveEventStatus derives a FlowReservationResponse's EventStatus by
// calling dercontrol.DeriveStatus (#564), the one state machine issue
// #666's "status follows the DER control derivation rule" criterion asks
// for. lc is the response's lifecycle record, which carries its cancel
// mark (#714); a response is never superseded, so that branch never fires.
//
// It also carries #564's deliberate 2018 policy: DeriveStatus takes no
// duration or end, so it never returns Complete (a 2023-only value this
// server does not serve on any event-derived resource). A finished grant
// and a zero-duration denial whose start has passed both read Active,
// exactly as an ended DERControl does. That is accepted rather than worked
// around: inventing a Complete-based denial status here would leave two
// different status vocabularies for one server.
func deriveEventStatus(start, creationTime, now int64, lc dercontrol.LifecycleRecord) sep2.EventStatus {
	return dercontrol.DeriveStatus(now, creationTime, start, lc)
}

// responseHref is the href build stores a response under: the response
// shares its request's id.
func responseHref(edevID, frqID string) string {
	return "/edev/" + edevID + "/frp/" + frqID
}

// requestID recovers the store id from a request's own href, under the
// EndDevice it is stored beneath. ok is false for any other shape.
func requestID(edevID, href string) (string, bool) {
	id, ok := strings.CutPrefix(href, "/edev/"+edevID+"/frq/")
	if !ok || id == "" || strings.Contains(id, "/") {
		return "", false
	}
	return id, true
}

// ResponseID recovers the store id from a response's own href, under the
// EndDevice it is stored beneath. ok is false for any other shape.
func ResponseID(edevID, href string) (string, bool) {
	id, ok := strings.CutPrefix(href, "/edev/"+edevID+"/frp/")
	if !ok || id == "" || strings.Contains(id, "/") {
		return "", false
	}
	return id, true
}

// NewDerivedStatusResponseStore decorates responses so Get and List serve
// the EventStatus derived from each response's lifecycle record. Every
// response is built by this server, so one with no record is derived too:
// once its start passes it reads Active, never Scheduled (2030.5 EventStatus
// currentStatus 0).
func NewDerivedStatusResponseStore(responses store.ScopedStore[sep2.FlowReservationResponse], lifecycles dercontrol.LifecycleReader) *dercontrol.DerivedStatusStore[sep2.FlowReservationResponse] {
	return dercontrol.NewDerivedStatusStore(responses, lifecycles, dercontrol.StatusPolicy[sep2.FlowReservationResponse]{
		Event: func(frp *sep2.FlowReservationResponse) *sep2.Event { return &frp.Event },
		ID: func(edevID string, frp sep2.FlowReservationResponse) (string, bool) {
			return ResponseID(edevID, frp.Href)
		},
		ServerAuthored: true,
	})
}
