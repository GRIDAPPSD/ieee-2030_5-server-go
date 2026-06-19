// CSIP V1.2 §6.19 — Advanced Subscription.
//
// CORE-019 asserts three behaviors layered on top of CORE-018's basic
// subscription flow:
//
//	(a) the server accepts >= 2 parallel Subscriptions against the same
//	    SubscribedResource;
//	(b) DELETE /edev/{id}/sub/{subId} on one of the parallel
//	    subscriptions removes it — verified by Notify-after-delete
//	    delivering only to the survivor;
//	(c) POST /edev/{id}/sub with a malformed XML body returns HTTP 400
//	    Bad Request.
//
// V1.2 procedure step → assertion mapping:
//
//	Step 1: POST sub-A and sub-B, both naming the FSAList href.
//	                                          ──► postSubscriptionExpect201 ×2
//	                                          ──► GET /edev/{id}/sub.All == 2
//	Step 2: DELETE one subscription via /edev/{id}/sub/{subId}.
//	                                          ──► deleteSubscriptionExpect204
//	                                          ──► GET /edev/{id}/sub.All == 1
//	                                          ──► Notify on the subscribed href
//	                                              delivers to exactly the
//	                                              survivor's NotificationURI
//	Step 3: POST malformed XML to /edev/{id}/sub.
//	                                          ──► expect 400 Bad Request
//
// Cancellation semantics interpretation: V1.2 §6.19 says "deleting one
// fires a cancellation Notification." The server's authoritative
// delete path (HandleDeleteSubscription) returns 204 No Content and
// removes the subscription from the store; it does not on its own
// emit a final Notification to the deleted subscriber. The functional
// shape the procedure cares about is "after delete, the deleted
// subscriber no longer receives Notifications" — i.e. the cancellation
// is observable as a Notify fan-out that hits only the survivors. We
// assert that shape here. Production support for emitting a Removed-
// status Notification to the just-deleted subscriber, if needed, is
// IEEE-091's territory (MAINT-006 server-side subscription terminate);
// CORE-019's procedure is satisfied by observing that the survivor is
// the only delivery.
//
// IEEE-087 / Phase 6.

package csip_test

import (
	"context"
	"encoding/xml"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-go/internal/subscription"
	"gitlab.pnnl.gov/arista/ieee-2030_5/ieee-2030_5-core/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-go/test/csip/csiptest"
)

const (
	core019EndDeviceID = "edev-core019"
	core019FSAHref     = "/edev/" + core019EndDeviceID + "/fsa"
)

