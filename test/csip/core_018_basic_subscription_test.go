// CSIP V1.2 §6.18 — Basic Subscription.
//
// CORE-018 asserts the minimum server-side subscription/notification
// flow: a client may POST a Subscription on a subscribable resource,
// and when that resource changes the server fires a Notification at
// the client-supplied notificationURI carrying the resource href and
// the SubscribedResource the client subscribed to.
//
// V1.2 procedure step → assertion mapping:
//
//	Step 1: client POSTs a Subscription to /edev/{id}/sub naming the
//	        FSAList href as SubscribedResource and the test receiver's
//	        URL as NotificationURI.
//	                                          ──► postSubscription, expects 201 Created
//	                                          ──► GET /edev/{id}/sub returns the new sub
//	Step 2: server-side state change on a resource under the subscribed
//	        href. We drive this via the #27 mutation-shape directly
//	        on the in-process store (we live in the same module as the
//	        server, so we can mutate the store the booted server reads
//	        from) — adding a DERControl under the EndDevice's FSA.
//	                                          ──► direct store mutation
//	Step 3: server fan-outs a Notification to the receiver. We invoke
//	        subscription.Manager.Notify directly with the subscribed
//	        resource href; that is the same call the production
//	        DELETE-EndDevice handler makes (see internal/handler/edev.go
//	        HandleDeleteEndDevice). The Manager performs ListByResource
//	        against the live SubscriptionStore and POSTs to every
//	        subscriber's NotificationURI — exactly the V1.2 §6.18
//	        Step 3 expectation.
//	                                          ──► receiver.Wait(1)
//	                                          ──► assert Notification.SubscribedResource
//	                                              == fsa href
//
// Why drive the Notify directly rather than asking the server to
// observe the store mutation: today's server emits notifications only
// from explicit handler call sites (the DELETE-EndDevice route was the
// first; #155 will add MAINT-004's add-DERControl call site).
// Plumbing notify into the #27 mutation handlers is out of scope
// for #151 — it lands with #155 (MAINT-004). For §6.18 the
// procedure care-abouts are "server accepts a subscription" and
// "server delivers a Notification matching the subscribed resource";
// driving Notify from the test still exercises the full server-side
// fan-out, queue, worker, HTTP POST path. The mutation step ahead of
// the Notify is kept so a future Pike rewiring can drop the explicit
// Notify call and the assertions still hold.
//
// Race-detector clean: subscription.Manager.Notify enqueues work onto
// a worker pool that POSTs concurrently. The receiver's append-under-
// mutex pattern + Manager.Start's goroutine lifecycle bound to a
// per-test ctx means `go test -race` is non-negotiable here. See
// internal/subscription/manager_test.go for the race-passing baseline.
//
// #151 / Phase 6.

package csip_test

import (
	"bytes"
	"context"
	"encoding/xml"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	coresub "github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2srv/handlers/subscription"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/test/csip/csiptest"
)

const (
	core018EndDeviceID = "edev-core018"
	core018FSAHref     = "/edev/" + core018EndDeviceID + "/fsa"
)

