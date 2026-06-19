package handler

import (
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-go/internal/encoding"
	"gitlab.pnnl.gov/arista/ieee-2030_5/ieee-2030_5-core/pkg/sep2"
	"gitlab.pnnl.gov/arista/ieee-2030_5/ieee-2030_5-core/pkg/store"
	"gitlab.pnnl.gov/arista/ieee-2030_5/ieee-2030_5-core/pkg/store/memory"
)

// BuildUsagePointList constructs a UsagePointList.
func BuildUsagePointList(href string, result store.ListResult[sep2.UsagePoint], pollRate uint32) sep2.UsagePointList {
	return sep2.UsagePointList{
		ListResource: sep2.ListResource{
			SubscribableResource: sep2.SubscribableResource{
				Resource: sep2.Resource{Href: href},
			},
			All:      result.All,
			Results:  result.Results,
			PollRate: pollRate,
		},
		UsagePoint: result.Items,
	}
}

// BuildMeterReadingList constructs a MeterReadingList.
func BuildMeterReadingList(href string, result store.ListResult[sep2.MeterReading], pollRate uint32) sep2.MeterReadingList {
	return sep2.MeterReadingList{
		ListResource: sep2.ListResource{
			SubscribableResource: sep2.SubscribableResource{
				Resource: sep2.Resource{Href: href},
			},
			All:      result.All,
			Results:  result.Results,
			PollRate: pollRate,
		},
		MeterReading: result.Items,
	}
}

// BuildReadingList constructs a ReadingList.
func BuildReadingList(href string, result store.ListResult[sep2.Reading], pollRate uint32) sep2.ReadingList {
	return sep2.ReadingList{
		ListResource: sep2.ListResource{
			SubscribableResource: sep2.SubscribableResource{
				Resource: sep2.Resource{Href: href},
			},
			All:      result.All,
			Results:  result.Results,
			PollRate: pollRate,
		},
		Reading: result.Items,
	}
}

// HandleUsagePoint returns a handler for GET /upt/{uptId}.
func HandleUsagePoint(uptStore *memory.Store[sep2.UsagePoint]) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			encoding.MethodNotAllowed(w, "GET, HEAD")
			return
		}
		id := r.PathValue("uptId")
		upt, err := uptStore.Get(r.Context(), id)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				http.Error(w, "not found", http.StatusNotFound)
				return
			}
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		encoding.WriteXML(w, http.StatusOK, &upt)
	}
}

// HandleCreateUsagePoint returns a handler for POST /upt.
func HandleCreateUsagePoint(uptStore *memory.Store[sep2.UsagePoint]) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			encoding.MethodNotAllowed(w, "POST")
			return
		}

		body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if err != nil {
			http.Error(w, "read body failed", http.StatusBadRequest)
			return
		}

		var upt sep2.UsagePoint
		if err := xml.Unmarshal(body, &upt); err != nil {
			http.Error(w, "invalid XML: "+err.Error(), http.StatusBadRequest)
			return
		}

		id := upt.MRID
		if id == "" {
			id = fmt.Sprintf("upt-%d", time.Now().UnixNano())
		}
		upt.Href = "/upt/" + id
		upt.MeterReadingListLink = &sep2.ListLink{Href: fmt.Sprintf("/upt/%s/mr", id)}

		if err := uptStore.Create(r.Context(), id, upt); err != nil {
			if errors.Is(err, store.ErrAlreadyExists) {
				existing, _ := uptStore.Get(r.Context(), id)
				w.Header().Set("Location", existing.Href)
				encoding.WriteXML(w, http.StatusOK, &existing)
				return
			}
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Location", upt.Href)
		encoding.WriteXML(w, http.StatusCreated, &upt)
	}
}

// HandleReadingType returns a handler for GET /rt/{id}.
func HandleReadingType(rtStore *memory.Store[sep2.ReadingType]) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			encoding.MethodNotAllowed(w, "GET, HEAD")
			return
		}
		id := r.PathValue("id")
		rt, err := rtStore.Get(r.Context(), id)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				http.Error(w, "not found", http.StatusNotFound)
				return
			}
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		encoding.WriteXML(w, http.StatusOK, &rt)
	}
}

// BuildReadingTypeList constructs a ReadingTypeList.
func BuildReadingTypeList(href string, result store.ListResult[sep2.ReadingType], pollRate uint32) sep2.ReadingTypeList {
	return sep2.ReadingTypeList{
		ListResource: sep2.ListResource{
			SubscribableResource: sep2.SubscribableResource{
				Resource: sep2.Resource{Href: href},
			},
			All:      result.All,
			Results:  result.Results,
			PollRate: pollRate,
		},
		ReadingType: result.Items,
	}
}
