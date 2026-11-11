package subscription_test

import (
	"context"
	"encoding/xml"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/handlers/subscription"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

// GRIDAPPSD/ieee-2030_5-server-go#1267: the Location POST /edev/{id}/sub
// returns answered 405 to GET, HEAD and PUT.

func allowAll(*http.Request, string) error { return nil }

func acceptURI(context.Context, string) error { return nil }

func instanceMux(s *memory.SubscriptionStore, canRead subscription.ReadCheck, validate func(context.Context, string) error) *http.ServeMux {
	mux := http.NewServeMux()
	h := subscription.HandleSubscription(s, validate, canRead)
	mux.HandleFunc("GET /edev/{id}/sub/{subId}", h)
	mux.HandleFunc("PUT /edev/{id}/sub/{subId}", h)
	return mux
}

func seedSubscription(t *testing.T, s *memory.SubscriptionStore, edev, id, resource, uri string) sep2.Subscription {
	t.Helper()
	sub := sep2.Subscription{
		SubscribableResource: sep2.SubscribableResource{
			Resource: sep2.Resource{Href: "/edev/" + edev + "/sub/" + id},
		},
		SubscribedResource: resource,
		NotificationURI:    uri,
	}
	if err := s.Create(context.Background(), id, sub); err != nil {
		t.Fatalf("seed %s: %v", id, err)
	}
	return sub
}

func subscriptionBody(href, resource, uri string) string {
	return `<Subscription xmlns="urn:ieee:std:2030.5:ns" href="` + href + `">` +
		`<subscribedResource>` + resource + `</subscribedResource>` +
		`<notificationURI>` + uri + `</notificationURI>` +
		`<limit>5</limit></Subscription>`
}

func do(mux http.Handler, method, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func TestHandleSubscription_OwnerGetServesTheStoredSubscription(t *testing.T) {
	t.Parallel()
	s := memory.NewSubscriptionStore()
	seedSubscription(t, s, "edev-A", "sub-1", "/edev/edev-A", "https://subscriber.test/notify")
	mux := instanceMux(s, allowAll, acceptURI)

	rec := do(mux, http.MethodGet, "/edev/edev-A/sub/sub-1", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var got sep2.Subscription
	if err := xml.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v; body=%s", err, rec.Body.String())
	}
	if got.Href != "/edev/edev-A/sub/sub-1" || got.SubscribedResource != "/edev/edev-A" || got.NotificationURI != "https://subscriber.test/notify" {
		t.Errorf("served %+v, want the stored href, subscribedResource and notificationURI", got)
	}
}

// HEAD is asserted over a real connection: net/http drops the body of a HEAD
// response there, while a recorder keeps whatever the handler wrote.
func TestHandleSubscription_HeadHasGetsHeadersAndNoBody(t *testing.T) {
	t.Parallel()
	s := memory.NewSubscriptionStore()
	seedSubscription(t, s, "edev-A", "sub-1", "/edev/edev-A", "https://subscriber.test/notify")
	srv := httptest.NewServer(instanceMux(s, allowAll, acceptURI))
	defer srv.Close()

	get, err := http.Get(srv.URL + "/edev/edev-A/sub/sub-1")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer func() { _ = get.Body.Close() }()
	head, err := http.Head(srv.URL + "/edev/edev-A/sub/sub-1")
	if err != nil {
		t.Fatalf("HEAD: %v", err)
	}
	defer func() { _ = head.Body.Close() }()

	if head.StatusCode != http.StatusOK {
		t.Fatalf("HEAD status = %d, want 200", head.StatusCode)
	}
	if head.Header.Get("Content-Type") != get.Header.Get("Content-Type") || head.Header.Get("Content-Type") == "" {
		t.Errorf("HEAD Content-Type = %q, GET Content-Type = %q; want equal and non-empty", head.Header.Get("Content-Type"), get.Header.Get("Content-Type"))
	}
	if head.ContentLength != get.ContentLength || head.ContentLength <= 0 {
		t.Errorf("HEAD Content-Length = %d, GET Content-Length = %d; want equal and positive", head.ContentLength, get.ContentLength)
	}
	buf := make([]byte, 1)
	if n, _ := head.Body.Read(buf); n != 0 {
		t.Errorf("HEAD carried a body")
	}
}

func TestHandleSubscription_PutReplacesOwnFieldsAndReadsBack(t *testing.T) {
	t.Parallel()
	s := memory.NewSubscriptionStore()
	seedSubscription(t, s, "edev-A", "sub-1", "/edev/edev-A", "https://subscriber.test/notify")
	mux := instanceMux(s, allowAll, acceptURI)

	// The document names another href; the stored one must win.
	rec := do(mux, http.MethodPut, "/edev/edev-A/sub/sub-1",
		subscriptionBody("/edev/edev-B/sub/sub-9", "/edev/edev-A/der", "https://subscriber.test/other"))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("PUT status = %d, want 204; body=%s", rec.Code, rec.Body.String())
	}

	var got sep2.Subscription
	read := do(mux, http.MethodGet, "/edev/edev-A/sub/sub-1", "")
	if err := xml.Unmarshal(read.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v; body=%s", err, read.Body.String())
	}
	if got.Href != "/edev/edev-A/sub/sub-1" {
		t.Errorf("href = %q after PUT, want the stored /edev/edev-A/sub/sub-1", got.Href)
	}
	if got.SubscribedResource != "/edev/edev-A/der" {
		t.Errorf("subscribedResource = %q, want /edev/edev-A/der", got.SubscribedResource)
	}
	if got.NotificationURI != "https://subscriber.test/other" {
		t.Errorf("notificationURI = %q, want https://subscriber.test/other", got.NotificationURI)
	}
	if got.Limit != 5 {
		t.Errorf("limit = %d, want 5", got.Limit)
	}

	// No subscription appeared under the href the document named.
	if _, err := s.Store.Get(context.Background(), "sub-9"); err == nil {
		t.Error("PUT stored a record under the id its document named")
	}
	// The device index still lists exactly the one subscription.
	if recs, _ := s.ListByDeviceWithIDs(context.Background(), "edev-A"); len(recs) != 1 || recs[0].ID != "sub-1" {
		t.Errorf("device edev-A lists %+v, want only sub-1", recs)
	}
	if recs, _ := s.ListByDeviceWithIDs(context.Background(), "edev-B"); len(recs) != 0 {
		t.Errorf("device edev-B lists %+v, want none", recs)
	}
}

// Notifications find a subscription through the resource index, so a PUT that
// moves subscribedResource must move the entry with it.
func TestHandleSubscription_PutMovesTheResourceIndex(t *testing.T) {
	t.Parallel()
	s := memory.NewSubscriptionStore()
	seedSubscription(t, s, "edev-A", "sub-1", "/edev/edev-A", "https://subscriber.test/notify")
	mux := instanceMux(s, allowAll, acceptURI)

	if rec := do(mux, http.MethodPut, "/edev/edev-A/sub/sub-1",
		subscriptionBody("", "/edev/edev-A/der", "https://subscriber.test/notify")); rec.Code != http.StatusNoContent {
		t.Fatalf("PUT status = %d, want 204; body=%s", rec.Code, rec.Body.String())
	}
	ctx := context.Background()
	if recs, _ := s.ListByResource(ctx, "/edev/edev-A"); len(recs) != 0 {
		t.Errorf("old resource still lists %+v, want none", recs)
	}
	if recs, _ := s.ListByResource(ctx, "/edev/edev-A/der"); len(recs) != 1 || recs[0].ID != "sub-1" {
		t.Errorf("new resource lists %+v, want sub-1", recs)
	}
}

func TestHandleSubscription_PutIsRefusedAsCreationWouldRefuse(t *testing.T) {
	t.Parallel()
	refuseResource := func(_ *http.Request, res string) error {
		if res == "/edev/edev-B" {
			return subscription.ErrSubscribedResourceRefused
		}
		return nil
	}
	refuseURI := func(_ context.Context, uri string) error {
		if strings.Contains(uri, "internal") {
			return subscription.ErrRefusedDestination
		}
		return nil
	}
	cases := []struct {
		name, resource, uri string
	}{
		{"unreadable subscribedResource", "/edev/edev-B", "https://subscriber.test/notify"},
		{"refused notificationURI", "/edev/edev-A", "https://internal.test/notify"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := memory.NewSubscriptionStore()
			orig := seedSubscription(t, s, "edev-A", "sub-1", "/edev/edev-A", "https://subscriber.test/notify")
			mux := instanceMux(s, refuseResource, refuseURI)

			rec := do(mux, http.MethodPut, "/edev/edev-A/sub/sub-1", subscriptionBody("", tc.resource, tc.uri))
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
			}
			kept, err := s.Store.Get(context.Background(), "sub-1")
			if err != nil {
				t.Fatalf("get: %v", err)
			}
			if kept.SubscribedResource != orig.SubscribedResource || kept.NotificationURI != orig.NotificationURI {
				t.Errorf("a refused PUT changed the stored subscription to %+v", kept)
			}
		})
	}
}

