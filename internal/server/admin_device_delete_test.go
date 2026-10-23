package server_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/adminplane"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/auth"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/server"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/assembly"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
)

func adminDeleteRequest(router http.Handler, id string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodDelete, "/api/devices/"+id, nil)
	req.Header.Set("Authorization", "Bearer the-key")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

// #1120: DELETE /api/devices/{id} removes what the protocol DELETE removes,
// notifies the EndDeviceList, and answers 404 for a device that is not there.
func TestAdminDeleteEndDevice_RunsTheProtocolCleanup(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	stores := newTestStores()
	notifier := &templateNotifier{}
	stores.EndDeviceDelete = assembly.EndDeviceDeleteHandler(server.NewCoreStores(stores), notifier)
	router, _ := adminplane.BuildAdminRouter("the-key", nil, stores, "GCM",
		auth.NewTicketStore(time.Minute), nil, nil, nil)

	for id, lfdi := range map[string]string{
		"7": "0000000000000000000000000000000000000007",
		"3": "0000000000000000000000000000000000000003",
	} {
		if err := stores.EndDevices.Create(ctx, id, sep2.EndDevice{LFDI: lfdi}); err != nil {
			t.Fatalf("seed EndDevice %q: %v", id, err)
		}
	}
	if err := stores.DERs.Create(ctx, "7", "d1", sep2.DER{}); err != nil {
		t.Fatalf("seed DER: %v", err)
	}
	plant := func(id, href, resource string) {
		t.Helper()
		sub := sep2.Subscription{SubscribedResource: resource, NotificationURI: "https://127.0.0.1:1/n"}
		sub.Href = href
		if err := stores.Subscriptions.Create(ctx, id, sub); err != nil {
			t.Fatalf("plant subscription %q: %v", id, err)
		}
	}
	plant("own", "/edev/7/sub/own", "/edev/7/der")
	plant("cross", "/edev/3/sub/cross", "/edev/7/der")
	plant("bystander", "/edev/3/sub/bystander", "/edev/3/der")

	if rec := adminDeleteRequest(router, "7"); rec.Code != http.StatusNoContent {
		t.Fatalf("DELETE /api/devices/7 = %d, want 204: %s", rec.Code, rec.Body.String())
	}

	if _, err := stores.EndDevices.Get(ctx, "7"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("EndDevice 7 after delete: err = %v, want ErrNotFound", err)
	}
	if _, err := stores.DERs.Get(ctx, "7", "d1"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("DER d1 under 7 after delete: err = %v, want ErrNotFound (the cascade)", err)
	}
	if _, err := stores.EndDevices.Get(ctx, "3"); err != nil {
		t.Errorf("EndDevice 3 after deleting 7: %v", err)
	}
	for id, want := range map[string]bool{"own": false, "cross": false, "bystander": true} {
		_, err := stores.Subscriptions.Get(ctx, id)
		if present := err == nil; present != want {
			t.Errorf("subscription %q present = %t after deleting 7, want %t (the post-delete sweep)", id, present, want)
		}
	}
	if got, want := notifier.take(), []string{"/edev 0"}; !slices.Equal(got, want) {
		t.Errorf("notifications = %q, want %q", got, want)
	}

	if rec := adminDeleteRequest(router, "7"); rec.Code != http.StatusNotFound {
		t.Errorf("second DELETE /api/devices/7 = %d, want 404", rec.Code)
	}
	if got := notifier.take(); len(got) != 0 {
		t.Errorf("a delete of an absent device notified: %q", got)
	}
}
