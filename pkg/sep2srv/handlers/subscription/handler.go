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
	"strings"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2/encoding"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/logsafe"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/idmint"
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

// maxRequestBody caps how much of a Subscription document the create and PUT
// handlers read; a longer body is cut short and fails to parse.
const maxRequestBody = 1 << 20

// subscriptionHref builds the href a subscription created under EndDevice
// edevID with storage id id carries on the wire. The create and delete
// paths below both call this so the two can't drift apart (#435).
func subscriptionHref(edevID, id string) string {
	return fmt.Sprintf("/edev/%s/sub/%s", edevID, id)
}

// SubscriberEndDevice returns the EndDevice id a subscription href was minted
// under by subscriptionHref, and false for any href not of that exact form.
func SubscriberEndDevice(href string) (edevID string, ok bool) {
	rest, found := strings.CutPrefix(href, "/edev/")
	if !found {
		return "", false
	}
	edevID, id, found := strings.Cut(rest, "/sub/")
	if !found || edevID == "" || id == "" || strings.Contains(edevID, "/") || strings.Contains(id, "/") {
		return "", false
	}
	return edevID, true
}

// ErrSubscribedResourceRefused is the error a ReadCheck wraps when the
// subscriber may not read the subscribed resource, or the href names no
// resource it could read. Any other ReadCheck error means the check could not
// be completed.
var ErrSubscribedResourceRefused = errors.New("subscribedResource refused")

// ReadCheck reports whether the caller of r may read resource, the
// subscribedResource of the Subscription r creates.
type ReadCheck func(r *http.Request, resource string) error

// maxLoggedResourceLen bounds a client-supplied subscribedResource in a log line.
const maxLoggedResourceLen = 128

func loggedResource(resource string) string {
	if len(resource) > maxLoggedResourceLen {
		return resource[:maxLoggedResourceLen]
	}
	return resource
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
	result := store.ListResult[sep2.Subscription]{All: uint32(len(records))}
	for _, rec := range paging.Slice(records, func(r memory.SubscriptionRecord) string { return r.ID }, opts) {
		result.Items = append(result.Items, rec.Subscription)
	}
	result.Results = uint32(len(result.Items))
	return result
}

// vetSubscription applies the checks a Subscription document must pass before
// it is stored: the caller may read the subscribedResource, and the
// notificationURI passes the destination policy. It writes the refusal and
// reports false when the document is not accepted. POST creates and PUT
// replaces through this one definition, so a replacement is refused for exactly
// what creation refuses.
func vetSubscription(w http.ResponseWriter, r *http.Request, edevID string, sub sep2.Subscription, validate func(ctx context.Context, uri string) error, canRead ReadCheck) bool {
	if canRead == nil {
		srverr.Internal(w, r, errors.New("no subscribedResource check wired"))
		return false
	}
	if err := canRead(r, sub.SubscribedResource); err != nil {
		if !errors.Is(err, ErrSubscribedResourceRefused) {
			srverr.Internal(w, r, fmt.Errorf("checking subscribedResource: %w", err))
			return false
		}
		log.Printf("subscription: refused subscribedResource %s for EndDevice %s: %s", logsafe.Quote(loggedResource(sub.SubscribedResource)), logsafe.Quote(edevID), logsafe.Err(err))
		// IEEE 2030.5-2018 8.7.3.4 rule m: a Subscription the server
		// cannot accept is answered 400. One body for an unreadable and a
		// nonexistent resource, so the answer does not reveal which.
		http.Error(w, "subscribedResource refused", http.StatusBadRequest)
		return false
	}

	if err := validate(r.Context(), sub.NotificationURI); err != nil {
		target := redactURI(sub.NotificationURI)
		var dnsErr *net.DNSError
		switch {
		case errors.Is(err, ErrRefusedDestination):
			log.Printf("subscription: refused notificationURI %s for EndDevice %s: %s", logsafe.Quote(target), logsafe.Quote(edevID), logsafe.Err(err))
		case errors.Is(err, ErrDestinationUnresolved) && r.Context().Err() != nil:
			log.Printf("subscription: request ended while resolving notificationURI %s for EndDevice %s, refusing: %s", logsafe.Quote(target), logsafe.Quote(edevID), logsafe.Err(err))
		case errors.Is(err, ErrDestinationUnresolved) && errors.As(err, &dnsErr) && dnsErr.IsNotFound:
			log.Printf("subscription: no such host for notificationURI %s for EndDevice %s, refusing: %s", logsafe.Quote(target), logsafe.Quote(edevID), logsafe.Err(err))
		case errors.Is(err, ErrDestinationUnresolved):
			log.Printf("subscription: could not resolve notificationURI %s for EndDevice %s, refusing: %s", logsafe.Quote(target), logsafe.Quote(edevID), logsafe.Err(err))
		default:
			log.Printf("subscription: validating notificationURI %s for EndDevice %s: %s", logsafe.Quote(target), logsafe.Quote(edevID), logsafe.Err(err))
			srverr.Internal(w, r, err)
			return false
		}
		// One response for both: a distinct one for an unresolvable name
		// would tell the client whether an internal name resolves.
		http.Error(w, "notificationURI refused", http.StatusBadRequest)
		return false
	}
	return true
}

