// Package response serves the Response function set: the ResponseSet
// container, its Response list, and the individual responses a client POSTs
// to acknowledge an event.
//
// The function set is where an event's replyTo points. IEEE 2030.5 makes the
// pairing conditional ("If a response is desired to an event, then the event
// SHALL provide, in the replyTo field, a URI"), but the certification profile
// does not: CSIP CTP CORE-022 requires the server under test to emit replyTo
// together with responseRequired="07", and repeats that setup verbatim in
// BASIC-004 through BASIC-015. So the href this package mints is the target
// the DER handler stamps onto every DERControl it serves, and a set that is
// neither listed nor served would leave that href dangling.
//
// The POST handler itself still lives in the flow_reservation package for
// historical reasons; only the read side and the href shapes are here.
package response

import (
	"context"
	"errors"
	"net/http"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2/encoding"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/srverr"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
)

// DefaultSetID is the id of the ResponseSet this server seeds and points every
// stamped replyTo at.
//
// The WADL describes a ResponseSet as "a particular ResponseList or channel"
// and permits several, so this is a default rather than a limit: a consumer
// that creates further sets has them listed alongside this one, and an event
// carrying its own replyTo is left alone. One set is the right default because
// a Response identifies its own device by endDeviceLFDI, so per-device
// partitioning of the list would add scope without adding information.
const DefaultSetID = "1"

// DefaultSetMRID is the mRID of the seeded set.
//
// ResponseSet extends IdentifiedObject, so the mRID is not decoration: it is
// the identity a client uses to tell one channel from another across a
// restart. It is a fixed literal rather than a value generated at boot for
// exactly that reason: a set whose mRID changed every time the server started
// would look to a client like a different channel each time. The high bytes
// spell "RSP" so the value is recognizable as server-assigned in a capture,
// and the low byte is the set ordinal.
const DefaultSetMRID = "52535000000000000000000000000001"

// DefaultSetDescription is the human-readable label on the seeded set.
const DefaultSetDescription = "Default response set"

// SetHref returns the self href of a ResponseSet.
func SetHref(setID string) string {
	return "/rsps/" + setID
}

// ListHref returns the href of a ResponseSet's ResponseList. This is the URI a
// client POSTs a Response to, and the value stamped into an event's replyTo.
func ListHref(setID string) string {
	return "/rsps/" + setID + "/rsp"
}

// MemberHref returns the self href of one Response within a set. It is minted
// in two places (the Location header on the POST, and the resource's own Href)
// which must agree, so both call this rather than formatting it twice.
func MemberHref(setID, rspID string) string {
	return "/rsps/" + setID + "/rsp/" + rspID
}

// DefaultSet returns the ResponseSet this server seeds.
func DefaultSet() sep2.ResponseSet {
	set := sep2.ResponseSet{
		MRID:        DefaultSetMRID,
		Description: DefaultSetDescription,
		ResponseListLink: &sep2.ListLink{
			Href: ListHref(DefaultSetID),
		},
	}
	set.Href = SetHref(DefaultSetID)
	return set
}

// SeedDefaultSet stores the default ResponseSet if the store does not already
// hold one under DefaultSetID.
//
// It is idempotent, and a set a consumer seeded itself under the same id wins:
// re-seeding over it would replace an operator's own mRID and description with
// ours, and the mRID is the client-visible identity of the channel.
func SeedDefaultSet(ctx context.Context, sets store.ResourceStore[sep2.ResponseSet]) error {
	if sets == nil {
		return nil
	}
	if _, err := sets.Get(ctx, DefaultSetID); err == nil {
		return nil
	} else if !errors.Is(err, store.ErrNotFound) {
		return err
	}
	return sets.Create(ctx, DefaultSetID, DefaultSet())
}

// HandleResponseSet returns a handler for GET /rsps/{rspsId}.
//
// A miss is a clean 404 rather than a synthesized empty set: a client that
// parsed a zero-valued ResponseSet would read an empty ResponseListLink and
// post its acknowledgements into the void.
func HandleResponseSet(sets store.ResourceStore[sep2.ResponseSet]) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			encoding.MethodNotAllowed(w, "GET, HEAD")
			return
		}
		set, err := sets.Get(r.Context(), r.PathValue("rspsId"))
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				http.Error(w, "not found", http.StatusNotFound)
				return
			}
			srverr.Internal(w, r, err)
			return
		}
		encoding.WriteXML(w, http.StatusOK, &set)
	}
}

// HandleResponse returns a handler for GET /rsps/{rspsId}/rsp/{rspId}.
//
// This is the href the POST hands back in its Location header. Without it a
// client that followed our own advertised Location got a 404, which is the
// advertised-but-unrouted defect the mintable-href assertion exists to police,
// and CSIP CTP asserts the Location on every event response test.
func HandleResponse(responses store.ScopedStore[sep2.Response]) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			encoding.MethodNotAllowed(w, "GET, HEAD")
			return
		}
		rsp, err := responses.Get(r.Context(), r.PathValue("rspsId"), r.PathValue("rspId"))
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				http.Error(w, "not found", http.StatusNotFound)
				return
			}
			srverr.Internal(w, r, err)
			return
		}
		encoding.WriteXML(w, http.StatusOK, &rsp)
	}
}
