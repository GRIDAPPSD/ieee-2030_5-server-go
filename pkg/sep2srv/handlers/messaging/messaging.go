package messaging

import (
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2/encoding"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/srverr"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
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
func HandleMessagingProgram(msgStore store.ResourceStore[sep2.MessagingProgram]) http.HandlerFunc {
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
			srverr.Internal(w, r, err)
			return
		}
		encoding.WriteXML(w, http.StatusOK, &msg)
	}
}

// HandlePostTextMessage returns a handler for POST /msg/{msgId}/tm (admin creates message).
func HandlePostTextMessage(tmStore store.ScopedStore[sep2.TextMessage]) http.HandlerFunc {
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

		now := time.Now()
		id := fmt.Sprintf("tm-%d", now.UnixNano())
		tm.Href = fmt.Sprintf("/msg/%s/tm/%s", msgID, id)

		// creationTime is required on every Event-derived resource (TextMessage
		// embeds RandomizableEvent) and names the instant the SERVER created
		// the event, so the server owns it the same way it owns href above.
		//
		// It carries no omitempty, so an unset value serves a parseable
		// <creationTime>0</creationTime> rather than a missing element. That is
		// quieter and worse: a client resolving two overlapping equal-primacy
		// events compares creationTime to pick the newer one (the EPRI
		// reference client's block_supersede tests
		// x->creationTime > y->creationTime), so with both sides at 0 the
		// comparison fails in either direction and the incoming event is
		// discarded. Assigning here also discards any stale value a client
		// supplied.
		tm.CreationTime = now.Unix()

		if err := tmStore.Create(r.Context(), msgID, id, tm); err != nil {
			srverr.Internal(w, r, err)
			return
		}

		w.Header().Set("Location", tm.Href)
		encoding.WriteXML(w, http.StatusCreated, &tm)
	}
}
