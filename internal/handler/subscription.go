package handler

import (
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"time"

	"gitlab.pnnl.gov/arista/ieee-2030_5/ieee-2030_5-core/pkg/sep2"
	"gitlab.pnnl.gov/arista/ieee-2030_5/ieee-2030_5-core/pkg/sep2/encoding"
	"gitlab.pnnl.gov/arista/ieee-2030_5/ieee-2030_5-core/pkg/sep2srv/paging"
	"gitlab.pnnl.gov/arista/ieee-2030_5/ieee-2030_5-core/pkg/store"
	"gitlab.pnnl.gov/arista/ieee-2030_5/ieee-2030_5-core/pkg/store/memory"
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

// HandleListSubscriptionsByDevice returns a handler for GET /edev/{id}/sub
// that scopes the response to subscriptions owned by EndDevice {id}.
//
// Per IEEE 2030.5 §10.6.3 and CSIP V1.2 §10.1 the subscription list under
// an EndDevice contains only that EndDevice's subscriptions. IEEE-099
// fixed an earlier wiring that piped the route through the underlying
// union Store, which leaked subscriptions across EndDevices and forced
// AGG-001 to a presence-only assertion as a workaround.
//
// Paging (s/l/a) follows the same spec-§4.6.2 contract as the generic
// list handler. The store returns the full per-EndDevice slice; we
// page it here so callers don't pay for filtering at the storage layer.
func HandleListSubscriptionsByDevice(subStore *memory.SubscriptionStore, pollRate uint32) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			encoding.MethodNotAllowed(w, "GET, HEAD")
			return
		}

		edevID := r.PathValue("id")
		records, err := subStore.ListByDeviceWithIDs(r.Context(), edevID)
		if err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}

		params := paging.ParseQuery(r.URL.Query())
		result := pageSubscriptionRecords(records, params.ToListOptions())

		list := BuildSubscriptionList(r.URL.Path, result, pollRate)
		encoding.WriteXML(w, http.StatusOK, list)
	}
}

// pageSubscriptionRecords applies spec §4.6.2 paging (s/l/a) to a
// per-EndDevice slice of subscription records. The All field carries
// the per-EndDevice total so clients can compute the next-page offset
// without seeing the cross-EndDevice union.
//
// Records arrive in deviceIndex insertion order (Create time); paging
// keys off the storage ID so a's "items strictly after key" semantics
// stay deterministic when the index order matches ID order.
func pageSubscriptionRecords(records []memory.SubscriptionRecord, opts store.ListOptions) store.ListResult[sep2.Subscription] {
	all := uint32(len(records))

	if opts.After != "" {
		idx := len(records)
		for i, rec := range records {
			if rec.ID > opts.After {
				idx = i
				break
			}
		}
		records = records[idx:]
	}

	if opts.Start >= uint32(len(records)) {
		return store.ListResult[sep2.Subscription]{All: all, Results: 0, Items: nil}
	}
	records = records[opts.Start:]

	limit := opts.Limit
	if limit == 0 {
		return store.ListResult[sep2.Subscription]{All: all, Results: 0, Items: nil}
	}
	if limit > uint32(len(records)) {
		limit = uint32(len(records))
	}
	records = records[:limit]

	items := make([]sep2.Subscription, 0, len(records))
	for _, rec := range records {
		items = append(items, rec.Subscription)
	}
	return store.ListResult[sep2.Subscription]{
		All:     all,
		Results: uint32(len(items)),
		Items:   items,
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
//
// On a successful delete, the handler dispatches a final "Removed"
// Notification (Status=3) to the deleted subscription's notification
// receiver via the supplied SubscriberNotifier — IEEE-100 / CSIP V1.2
// §11.6 strengthening. The notify call is best-effort: the spec does
// not require the final Notification, so a queue-full, marshal, or
// transport error is logged but does not change the 204 response.
//
// The notifier is optional. Passing nil disables the final
// Notification (useful for tests that don't exercise the subscription
// pipeline). Production wires *subscription.Manager.
func HandleDeleteSubscription(subStore *memory.SubscriptionStore, notifier SubscriberNotifier) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			encoding.MethodNotAllowed(w, "DELETE")
			return
		}

		subID := r.PathValue("subId")

		// Look up the subscription record before Delete so we can hand
		// the pre-delete value to the notifier — once Delete returns,
		// the record is gone from the store. ErrNotFound here is the
		// "DELETE on unknown ID" path; surface 404 immediately and skip
		// the notify (no subscriber existed).
		sub, getErr := subStore.Store.Get(r.Context(), subID)
		if getErr != nil {
			if errors.Is(getErr, store.ErrNotFound) {
				http.Error(w, "not found", http.StatusNotFound)
				return
			}
			log.Printf("subscription: lookup %q before delete: %v", subID, getErr)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}

		if err := subStore.Delete(r.Context(), subID); err != nil {
			if errors.Is(err, store.ErrNotFound) {
				http.Error(w, "not found", http.StatusNotFound)
				return
			}
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}

		// Best-effort final Removed Notification (IEEE-100). A nil
		// notifier or an error from NotifyRemoved is logged but does
		// not change the 204 — the spec doesn't require this and the
		// subscription has already been deleted from authoritative
		// state.
		if notifier != nil {
			if err := notifier.NotifyRemoved(r.Context(), sub); err != nil {
				log.Printf("subscription: notify removed for %q to %q: %v",
					sub.Href, sub.NotificationURI, err)
			}
		}

		w.WriteHeader(http.StatusNoContent)
	}
}