// HandleCreateSubscription returns a handler for POST /edev/{id}/sub.
//
// validate vets the notificationURI before anything is stored. Pass
// (*Manager).ValidateNotificationURI so creation and delivery apply the same
// DestinationPolicy; nil applies the default policy.
//
// canRead decides whether the caller may read the subscribedResource. A nil
// canRead refuses every create, since without it any resource could be
// subscribed to.
func HandleCreateSubscription(subStore *memory.SubscriptionStore, validate func(ctx context.Context, uri string) error, canRead ReadCheck) http.HandlerFunc {
	if validate == nil {
		log.Print("subscription: no notificationURI validator wired; POST /edev/{id}/sub applies the default DestinationPolicy")
		validate = DestinationPolicy{}.ValidateNotificationURI
	}
	if canRead == nil {
		log.Print("subscription: no subscribedResource check wired; POST /edev/{id}/sub and PUT /edev/{id}/sub/{subId} refuse every Subscription")
	}
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			encoding.MethodNotAllowed(w, "POST")
			return
		}

		edevID := r.PathValue("id")

		body, err := io.ReadAll(io.LimitReader(r.Body, maxRequestBody))
		if err != nil {
			http.Error(w, "read body failed", http.StatusBadRequest)
			return
		}

		var sub sep2.Subscription
		if err := xml.Unmarshal(body, &sub); err != nil {
			srverr.BadRequestMessage(w, r, "invalid XML", err)
			return
		}

		if !vetSubscription(w, r, edevID, sub, validate, canRead) {
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
			id = idmint.Prefixed("sub")
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

// errForeignSubscription is what getOwnedSubscription returns for a record
// stored under a different EndDevice than the path names.
var errForeignSubscription = errors.New("subscription belongs to another EndDevice")

// getOwnedSubscription reads subscription subID and confirms it was created
// under EndDevice edevID, by the href the create path stamped on it. It
// returns store.ErrNotFound for an absent record and errForeignSubscription
// for another device's, which callers answer alike so a response cannot be
// used to probe another device's subscription ids.
func getOwnedSubscription(ctx context.Context, subStore *memory.SubscriptionStore, edevID, subID string) (sep2.Subscription, error) {
	sub, err := subStore.Store.Get(ctx, subID)
	if err != nil {
		return sep2.Subscription{}, err
	}
	if sub.Href != subscriptionHref(edevID, subID) {
		return sep2.Subscription{}, errForeignSubscription
	}
	return sub, nil
}

// lookupOwned reads subscription subID for the EndDevice edevID and, when it
// cannot be served, writes the response and returns false. Absent and foreign
// records both answer 404, so the response cannot probe another device's
// subscription ids; action names the request in the log line for a foreign one.
func lookupOwned(w http.ResponseWriter, r *http.Request, subStore *memory.SubscriptionStore, edevID, subID, action string) (sep2.Subscription, bool) {
	sub, err := getOwnedSubscription(r.Context(), subStore, edevID, subID)
	switch {
	case err == nil:
		return sub, true
	case errors.Is(err, store.ErrNotFound):
		http.Error(w, "not found", http.StatusNotFound)
	case errors.Is(err, errForeignSubscription):
		// Path id and subId only: sub.Href would name the other EndDevice.
		log.Printf("subscription: refused cross-device %s for EndDevice %s subId %s", action, logsafe.Quote(edevID), logsafe.Quote(subID))
		http.Error(w, "not found", http.StatusNotFound)
	default:
		srverr.Internal(w, r, fmt.Errorf("reading the subscription for %s: %w", action, err))
	}
	return sep2.Subscription{}, false
}

// HandleSubscription returns a handler for GET, HEAD and PUT on
// /edev/{id}/sub/{subId}, the Location POST /edev/{id}/sub hands back.
// DELETE on the same path is [HandleDeleteSubscription].
//
// ownershipGate has confirmed the caller owns {id}; this handler confirms the
// subscription was created under {id}. Another device's subscription, like an
// absent one, is a 404 on all three methods, matching DELETE.
//
// PUT replaces the subscription's own fields. Its href is the stored one, never
// the document's, so the owner and the id cannot be changed by a PUT, and the
// document passes the checks creation applies ([vetSubscription]). validate
// and canRead are those of [HandleCreateSubscription], and a nil canRead
// refuses every PUT for the same reason.
func HandleSubscription(subStore *memory.SubscriptionStore, validate func(ctx context.Context, uri string) error, canRead ReadCheck) http.HandlerFunc {
	// The unwired cases are logged once, by HandleCreateSubscription, which
	// the same assembly mounts with the same arguments.
	if validate == nil {
		validate = DestinationPolicy{}.ValidateNotificationURI
	}
	return func(w http.ResponseWriter, r *http.Request) {
		edevID := r.PathValue("id")
		subID := r.PathValue("subId")

		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodPut:
		default:
			encoding.MethodNotAllowed(w, "GET, HEAD, PUT")
			return
		}

		stored, ok := lookupOwned(w, r, subStore, edevID, subID, "access")
		if !ok {
			return
		}

		if r.Method != http.MethodPut {
			encoding.WriteXML(w, http.StatusOK, &stored)
			return
		}

		body, err := io.ReadAll(io.LimitReader(r.Body, maxRequestBody))
		if err != nil {
			http.Error(w, "read body failed", http.StatusBadRequest)
			return
		}
		var sub sep2.Subscription
		if err := xml.Unmarshal(body, &sub); err != nil {
			srverr.BadRequestMessage(w, r, "invalid XML", err)
			return
		}
		if !vetSubscription(w, r, edevID, sub, validate, canRead) {
			return
		}
		sub.Href = stored.Href

		if err := subStore.Update(r.Context(), subID, sub); err != nil {
			if errors.Is(err, store.ErrNotFound) {
				// Deleted between the read and the write.
				http.Error(w, "not found", http.StatusNotFound)
				return
			}
			srverr.Internal(w, r, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
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
		//
		// ownershipGate already confirmed the caller owns or manages
		// {id}; it never sees {subId}. Without the ownership half of the
		// lookup, a subId created under a different EndDevice was still
		// deletable by any caller who could reach this route (#435). 404,
		// not 403, so the response can't be used to probe another device's
		// subscription IDs.
		sub, ok := lookupOwned(w, r, subStore, edevID, subID, "delete")
		if !ok {
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
				log.Printf("subscription: notify removed for %s to %s: %s",
					logsafe.Quote(sub.Href), logsafe.Quote(redactURI(sub.NotificationURI)), logsafe.Err(err))
			}
		}

		w.WriteHeader(http.StatusNoContent)
	}
}
