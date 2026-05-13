package handler_test

import (
	"context"
	"encoding/xml"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-go/internal/handler"
	"github.com/GRIDAPPSD/ieee-2030_5-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-go/pkg/store/memory"
)

// IEEE-099: GET /edev/{id}/sub must return only subscriptions scoped to
// EndDevice {id}. Before this ticket landed, the handler was wired to
// the underlying union Store and returned the union across all
// EndDevices.

func seedSub(t *testing.T, store *memory.SubscriptionStore, id, edevID, resource string) {
	t.Helper()
	sub := sep2.Subscription{
		SubscribableResource: sep2.SubscribableResource{
			Resource: sep2.Resource{Href: "/edev/" + edevID + "/sub/" + id},
		},
		SubscribedResource: resource,
		NotificationURI:    "https://example.test/notify/" + id,
	}
	if err := store.Create(context.Background(), id, sub); err != nil {
		t.Fatalf("seed %q: %v", id, err)
	}
}

func TestHandleListSubscriptionsByDevice_ScopesByEndDevice(t *testing.T) {
	t.Parallel()
	store := memory.NewSubscriptionStore()
	seedSub(t, store, "a1", "1", "/edev/1")
	seedSub(t, store, "a2", "1", "/edev/1/fsa")
	seedSub(t, store, "b1", "2", "/edev/2")
	seedSub(t, store, "b2", "2", "/edev/2/fsa")

	mux := http.NewServeMux()
	mux.HandleFunc("GET /edev/{id}/sub", handler.HandleListSubscriptionsByDevice(store, 900))

	got1 := decodeSubList(t, mux, "/edev/1/sub?l=255")
	if got1.All != 2 {
		t.Errorf("GET /edev/1/sub All = %d, want 2", got1.All)
	}
	if got1.Results != 2 {
		t.Errorf("GET /edev/1/sub Results = %d, want 2", got1.Results)
	}
	gotHrefs1 := subHrefs(got1)
	if gotHrefs1[0] != "/edev/1/sub/a1" || gotHrefs1[1] != "/edev/1/sub/a2" {
		t.Errorf("GET /edev/1/sub hrefs = %v, want [/edev/1/sub/a1 /edev/1/sub/a2]", gotHrefs1)
	}

	got2 := decodeSubList(t, mux, "/edev/2/sub?l=255")
	if got2.All != 2 {
		t.Errorf("GET /edev/2/sub All = %d, want 2", got2.All)
	}
	gotHrefs2 := subHrefs(got2)
	if gotHrefs2[0] != "/edev/2/sub/b1" || gotHrefs2[1] != "/edev/2/sub/b2" {
		t.Errorf("GET /edev/2/sub hrefs = %v, want [/edev/2/sub/b1 /edev/2/sub/b2]", gotHrefs2)
	}
}

func TestHandleListSubscriptionsByDevice_UnknownEdevReturnsEmpty(t *testing.T) {
	t.Parallel()
	store := memory.NewSubscriptionStore()
	seedSub(t, store, "a1", "1", "/edev/1")

	mux := http.NewServeMux()
	mux.HandleFunc("GET /edev/{id}/sub", handler.HandleListSubscriptionsByDevice(store, 900))

	got := decodeSubList(t, mux, "/edev/ghost/sub?l=255")
	if got.All != 0 {
		t.Errorf("All = %d, want 0", got.All)
	}
	if got.Results != 0 {
		t.Errorf("Results = %d, want 0", got.Results)
	}
	if len(got.Subscription) != 0 {
		t.Errorf("Subscription = %v, want empty", got.Subscription)
	}
}

// Paging via s/l/a query params still works on the scoped list.
func TestHandleListSubscriptionsByDevice_Paging(t *testing.T) {
	t.Parallel()
	store := memory.NewSubscriptionStore()
	for _, id := range []string{"a1", "a2", "a3"} {
		seedSub(t, store, id, "1", "/edev/1")
	}
	// Foreign edev — must not appear in /edev/1/sub regardless of paging.
	seedSub(t, store, "b1", "2", "/edev/2")

	mux := http.NewServeMux()
	mux.HandleFunc("GET /edev/{id}/sub", handler.HandleListSubscriptionsByDevice(store, 900))

	got := decodeSubList(t, mux, "/edev/1/sub?l=2")
	if got.All != 3 {
		t.Errorf("All = %d, want 3 (count is the per-edev total)", got.All)
	}
	if got.Results != 2 {
		t.Errorf("Results = %d, want 2 (l=2)", got.Results)
	}
	if len(got.Subscription) != 2 {
		t.Fatalf("len(Subscription) = %d, want 2", len(got.Subscription))
	}
	for _, s := range got.Subscription {
		if s.Href == "/edev/2/sub/b1" {
			t.Errorf("foreign edev subscription leaked: %+v", s)
		}
	}
}

// Per-edev count regression — pin the eventual AGG-001 assertion.
func TestHandleListSubscriptionsByDevice_NoCrossEdevLeak(t *testing.T) {
	t.Parallel()
	store := memory.NewSubscriptionStore()
	for _, edev := range []string{"EDA1", "EDA2", "EDB1", "EDB2"} {
		for i, r := range []string{"/edev", "/edev/" + edev, "/edev/" + edev + "/fsa"} {
			seedSub(t, store, "sub-"+edev+"-"+stringFromInt(i), edev, r)
		}
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /edev/{id}/sub", handler.HandleListSubscriptionsByDevice(store, 900))

	for _, edev := range []string{"EDA1", "EDA2", "EDB1", "EDB2"} {
		got := decodeSubList(t, mux, "/edev/"+edev+"/sub?l=255")
		if got.All != 3 {
			t.Errorf("edev=%q All = %d, want 3 (per-edev scope)", edev, got.All)
		}
		for _, s := range got.Subscription {
			wantPrefix := "/edev/" + edev + "/sub/"
			if got := s.Href; len(got) < len(wantPrefix) || got[:len(wantPrefix)] != wantPrefix {
				t.Errorf("edev=%q list contains foreign sub: %q", edev, got)
			}
		}
	}
}

func decodeSubList(t *testing.T, h http.Handler, path string) sep2.SubscriptionList {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("%s: status = %d, want 200", path, rec.Code)
	}
	body, err := io.ReadAll(rec.Body)
	if err != nil {
		t.Fatalf("%s: read body: %v", path, err)
	}
	var list sep2.SubscriptionList
	if err := xml.Unmarshal(body, &list); err != nil {
		t.Fatalf("%s: unmarshal SubscriptionList: %v\nbody=%s", path, err, body)
	}
	return list
}

func subHrefs(list sep2.SubscriptionList) []string {
	hrefs := make([]string, 0, len(list.Subscription))
	for _, s := range list.Subscription {
		hrefs = append(hrefs, s.Href)
	}
	sort.Strings(hrefs)
	return hrefs
}

// stringFromInt avoids strconv just for two-digit tags in subtest names.
func stringFromInt(i int) string {
	switch i {
	case 0:
		return "0"
	case 1:
		return "1"
	case 2:
		return "2"
	default:
		return "n"
	}
}