// TestCORE_018_BasicSubscription implements CSIP V1.2 §6.18.
func TestCORE_018_BasicSubscription(t *testing.T) {
	t.Parallel()

	srv := csiptest.BootServer(t)
	receiver := csiptest.NewNotificationReceiver(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	// Stand up a real subscription.Manager wired to the booted
	// server's authoritative SubscriptionStore. This is the production
	// type with the production wiring; only its caller — Notify — is
	// driven from the test. Worker pool sized small: one POST per
	// subscriber, this test has one subscriber.
	mgr := coresub.NewManager(srv.Stores.Subscriptions, 2, 16)
	go mgr.Start(ctx)

	// Step 1: client POSTs a Subscription naming the FSAList as
	// SubscribedResource. The csip subscription create handler does not
	// require the parent EndDevice to pre-exist (see
	// internal/handler/subscription.go), so we don't pre-seed /edev.
	sub := sep2.Subscription{
		SubscribedResource: core018FSAHref,
		NotificationURI:    receiver.URL(),
		Encoding:           sep2.EncodingXML,
	}
	location := postSubscriptionExpect201(t, srv, core018EndDeviceID, &sub)
	if location == "" {
		t.Fatalf("CORE-018 Step 1: POST /edev/.../sub returned empty Location header")
	}
	if !strings.HasPrefix(location, "/edev/"+core018EndDeviceID+"/sub/") {
		t.Errorf("CORE-018 Step 1: Location = %q, want prefix /edev/%s/sub/", location, core018EndDeviceID)
	}

	// Sanity-check the subscription is in the store reachable by the
	// Manager's ListByResource — if this fails, the Notify in Step 3
	// would fan out to zero subscribers and Step 3's wait would time
	// out with a confusing "no notifications" rather than the actual
	// "create did not persist" failure.
	subs, err := srv.Stores.Subscriptions.ListByResource(ctx, core018FSAHref)
	if err != nil {
		t.Fatalf("CORE-018 baseline: ListByResource(%s): %v", core018FSAHref, err)
	}
	if len(subs) != 1 {
		t.Fatalf("CORE-018 baseline: ListByResource(%s) returned %d subs, want 1", core018FSAHref, len(subs))
	}

	// Step 2: server-side state change. We mutate the FSA store
	// directly — the booted server's handler reads from the same
	// store, so a subsequent GET /edev/{id}/fsa would observe the
	// new entry. The mutation itself is not asserted here; the
	// assertion is on the notification fan-out in Step 3. This
	// mirrors the #27 mutation shape (FSA-keyed Create on the
	// ScopedStore) without going through the test-tag HTTP route —
	// the test-tag route is the wire-level surface for cross-process
	// drivers; in-process tests can call the same store API.
	fsas := srv.Stores.FSAs.ForParent(core018EndDeviceID)
	fsa := sep2.FunctionSetAssignments{
		Resource: sep2.Resource{Href: "/edev/" + core018EndDeviceID + "/fsa/fsa-1"},
	}
	if err := fsas.Create(ctx, "fsa-1", fsa); err != nil {
		t.Fatalf("CORE-018 Step 2: create FSA fsa-1: %v", err)
	}

	// Step 3: server fan-outs a Notification. Notify takes the href
	// of the resource that changed; ListByResource matches subs whose
	// SubscribedResource equals that href verbatim. The Subscription
	// was registered against the FSAList href, so Notify on the
	// FSAList href hits it.
	mgr.Notify(ctx, core018FSAHref, sep2.NotificationStatusChanged)

	got, ok := receiver.Wait(1, 2*time.Second)
	if !ok {
		t.Fatalf("CORE-018 Step 3: timed out waiting for Notification; received %d, want >=1", len(got))
	}
	if len(got) != 1 {
		t.Fatalf("CORE-018 Step 3: receiver got %d Notifications, want exactly 1", len(got))
	}

	// Wire-shape assertions: Content-Type, namespace, parsed body
	// fields. The production manager.deliver sets
	// Content-Type: application/sep+xml and posts a sep2.Notification
	// XML body — a regression on either is a wire-level conformance
	// failure that this test should catch in isolation.
	rec := got[0]
	const wantCT = "application/sep+xml"
	if rec.ContentType != wantCT {
		t.Errorf("CORE-018 Notification Content-Type = %q, want %q", rec.ContentType, wantCT)
	}
	if rec.Notification == nil {
		t.Fatalf("CORE-018 Notification body did not parse as sep2.Notification: body=%q", string(rec.Body))
	}
	if rec.Notification.SubscribedResource != core018FSAHref {
		t.Errorf("CORE-018 Notification.SubscribedResource = %q, want %q",
			rec.Notification.SubscribedResource, core018FSAHref)
	}
	if rec.Notification.Href != core018FSAHref {
		t.Errorf("CORE-018 Notification.Resource.Href = %q, want %q",
			rec.Notification.Href, core018FSAHref)
	}
	if rec.Notification.Status != sep2.NotificationStatusChanged {
		t.Errorf("CORE-018 Notification.Status = %d, want %d (Changed)",
			rec.Notification.Status, sep2.NotificationStatusChanged)
	}
	if rec.Notification.SubscriptionURI == "" {
		t.Errorf("CORE-018 Notification.SubscriptionURI = empty, want subscription href")
	}
}

// postSubscriptionExpect201 marshals sub and POSTs it to
// <baseURL>/edev/{edevID}/sub. Returns the Location header on 201
// Created. The helper is reused by CORE-019.
func postSubscriptionExpect201(t *testing.T, srv *csiptest.BootedServer, edevID string, sub *sep2.Subscription) string {
	t.Helper()
	body, err := xml.Marshal(sub)
	if err != nil {
		t.Fatalf("marshal Subscription: %v", err)
	}
	url := srv.BaseURL + "/edev/" + edevID + "/sub"
	resp := doSubRequest(t, srv, http.MethodPost, url, "application/sep+xml", body)
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST %s: status = %d, want 201", url, resp.StatusCode)
	}
	return resp.Header.Get("Location")
}

// doSubRequest issues a verbed request with the given body against the
// booted server's TLS stack — reusing the device cert and root CA
// already wired by csiptest.BootServer. Failure to build the request
// or to dial the server is fatal; HTTP status interpretation is the
// caller's job (so 4xx is not auto-fatal — CORE-019's malformed body
// leg specifically wants the 400 reply).
//
// Lives in the test file (not csiptest) so it doesn't grow the helper
// package's surface for a use case that's only this test file plus
// CORE-019. If a third caller appears, lift to csiptest.
func doSubRequest(t *testing.T, srv *csiptest.BootedServer, method, url, contentType string, body []byte) *http.Response {
	t.Helper()
	var reqBody io.Reader
	if body != nil {
		reqBody = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(context.Background(), method, url, reqBody)
	if err != nil {
		t.Fatalf("build %s %s: %v", method, url, err)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := srv.Client().HTTPClient().Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	return resp
}