func TestHandleSubscription_PutWithNoReadCheckIsRefused(t *testing.T) {
	t.Parallel()
	s := memory.NewSubscriptionStore()
	seedSubscription(t, s, "edev-A", "sub-1", "/edev/edev-A", "https://subscriber.test/notify")
	mux := instanceMux(s, nil, acceptURI)

	rec := do(mux, http.MethodPut, "/edev/edev-A/sub/sub-1", subscriptionBody("", "/edev/edev-A", "https://subscriber.test/x"))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	kept, _ := s.Store.Get(context.Background(), "sub-1")
	if kept.NotificationURI != "https://subscriber.test/notify" {
		t.Errorf("notificationURI = %q after a refused PUT, want unchanged", kept.NotificationURI)
	}
}

func TestHandleSubscription_PutRejectsMalformedXML(t *testing.T) {
	t.Parallel()
	s := memory.NewSubscriptionStore()
	seedSubscription(t, s, "edev-A", "sub-1", "/edev/edev-A", "https://subscriber.test/notify")
	mux := instanceMux(s, allowAll, acceptURI)

	if rec := do(mux, http.MethodPut, "/edev/edev-A/sub/sub-1", "<Subscription"); rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

// Another device's subscription, and an absent one, answer the same 404 on
// every method, so a response cannot be used to probe subscription ids.
func TestHandleSubscription_CrossDeviceAndAbsentAreNotFound(t *testing.T) {
	t.Parallel()
	s := memory.NewSubscriptionStore()
	orig := seedSubscription(t, s, "edev-A", "sub-1", "/edev/edev-A", "https://subscriber.test/notify")
	mux := instanceMux(s, allowAll, acceptURI)

	for _, path := range []string{"/edev/edev-B/sub/sub-1", "/edev/edev-A/sub/absent"} {
		for _, method := range []string{http.MethodGet, http.MethodPut} {
			body := ""
			if method == http.MethodPut {
				body = subscriptionBody("", "/edev/edev-B", "https://subscriber.test/hijack")
			}
			if rec := do(mux, method, path, body); rec.Code != http.StatusNotFound {
				t.Errorf("%s %s status = %d, want 404; body=%s", method, path, rec.Code, rec.Body.String())
			}
		}
	}
	// HEAD takes the GET mount, so route it through the handler directly.
	h := subscription.HandleSubscription(s, acceptURI, allowAll)
	req := httptest.NewRequest(http.MethodHead, "/edev/edev-B/sub/sub-1", nil)
	req.SetPathValue("id", "edev-B")
	req.SetPathValue("subId", "sub-1")
	rec := httptest.NewRecorder()
	h(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("HEAD cross-device status = %d, want 404", rec.Code)
	}

	kept, err := s.Store.Get(context.Background(), "sub-1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if kept.Href != orig.Href || kept.NotificationURI != orig.NotificationURI || kept.SubscribedResource != orig.SubscribedResource {
		t.Errorf("a cross-device PUT changed the stored subscription to %+v", kept)
	}
}

func TestHandleSubscription_OtherMethodsAre405WithAllow(t *testing.T) {
	t.Parallel()
	s := memory.NewSubscriptionStore()
	h := subscription.HandleSubscription(s, acceptURI, allowAll)
	req := httptest.NewRequest(http.MethodPost, "/edev/edev-A/sub/sub-1", nil)
	rec := httptest.NewRecorder()
	h(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rec.Code)
	}
	if got := rec.Header().Get("Allow"); got != "GET, HEAD, PUT" {
		t.Errorf("Allow = %q, want %q", got, "GET, HEAD, PUT")
	}
}

