package handler

import (
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/craig8/ieee-2030_5-go/internal/encoding"
	"github.com/craig8/ieee-2030_5-go/pkg/sep2"
	"github.com/craig8/ieee-2030_5-go/pkg/store"
	"github.com/craig8/ieee-2030_5-go/pkg/store/memory"
)

// BuildSubscriptionList constructs a SubscriptionList from store results.
func BuildSubscriptionList(href string, result store.ListResult[sep2.Subscription], pollRate uint32) sep2.SubscriptionList {
	return sep2.SubscriptionList{
		ListResource: sep2.ListResource{
			SubscribableResource: sep2.SubscribableResource{
				Resource: sep2.Resource{Href: href},
			},
			All:      result.All,
			Results:  result.Results,
			PollRate: pollRate,
		},
		Subscription: result.Items,
	}
}

// HandleCreateSubscription returns a handler for POST /edev/{id}/sub.
func HandleCreateSubscription(subStore *memory.SubscriptionStore) http.HandlerFunc {
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

		var sub sep2.Subscription
		if err := xml.Unmarshal(body, &sub); err != nil {
			http.Error(w, "invalid XML: "+err.Error(), http.StatusBadRequest)
			return
		}

		id := fmt.Sprintf("sub-%d", time.Now().UnixNano())
		sub.Href = fmt.Sprintf("/edev/%s/sub/%s", edevID, id)

		if err := subStore.Create(r.Context(), id, sub); err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Location", sub.Href)
		encoding.WriteXML(w, http.StatusCreated, &sub)
	}
}

// HandleDeleteSubscription returns a handler for DELETE /edev/{id}/sub/{subId}.
func HandleDeleteSubscription(subStore *memory.SubscriptionStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			encoding.MethodNotAllowed(w, "DELETE")
			return
		}

		subID := r.PathValue("subId")
		if err := subStore.Delete(r.Context(), subID); err != nil {
			if errors.Is(err, store.ErrNotFound) {
				http.Error(w, "not found", http.StatusNotFound)
				return
			}
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}

		w.WriteHeader(http.StatusNoContent)
	}
}
