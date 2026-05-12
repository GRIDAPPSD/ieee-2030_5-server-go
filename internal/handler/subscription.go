package handler

import (
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-go/internal/encoding"
	"github.com/GRIDAPPSD/ieee-2030_5-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-go/pkg/store"
	"github.com/GRIDAPPSD/ieee-2030_5-go/pkg/store/memory"
)

// subscriptionIDOverride is a test-only seam: when non-nil, the create
// path calls it with the incoming request and uses the returned non-empty
// string as the subscription ID instead of the auto-generated one. Set
// only when the csip_test_hooks build tag is present (see
// subscription_test_hook.go). Production builds leave this nil; the
// create path's nil-check is a single compare and the override branch is
// dead code.
var subscriptionIDOverride func(r *http.Request) string

// subscriptionRefuseCreate is a test-only seam: when non-nil, the create
// path calls it with the resolved subscription ID and returns 409
// Conflict if it reports the ID is refused (e.g. tombstoned by a prior
// /test/mutations/subscription-cancel call). Set only when the
// csip_test_hooks build tag is present. Nil in production builds.
var subscriptionRefuseCreate func(id string) bool

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

		// Allow the csip_test_hooks build to override the auto-generated
		// ID via an X-CSIP-Test-Subscription-ID header. Nil in production;
		// the override branch is unreachable without the build tag.
		var id string
		if subscriptionIDOverride != nil {
			id = subscriptionIDOverride(r)
		}
		if id == "" {
			id = fmt.Sprintf("sub-%d", time.Now().UnixNano())
		}

		// Tombstone check (csip_test_hooks only). Refuses re-creation of
		// an ID that a prior /test/mutations/subscription-cancel call
		// marked as canceled. Returns 409 Conflict: the resource has been
		// terminated, not destroyed at a URL, so 409 is a closer fit than
		// 410 Gone.
		if subscriptionRefuseCreate != nil && subscriptionRefuseCreate(id) {
			http.Error(w, "subscription canceled", http.StatusConflict)
			return
		}

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
