package assembly_test

import (
	"context"
	"encoding/xml"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
)

// GRIDAPPSD/ieee-2030_5-server-go#1267: GET, HEAD and PUT on the Location
// POST /edev/{id}/sub returns, driven through the assembled router so the
// ownership gate and the create-time checks are the real ones.

func subInstanceRequest(t *testing.T, srv *httptest.Server, method, path, body string) (int, []byte) {
	t.Helper()
	req, err := http.NewRequest(method, srv.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatalf("build %s %s: %v", method, path, err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/sep+xml")
	}
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read %s %s: %v", method, path, err)
	}
	return resp.StatusCode, raw
}

func subInstanceBody(resource, uri string) string {
	return `<Subscription xmlns="urn:ieee:std:2030.5:ns"><subscribedResource>` + xmlEscape(resource) +
		`</subscribedResource><encoding>0</encoding><level>+S1</level><limit>7</limit>` +
		`<notificationURI>` + xmlEscape(uri) + `</notificationURI></Subscription>`
}

func TestSubscriptionInstance_OwnerReadsAndReplacesAtItsLocation(t *testing.T) {
	t.Parallel()
	f := newSubscribedResourceFleet(t)
	rcv := newNotificationReceiver(t)

	resp, err := f.a.Client().Post(f.a.URL+"/edev/"+subDevAID+"/sub", "application/sep+xml",
		strings.NewReader(subInstanceBody("/edev/"+subDevAID+"/frp", rcv.srv.URL)))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	_ = resp.Body.Close()
	loc := resp.Header.Get("Location")
	if resp.StatusCode != http.StatusCreated || loc == "" {
		t.Fatalf("POST status %d Location %q, want 201 and a Location", resp.StatusCode, loc)
	}

	status, raw := subInstanceRequest(t, f.a, http.MethodGet, loc, "")
	if status != http.StatusOK {
		t.Fatalf("owner GET %s: status %d, want 200; body=%q", loc, status, raw)
	}
	var got sep2.Subscription
	if err := xml.Unmarshal(raw, &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Href != loc || got.SubscribedResource != "/edev/"+subDevAID+"/frp" || got.NotificationURI != rcv.srv.URL || got.Limit != 7 {
		t.Errorf("GET served %+v, want href %q, the created subscribedResource, notificationURI and limit 7", got, loc)
	}

	if status, raw := subInstanceRequest(t, f.a, http.MethodHead, loc, ""); status != http.StatusOK || len(raw) != 0 {
		t.Errorf("owner HEAD: status %d with %d body bytes, want 200 and none", status, len(raw))
	}

	if status, raw := subInstanceRequest(t, f.a, http.MethodPut, loc, subInstanceBody("/edev/"+subDevAID+"/der", rcv.srv.URL)); status != http.StatusNoContent {
		t.Fatalf("owner PUT: status %d, want 204; body=%q", status, raw)
	}
	_, raw = subInstanceRequest(t, f.a, http.MethodGet, loc, "")
	got = sep2.Subscription{}
	if err := xml.Unmarshal(raw, &got); err != nil {
		t.Fatalf("decode after PUT: %v", err)
	}
	if got.Href != loc || got.SubscribedResource != "/edev/"+subDevAID+"/der" {
		t.Errorf("after PUT served %+v, want href %q and subscribedResource /edev/%s/der", got, loc, subDevAID)
	}
	if n := storedFor(t, f.stores, "/edev/"+subDevAID+"/frp"); n != 0 {
		t.Errorf("old subscribedResource still has %d stored subscriptions, want 0", n)
	}
	if n := storedFor(t, f.stores, "/edev/"+subDevAID+"/der"); n != 1 {
		t.Errorf("new subscribedResource has %d stored subscriptions, want 1", n)
	}

	// Replacement is refused for what creation refuses: A cannot subscribe to
	// B's resource, so it cannot move its subscription there.
	if status, _ := subInstanceRequest(t, f.a, http.MethodPut, loc, subInstanceBody("/edev/"+subDevBID+"/frp", rcv.srv.URL)); status != http.StatusBadRequest {
		t.Errorf("PUT to another device's resource: status %d, want 400", status)
	}
	if n := storedFor(t, f.stores, "/edev/"+subDevBID+"/frp"); n != 0 {
		t.Errorf("refused PUT left %d subscriptions on B's resource, want 0", n)
	}
}

func TestSubscriptionInstance_OtherDevicesAndManagersAreRefused(t *testing.T) {
	t.Parallel()
	f := newSubscribedResourceFleet(t)
	ctx := context.Background()
	href := "/edev/" + subDevAID + "/sub/sub-a"
	if err := f.stores.Subscriptions.Create(ctx, "sub-a", sep2.Subscription{
		SubscribableResource: sep2.SubscribableResource{Resource: sep2.Resource{Href: href}},
		SubscribedResource:   "/edev/" + subDevAID,
		NotificationURI:      "https://subscriber.test/notify",
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	body := subInstanceBody("/edev/"+subDevBID, "https://subscriber.test/hijack")

	// B owns its own path id, not A's subscription: 404 on every method.
	crossPath := "/edev/" + subDevBID + "/sub/sub-a"
	for _, method := range []string{http.MethodGet, http.MethodHead, http.MethodPut} {
		b := ""
		if method == http.MethodPut {
			b = body
		}
		if status, raw := subInstanceRequest(t, f.b, method, crossPath, b); status != http.StatusNotFound {
			t.Errorf("B %s %s: status %d, want 404; body=%q", method, crossPath, status, raw)
		}
	}

	// B addressing A's own path is refused by the ownership gate.
	for _, method := range []string{http.MethodGet, http.MethodHead, http.MethodPut} {
		b := ""
		if method == http.MethodPut {
			b = body
		}
		if status, raw := subInstanceRequest(t, f.b, method, href, b); status != http.StatusForbidden {
			t.Errorf("B %s %s: status %d, want 403; body=%q", method, href, status, raw)
		}
	}

	// The manager of B holds no authority over A at all.
	if status, _ := subInstanceRequest(t, f.m, http.MethodGet, href, ""); status != http.StatusForbidden {
		t.Errorf("manager of B GET A's subscription: status %d, want 403", status)
	}

	// On a device it manages, a manager reads but does not write: PUT is not
	// on the write allow-list, as DELETE and POST on a subscription are not.
	bHref := "/edev/" + subDevBID + "/sub/sub-b"
	if err := f.stores.Subscriptions.Create(ctx, "sub-b", sep2.Subscription{
		SubscribableResource: sep2.SubscribableResource{Resource: sep2.Resource{Href: bHref}},
		SubscribedResource:   "/edev/" + subDevBID,
		NotificationURI:      "https://subscriber.test/notify",
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if status, raw := subInstanceRequest(t, f.m, http.MethodGet, bHref, ""); status != http.StatusOK {
		t.Errorf("manager GET of managed device's subscription: status %d, want 200; body=%q", status, raw)
	}
	if status, _ := subInstanceRequest(t, f.m, http.MethodPut, bHref, subInstanceBody("/edev/"+subDevBID, "https://subscriber.test/x")); status != http.StatusForbidden {
		t.Errorf("manager PUT of managed device's subscription: status %d, want 403", status)
	}

	kept, err := f.stores.Subscriptions.Store.Get(ctx, "sub-a")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if kept.SubscribedResource != "/edev/"+subDevAID || kept.NotificationURI != "https://subscriber.test/notify" {
		t.Errorf("a refused PUT changed A's subscription to %+v", kept)
	}
}