func TestSubscriptionStore_UpdateOfAbsentIDIsNotFound(t *testing.T) {
	t.Parallel()
	s := memory.NewSubscriptionStore()
	err := s.Update(context.Background(), "nope", sep2.Subscription{})
	if err == nil || !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("err = %v, want store.ErrNotFound", err)
	}
}

// A PUT body past the read cap is cut short and refused; the stored record is
// untouched. The padding sits before the document's fields so that a larger
// cap would let the document parse and the PUT succeed.
func TestHandleSubscription_PutOverTheBodyCapIsRefused(t *testing.T) {
	t.Parallel()
	s := memory.NewSubscriptionStore()
	orig := seedSubscription(t, s, "edev-A", "sub-1", "/edev/edev-A", "https://subscriber.test/notify")
	mux := instanceMux(s, allowAll, acceptURI)

	body := `<Subscription xmlns="urn:ieee:std:2030.5:ns" href="/edev/edev-A/sub/sub-1">` +
		strings.Repeat(" ", 1<<20) +
		`<subscribedResource>/edev/edev-A</subscribedResource>` +
		`<notificationURI>https://changed.test/notify</notificationURI></Subscription>`
	rec := do(mux, http.MethodPut, "/edev/edev-A/sub/sub-1", body)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
	kept, err := s.Store.Get(context.Background(), "sub-1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if kept.NotificationURI != orig.NotificationURI {
		t.Errorf("an over-cap PUT changed notificationURI to %q", kept.NotificationURI)
	}
}

// Not parallel: it swaps the process-wide log output (captureLog).
func TestHandleCreateSubscription_NilCheckLogNamesPutToo(t *testing.T) {
	logs := captureLog(t)
	_ = subscription.HandleCreateSubscription(memory.NewSubscriptionStore(), acceptURI, nil)
	if !strings.Contains(logs.String(), "PUT /edev/{id}/sub/{subId}") {
		t.Errorf("startup log does not say PUT is refused too; got:\n%s", logs.String())
	}
}
