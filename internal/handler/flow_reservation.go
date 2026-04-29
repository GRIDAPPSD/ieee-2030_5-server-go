package handler

import (
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/craig8/ieee-2030_5-go/internal/encoding"
	"github.com/craig8/ieee-2030_5-go/pkg/sep2"
	"github.com/craig8/ieee-2030_5-go/pkg/store"
	"github.com/craig8/ieee-2030_5-go/pkg/store/memory"
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

// HandlePostFlowReservationRequest returns a handler for POST /edev/{id}/frq.
func HandlePostFlowReservationRequest(
	frqStore *memory.ScopedStore[sep2.FlowReservationRequest],
	frpStore *memory.ScopedStore[sep2.FlowReservationResponse],
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
			http.Error(w, "invalid XML: "+err.Error(), http.StatusBadRequest)
			return
		}

		frqID := fmt.Sprintf("frq-%d", time.Now().UnixNano())
		frq.Href = fmt.Sprintf("/edev/%s/frq/%s", edevID, frqID)
		frq.CreationTime = time.Now().Unix()

		if err := frqStore.Create(r.Context(), edevID, frqID, frq); err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}

		// Auto-create a FlowReservationResponse (server approves by default)
		frpID := fmt.Sprintf("frp-%d", time.Now().UnixNano())
		frp := sep2.FlowReservationResponse{
			EnergyAvailable: frq.EnergyRequested,
			PowerAvailable:  frq.PowerRequested,
			Subject:         frq.MRID,
		}
		frp.Href = fmt.Sprintf("/edev/%s/frp/%s", edevID, frpID)
		if frq.IntervalRequested != nil {
			interval := *frq.IntervalRequested
			frp.Interval = &interval
		}
		status := sep2.EventStatusActive
		frp.EventStatus = &sep2.EventStatus{CurrentStatus: status, DateTime: time.Now().Unix()}

		if err := frpStore.Create(r.Context(), edevID, frpID, frp); err != nil {
			http.Error(w, "create flow reservation: "+err.Error(), http.StatusInternalServerError)
			return
		}

		w.Header().Set("Location", frq.Href)
		encoding.WriteXML(w, http.StatusCreated, &frq)
	}
}

// HandlePostResponse returns a handler for POST /rsps/{rspsId}/rsp.
func HandlePostResponse(rspStore *memory.ScopedStore[sep2.Response]) http.HandlerFunc {
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

		var rsp sep2.Response
		if err := xml.Unmarshal(body, &rsp); err != nil {
			http.Error(w, "invalid XML: "+err.Error(), http.StatusBadRequest)
			return
		}

		id := fmt.Sprintf("rsp-%d", time.Now().UnixNano())
		rsp.Href = fmt.Sprintf("/rsps/%s/rsp/%s", rspsID, id)
		rsp.CreatedDateTime = time.Now().Unix()

		if err := rspStore.Create(r.Context(), rspsID, id, rsp); err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
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