// TestCORE_019_AdvancedSubscription implements CSIP V1.2 §6.19.
func TestCORE_019_AdvancedSubscription(t *testing.T) {
	t.Parallel()

	srv := csiptest.BootServer(t)
	receiverA := csiptest.NewNotificationReceiver(t)
	receiverB := csiptest.NewNotificationReceiver(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	// Real subscription.Manager wired to the booted server's store, as
	// CORE-018. Production type, production wiring; only the caller —
	// Notify — is driven from the test.
	mgr := subscription.NewManager(srv.Stores.Subscriptions, 2, 16)
	go mgr.Start(ctx)

	// --- (a) Parallel subscriptions on the same resource. ----------

	subA := sep2.Subscription{
		SubscribedResource: core019FSAHref,
		NotificationURI:    receiverA.URL(),
		Encoding:           sep2.EncodingXML,
	}
	subB := sep2.Subscription{
		SubscribedResource: core019FSAHref,
		NotificationURI:    receiverB.URL(),
		Encoding:           sep2.EncodingXML,
	}
	locationA := postSubscriptionExpect201(t, srv, core019EndDeviceID, &subA)
	locationB := postSubscriptionExpect201(t, srv, core019EndDeviceID, &subB)
	if locationA == "" || locationB == "" {
		t.Fatalf("CORE-019 (a): empty Location: A=%q B=%q", locationA, locationB)
	}
	if locationA == locationB {
		t.Fatalf("CORE-019 (a): A and B got the same Location %q — distinct subs expected", locationA)
	}

	listURL := srv.BaseURL + "/edev/" + core019EndDeviceID + "/sub"
	parallel := getSubscriptionList(t, srv, listURL)
	if parallel.All != 2 {
		t.Errorf("CORE-019 (a): SubscriptionList.All = %d, want 2", parallel.All)
	}
	if len(parallel.Subscription) != 2 {
		t.Fatalf("CORE-019 (a): len(Subscription) = %d, want 2", len(parallel.Subscription))
	}

	// Sanity: both subs must be discoverable via the Manager's
	// ListByResource path — otherwise the survivor assertion in (b)
	// would be vacuous.
	listed, err := srv.Stores.Subscriptions.ListByResource(ctx, core019FSAHref)
	if err != nil {
		t.Fatalf("CORE-019 (a) baseline: ListByResource: %v", err)
	}
	if len(listed) != 2 {
		t.Fatalf("CORE-019 (a) baseline: ListByResource returned %d, want 2", len(listed))
	}

	// --- (b) DELETE one sub, observe survivor-only delivery. -------

	// Extract sub-A's id from its Location (/edev/{id}/sub/{subId}).
	subAID := lastPathSegment(locationA)
	if subAID == "" {
		t.Fatalf("CORE-019 (b): could not extract sub id from Location %q", locationA)
	}

	deleteURL := srv.BaseURL + locationA
	deleteSubscriptionExpect204(t, srv, deleteURL)

	survivor := getSubscriptionList(t, srv, listURL)
	if survivor.All != 1 {
		t.Errorf("CORE-019 (b): post-delete SubscriptionList.All = %d, want 1", survivor.All)
	}
	if len(survivor.Subscription) != 1 {
		t.Fatalf("CORE-019 (b): len(Subscription) = %d, want 1", len(survivor.Subscription))
	}
	if got := lastPathSegment(survivor.Subscription[0].Href); got == subAID {
		t.Errorf("CORE-019 (b): survivor id = %q, want != deleted id (%q)", got, subAID)
	}

	// Drive a Notify on the subscribed resource. Only the survivor
	// (receiverB) should fire. ReceiverA was attached to the deleted
	// sub and must remain empty.
	mgr.Notify(ctx, core019FSAHref, sep2.NotificationStatusChanged)

	gotB, ok := receiverB.Wait(1, 2*time.Second)
	if !ok {
		t.Fatalf("CORE-019 (b): survivor receiver timed out; received %d, want >=1", len(gotB))
	}
	if len(gotB) != 1 {
		t.Errorf("CORE-019 (b): survivor receiver got %d, want 1", len(gotB))
	}
	// IEEE-100 / CSIP V1.2 §11.6: the DELETE itself fires a final
	// Removed Notification (Status=3) to the deleted subscriber so it
	// can flush local state. So receiverA must have exactly one
	// Notification — the Removed — and a subsequent Notify on the
	// resource must NOT add a second (the sub is gone from
	// ListByResource).
	gotA, okA := receiverA.Wait(1, 2*time.Second)
	if !okA {
		t.Fatalf("CORE-019 (b): deleted-sub receiver never got the final Removed Notification; received %d", len(gotA))
	}
	if len(gotA) != 1 {
		t.Errorf("CORE-019 (b): deleted-sub receiver got %d Notifications, want exactly 1 (Removed)", len(gotA))
	}
	if gotA[0].Notification == nil {
		t.Fatalf("CORE-019 (b): Removed Notification did not parse")
	}
	if gotA[0].Notification.Status != sep2.NotificationStatusRemoved {
		t.Errorf("CORE-019 (b): final Notification Status = %d, want %d (Removed)",
			gotA[0].Notification.Status, sep2.NotificationStatusRemoved)
	}
	// A second beat to confirm the post-DELETE Notify() did not
	// double-deliver to the gone subscription.
	time.Sleep(100 * time.Millisecond)
	if got := receiverA.Count(); got != 1 {
		t.Errorf("CORE-019 (b): deleted-sub receiver got %d Notifications after post-delete Notify, want 1 (Removed only)", got)
	}

	// --- (c) Malformed Notification body → HTTP 400. ---------------
	//
	// V1.2 §6.19 prose: "submitting a malformed Notification body
	// returns HTTP 400." The wire-level expression in the CSIP server
	// surface is a POST to the subscription endpoint with an
	// undecodable XML payload — the create-subscription handler does
	// the body parse, so this is the same surface CORE-019 (a)
	// exercised, driven with a broken body.
	malformedURL := srv.BaseURL + "/edev/" + core019EndDeviceID + "/sub"
	resp := doSubRequest(t, srv, http.MethodPost, malformedURL,
		"application/sep+xml", []byte("<not-a-subscription"))
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("CORE-019 (c): POST malformed body status = %d, want 400", resp.StatusCode)
	}
}

// getSubscriptionList GETs /edev/{id}/sub and unmarshals into a
// SubscriptionList. Strict 200 OK.
func getSubscriptionList(t *testing.T, srv *csiptest.BootedServer, url string) sep2.SubscriptionList {
	t.Helper()
	resp := doSubRequest(t, srv, http.MethodGet, url, "", nil)
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s: status = %d, want 200", url, resp.StatusCode)
	}
	var list sep2.SubscriptionList
	dec := xml.NewDecoder(resp.Body)
	if err := dec.Decode(&list); err != nil {
		t.Fatalf("decode SubscriptionList: %v", err)
	}
	return list
}

// deleteSubscriptionExpect204 DELETEs a subscription and asserts 204
// No Content.
func deleteSubscriptionExpect204(t *testing.T, srv *csiptest.BootedServer, url string) {
	t.Helper()
	resp := doSubRequest(t, srv, http.MethodDelete, url, "", nil)
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("DELETE %s: status = %d, want 204", url, resp.StatusCode)
	}
}

// lastPathSegment returns the trailing slash-delimited segment, or ""
// for empty input or a trailing slash. Inlined here rather than pulled
// from path/path because the input is a Location header and the
// failure mode we care about is "the handler returned a Location
// without a final segment", which path.Base would mask by returning
// ".".
func lastPathSegment(p string) string {
	if p == "" {
		return ""
	}
	if i := strings.LastIndex(p, "/"); i >= 0 && i+1 < len(p) {
		return p[i+1:]
	}
	return ""
}
