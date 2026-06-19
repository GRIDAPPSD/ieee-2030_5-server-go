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

// BuildMessagingProgramList constructs a MessagingProgramList.
func BuildMessagingProgramList(href string, result store.ListResult[sep2.MessagingProgram], pollRate uint32) sep2.MessagingProgramList {
	return sep2.MessagingProgramList{
		ListResource: sep2.ListResource{
			SubscribableResource: sep2.SubscribableResource{
				Resource: sep2.Resource{Href: href},
			},
			All:      result.All,
			Results:  result.Results,
			PollRate: pollRate,
		},
		MessagingProgram: result.Items,
	}
}

// BuildTextMessageList constructs a TextMessageList.
func BuildTextMessageList(href string, result store.ListResult[sep2.TextMessage], pollRate uint32) sep2.TextMessageList {
	return sep2.TextMessageList{
		ListResource: sep2.ListResource{
			SubscribableResource: sep2.SubscribableResource{
				Resource: sep2.Resource{Href: href},
			},
			All:      result.All,
			Results:  result.Results,
			PollRate: pollRate,
		},
		TextMessage: result.Items,
	}
}

// HandleMessagingProgram returns a handler for GET /msg/{msgId}.
func HandleMessagingProgram(msgStore *memory.Store[sep2.MessagingProgram]) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			encoding.MethodNotAllowed(w, "GET, HEAD")
			return
		}
		id := r.PathValue("msgId")
		msg, err := msgStore.Get(r.Context(), id)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				http.Error(w, "not found", http.StatusNotFound)
				return
			}
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		encoding.WriteXML(w, http.StatusOK, &msg)
	}
}

// HandlePostTextMessage returns a handler for POST /msg/{msgId}/tm (admin creates message).
func HandlePostTextMessage(tmStore *memory.ScopedStore[sep2.TextMessage]) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			encoding.MethodNotAllowed(w, "POST")
			return
		}

		msgID := r.PathValue("msgId")
		body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if err != nil {
			http.Error(w, "read body failed", http.StatusBadRequest)
			return
		}

		var tm sep2.TextMessage
		if err := xml.Unmarshal(body, &tm); err != nil {
			http.Error(w, "invalid XML: "+err.Error(), http.StatusBadRequest)
			return
		}

		id := fmt.Sprintf("tm-%d", time.Now().UnixNano())
		tm.Href = fmt.Sprintf("/msg/%s/tm/%s", msgID, id)

		if err := tmStore.Create(r.Context(), msgID, id, tm); err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Location", tm.Href)
		encoding.WriteXML(w, http.StatusCreated, &tm)
	}
}
