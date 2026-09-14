package flow_reservation

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2/encoding"
	coreresponse "github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/handlers/response"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/srverr"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
)

// BuildFlowReservationRequestList constructs a FlowReservationRequestList.
func BuildFlowReservationRequestList(href string, result store.ListResult[sep2.FlowReservationRequest], pollRate uint32) sep2.FlowReservationRequestList {
	return sep2.FlowReservationRequestList{
		ListResource: sep2.ListResource{
			SubscribableResource: sep2.SubscribableResource{
				Resource: sep2.Resource{Href: href},
			},
			All:      result.All,
			Results:  result.Results,
			PollRate: pollRate,
		},
		FlowReservationRequest: result.Items,
	}
}

// BuildFlowReservationResponseList constructs a FlowReservationResponseList.
func BuildFlowReservationResponseList(href string, result store.ListResult[sep2.FlowReservationResponse], pollRate uint32) sep2.FlowReservationResponseList {
	return sep2.FlowReservationResponseList{
		ListResource: sep2.ListResource{
			SubscribableResource: sep2.SubscribableResource{
				Resource: sep2.Resource{Href: href},
			},
			All:      result.All,
			Results:  result.Results,
			PollRate: pollRate,
		},
		FlowReservationResponse: result.Items,
	}
}

// FRPCreator is the subset of the FlowReservationResponse store that
// HandlePostFlowReservationRequest needs to persist an auto-approved
// response.
type FRPCreator interface {
	Create(ctx context.Context, parentID, id string, resource sep2.FlowReservationResponse) error
}

// HandlePostFlowReservationRequest returns a handler for POST /edev/{id}/frq.
func HandlePostFlowReservationRequest(
	frqStore store.ScopedStore[sep2.FlowReservationRequest],
	frpStore FRPCreator,
) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			encoding.MethodNotAllowed(w, "POST")
			return
		}

		edevID := r.PathValue("id")
		body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if err != nil {
			http.Error(w, "read body failed", http.StatusBadRequest)
			return
		}

		var frq sep2.FlowReservationRequest
		if err := xml.Unmarshal(body, &frq); err != nil {
			srverr.BadRequestMessage(w, r, "invalid XML", err)
			return
		}

		frqID := fmt.Sprintf("frq-%d", time.Now().UnixNano())
		frq.Href = fmt.Sprintf("/edev/%s/frq/%s", edevID, frqID)
		frq.CreationTime = time.Now().Unix()

		if err := frqStore.Create(r.Context(), edevID, frqID, frq); err != nil {
			srverr.Internal(w, r, err)
			return
		}

		// Auto-create a FlowReservationResponse (server approves by default).
		//
		// One clock read serves both the event's creationTime and its
		// EventStatus dateTime. Two separate time.Now() calls can straddle a
		// second boundary and yield a status timestamp that predates the
		// creation instant of the very event it describes.
		now := time.Now()
		frpID := fmt.Sprintf("frp-%d", now.UnixNano())
		frp := sep2.FlowReservationResponse{
			EnergyAvailable: frq.EnergyRequested,
			PowerAvailable:  frq.PowerRequested,
			Subject:         frq.MRID,
		}
		frp.Href = fmt.Sprintf("/edev/%s/frp/%s", edevID, frpID)

		// creationTime is required on every Event-derived resource and the
		// server is its only legitimate producer. Leaving it unset is not a
		// cosmetic gap: it serializes as a parseable <creationTime>0</...>,
		// and a client resolving two overlapping equal-primacy events compares
		// creationTime to pick the newer one (the EPRI reference client's
		// block_supersede tests x->creationTime > y->creationTime). With both
		// sides at 0 that comparison is false in either direction, so the
		// incoming event is silently discarded and the server can no longer
		// replace a reservation it already granted.
		frp.CreationTime = now.Unix()

		if frq.IntervalRequested != nil {
			interval := *frq.IntervalRequested
			frp.Interval = &interval
		}
		status := sep2.EventStatusActive
		frp.EventStatus = &sep2.EventStatus{CurrentStatus: status, DateTime: now.Unix()}

		if err := frpStore.Create(r.Context(), edevID, frpID, frp); err != nil {
			srverr.Internal(w, r, fmt.Errorf("create the auto-approving FlowReservationResponse: %w", err))
			return
		}

		w.Header().Set("Location", frq.Href)
		encoding.WriteXML(w, http.StatusCreated, &frq)
	}
}

// HandlePostResponse returns a handler for POST /rsps/{rspsId}/rsp.
//
// The WADL marks this POST Mandatory and declares six root elements at the
// member path it creates, so the body is decoded with sep2.DecodeResponse
// rather than unmarshalled straight into a sep2.Response. Unmarshalling into
// the base type answered the EPRI reference client's conforming
// <DERControlResponse> with 400, because Response.XMLName is pinned. The
// pin stays: DecodeResponse dispatches on the root element and decodes the
// declared subtype, so the accepted set is exactly the subtypes the WADL
// names.
func HandlePostResponse(rspStore store.ScopedStore[sep2.Response]) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			encoding.MethodNotAllowed(w, "POST")
			return
		}

		rspsID := r.PathValue("rspsId")
		body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if err != nil {
			http.Error(w, "read body failed", http.StatusBadRequest)
			return
		}

		rsp, err := sep2.DecodeResponse(body)
		if err != nil {
			srverr.BadRequestMessage(w, r, "invalid XML", err)
			return
		}

		id := fmt.Sprintf("rsp-%d", time.Now().UnixNano())
		rsp.Href = coreresponse.MemberHref(rspsID, id)
		rsp.CreatedDateTime = time.Now().Unix()

		if err := rspStore.Create(r.Context(), rspsID, id, rsp); err != nil {
			srverr.Internal(w, r, err)
			return
		}

		w.Header().Set("Location", rsp.Href)
		w.WriteHeader(http.StatusCreated)
	}
}

// BuildResponseList constructs a ResponseList.
func BuildResponseList(href string, result store.ListResult[sep2.Response], pollRate uint32) sep2.ResponseList {
	return sep2.ResponseList{
		ListResource: sep2.ListResource{
			SubscribableResource: sep2.SubscribableResource{
				Resource: sep2.Resource{Href: href},
			},
			All:      result.All,
			Results:  result.Results,
			PollRate: pollRate,
		},
		Response: result.Items,
	}
}

// BuildResponseSetList constructs a ResponseSetList.
func BuildResponseSetList(href string, result store.ListResult[sep2.ResponseSet], pollRate uint32) sep2.ResponseSetList {
	return sep2.ResponseSetList{
		ListResource: sep2.ListResource{
			SubscribableResource: sep2.SubscribableResource{
				Resource: sep2.Resource{Href: href},
			},
			All:      result.All,
			Results:  result.Results,
			PollRate: pollRate,
		},
		ResponseSet: result.Items,
	}
}
