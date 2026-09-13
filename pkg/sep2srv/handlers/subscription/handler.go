package subscription

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2/encoding"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/paging"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/srverr"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
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

// subscriptionHref builds the href a subscription created under EndDevice
// edevID with storage id id carries on the wire. The create and delete
// paths below both call this so the two can't drift apart (#435).
func subscriptionHref(edevID, id string) string {
	return fmt.Sprintf("/edev/%s/sub/%s", edevID, id)
}

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
// Per IEEE 2030.5 section 10.6.3 and CSIP V1.2 section 10.1 the
// subscription list under an EndDevice contains only that EndDevice's
// subscriptions. This route used to pipe through the underlying union
// Store, which leaked subscriptions across EndDevices and forced AGG-001
// to a presence-only assertion as a workaround; that leak is fixed
// (GRIDAPPSD/ieee-2030_5-server-go#168).
//
// Paging (s/l/a) follows the same spec-section-4.6.2 contract as the
// generic list handler. The store returns the full per-EndDevice slice;
// we page it here so callers don't pay for filtering at the storage layer.
func HandleListSubscriptionsByDevice(subStore *memory.SubscriptionStore, pollRate uint32) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			encoding.MethodNotAllowed(w, "GET, HEAD")
			return
		}

		edevID := r.PathValue("id")
		records, err := subStore.ListByDeviceWithIDs(r.Context(), edevID)
		if err != nil {
			srverr.Internal(w, r, err)
			return
		}

		params := paging.ParseQuery(r.URL.Query())
		result := pageSubscriptionRecords(records, params.ToListOptions())

		list := BuildSubscriptionList(r.URL.Path, result, pollRate)
		encoding.WriteXML(w, http.StatusOK, list)
	}
}

// pageSubscriptionRecords applies spec section 4.6.2 paging (s/l/a) to a
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
//
// validate vets the notificationURI before anything is stored. Pass
// (*Manager).ValidateNotificationURI so creation and delivery apply the same
// DestinationPolicy; nil applies the default policy.
func HandleCreateSubscription(subStore *memory.SubscriptionStore, validate func(ctx context.Context, uri string) error) http.HandlerFunc {
	if validate == nil {
		log.Print("subscription: no notificationURI validator wired; POST /edev/{id}/sub applies the default DestinationPolicy")
		validate = DestinationPolicy{}.ValidateNotificationURI
	}
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

		if err := validate(r.Context(), sub.NotificationURI); err != nil {
			target := redactURI(sub.NotificationURI)
			var dnsErr *net.DNSError
			switch {
			case errors.Is(err, ErrRefusedDestination):
				log.Printf("subscription: refused notificationURI %q for EndDevice %q: %v", target, edevID, err)
			case errors.Is(err, ErrDestinationUnresolved) && r.Context().Err() != nil:
				log.Printf("subscription: request ended while resolving notificationURI %q for EndDevice %q, refusing: %v", target, edevID, err)
			case errors.Is(err, ErrDestinationUnresolved) && errors.As(err, &dnsErr) && dnsErr.IsNotFound:
				log.Printf("subscription: no such host for notificationURI %q for EndDevice %q, refusing: %v", target, edevID, err)
			case errors.Is(err, ErrDestinationUnresolved):
				log.Printf("subscription: could not resolve notificationURI %q for EndDevice %q, refusing: %v", target, edevID, err)
			default:
				log.Printf("subscription: validating notificationURI %q for EndDevice %q: %v", target, edevID, err)
				srverr.Internal(w, r, err)
				return
			}
			// One response for both: a distinct one for an unresolvable name
			// would tell the client whether an internal name resolves.
			http.Error(w, "notificationURI refused", http.StatusBadRequest)
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

		sub.Href = subscriptionHref(edevID, id)

		if err := subStore.Create(r.Context(), id, sub); err != nil {
			srverr.Internal(w, r, err)
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
// receiver via the supplied notifyRemoved callback, per CSIP V1.2 section
// 11.6 strengthening (GRIDAPPSD/ieee-2030_5-server-go#169). The notify
// call is best-effort: the spec does
// not require the final Notification, so a queue-full, marshal, or
// transport error is logged but does not change the 204 response.
//
// notifyRemoved is optional. Passing nil disables the final Notification
// (useful for tests that don't exercise the subscription pipeline).
// Production callers pass (*Manager).NotifyRemoved as a method value.
//
// Option B: takes a func rather than an interface so core avoids defining
// a redundant SubscriberNotifier interface (the consumer-side interface
// stays in server's internal/handler package per Pike rule).
func HandleDeleteSubscription(subStore *memory.SubscriptionStore, notifyRemoved func(ctx context.Context, sub sep2.Subscription) error) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			encoding.MethodNotAllowed(w, "DELETE")
			return
		}

		edevID := r.PathValue("id")
		subID := r.PathValue("subId")

		// Look up the subscription record before Delete so we can hand
		// the pre-delete value to the notifier: once Delete returns,
		// the record is gone from the store. ErrNotFound here is the
		// "DELETE on unknown ID" path; surface 404 immediately and skip
		// the notify (no subscriber existed).
		sub, getErr := subStore.Store.Get(r.Context(), subID)
		if getErr != nil {
			if errors.Is(getErr, store.ErrNotFound) {
				http.Error(w, "not found", http.StatusNotFound)
				return
			}
			srverr.Internal(w, r, fmt.Errorf("the lookup before delete failed: %w", getErr))
			return
		}

		// ownershipGate already confirmed the caller owns or manages
		// {id}; it never sees {subId}. Without this check, a subId
		// created under a different EndDevice was still deletable by
		// any caller who could reach this route (#435). 404, not 403,
		// so the response can't be used to probe another device's
		// subscription IDs.
		if sub.Href != subscriptionHref(edevID, subID) {
			// Path id and subId only: sub.Href would name the other
			// EndDevice this subId actually belongs to.
			log.Printf("subscription: refused cross-device delete for EndDevice %q subId %q", edevID, subID)
			http.Error(w, "not found", http.StatusNotFound)
			return
		}

		if err := subStore.Delete(r.Context(), subID); err != nil {
			if errors.Is(err, store.ErrNotFound) {
				http.Error(w, "not found", http.StatusNotFound)
				return
			}
			srverr.Internal(w, r, err)
			return
		}

		// Best-effort final Removed Notification. A nil notifyRemoved or an
		// error from it is logged but does not change the 204: the spec
		// doesn't require this and the subscription has already been
		// deleted from authoritative state.
		if notifyRemoved != nil {
			if err := notifyRemoved(r.Context(), sub); err != nil {
				log.Printf("subscription: notify removed for %q to %q: %v",
					sub.Href, redactURI(sub.NotificationURI), err)
			}
		}

		w.WriteHeader(http.StatusNoContent)
	}
}
