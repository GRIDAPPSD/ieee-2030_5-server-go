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

// BuildLogEventList constructs a LogEventList from store results.
func BuildLogEventList(href string, result store.ListResult[sep2.LogEvent], pollRate uint32) sep2.LogEventList {
	return sep2.LogEventList{
		ListResource: sep2.ListResource{
			SubscribableResource: sep2.SubscribableResource{
				Resource: sep2.Resource{Href: href},
			},
			All:      result.All,
			Results:  result.Results,
			PollRate: pollRate,
		},
		LogEvent: result.Items,
	}
}

// HandlePostLogEvent returns a handler for POST /edev/{id}/log.
func HandlePostLogEvent(logStore *memory.ScopedStore[sep2.LogEvent]) http.HandlerFunc {
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

		var logEvent sep2.LogEvent
		if err := xml.Unmarshal(body, &logEvent); err != nil {
			http.Error(w, "invalid XML: "+err.Error(), http.StatusBadRequest)
			return
		}

		id := fmt.Sprintf("%020d", time.Now().UnixNano())
		logEvent.Href = fmt.Sprintf("/edev/%s/log/%s", edevID, id)

		if err := logStore.Create(r.Context(), edevID, id, logEvent); err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Location", logEvent.Href)
		w.WriteHeader(http.StatusCreated)
	}
}
