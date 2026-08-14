package logevent

import (
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2/encoding"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/srverr"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
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

// HandlePostLogEvent returns a handler for POST /edev/{id}/lel, which the WADL
// declares mode M at sep_wadl.xml:1358.
//
// The Href it stamps, and hands straight back in the Location header, is the
// WADL address of the LogEvent instance, /edev/{id1}/lel/{id2}
// (sep_wadl.xml:1404). It used to be /edev/{id}/log/{id}, which no route
// served at that address OR at the declared one, so every device that
// reported an alarm was handed a URI this server would answer 404 on. The
// href goes into the STORED document, not only into the header, so the
// instance route
// serves back the same URI the client was told to follow.
func HandlePostLogEvent(logStore store.ScopedStore[sep2.LogEvent]) http.HandlerFunc {
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
		logEvent.Href = fmt.Sprintf("/edev/%s/lel/%s", edevID, id)

		if err := logStore.Create(r.Context(), edevID, id, logEvent); err != nil {
			srverr.Internal(w, r, err)
			return
		}

		w.Header().Set("Location", logEvent.Href)
		w.WriteHeader(http.StatusCreated)
	}
}
