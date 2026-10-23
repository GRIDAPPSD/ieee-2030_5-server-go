// CSIP V1.2 section 11.1 - Inverter Maintenance (Out-Of-Band), driven through
// the admin plane's DELETE /api/devices/{id} (#1120) and, with the
// csip_test_hooks tag, through the test mutation hook
// (maint_001_inverter_oob_test.go). Both variants share runMAINT001's Steps 1
// and 3.
//
//	Step 1: pre-seed an EndDevice and a Subscription on the EndDeviceList.
//	Step 2: the variant deletes the device out of band.
//	Step 3: the subscriber is notified, and GET on the deleted href is 404.

package csip_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/adminplane"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/auth"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/server"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/assembly"
	coresub "github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/handlers/subscription"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/test/csip/csiptest"
)

const (
	maint001EndDeviceID = "edev-maint001"
	maint001AdminKey    = "maint-001-admin-key"
)

// maint001Delete is a variant's Step 2. It must leave the delete done and the
// EndDeviceList Notification queued on mgr.
type maint001Delete func(ctx context.Context, t *testing.T, srv *csiptest.BootedServer, mgr *coresub.Manager)

func runMAINT001(t *testing.T, del maint001Delete) {
	t.Helper()
	owner := csiptest.NewDeviceIdentity(t, "MAINT-001-DEVICE")
	srv := csiptest.BootServer(t, csiptest.WithDeviceIdentity(owner))
	receiver := csiptest.NewNotificationReceiver(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	mgr := coresub.NewManager(srv.Stores.Subscriptions, 2, 16, csiptest.AllowLoopbackReceivers())
	go mgr.Start(ctx)

	// Step 1a: seed the EndDevice Step 2 deletes.
	seedOwnedEndDevice(t, srv.Stores.EndDevices, maint001EndDeviceID, owner)

	// Step 1b: aggregator subscribes to /edev (EndDeviceList).
	sub := sep2.Subscription{
		SubscribedResource: "/edev",
		NotificationURI:    receiver.URL(),
		Encoding:           sep2.EncodingXML,
	}
	if location := postSubscriptionExpect201(t, srv, maint001EndDeviceID, &sub); location == "" {
		t.Fatalf("MAINT-001 Step 1b: empty Location header")
	}

	// Sanity: without this sub in ListByResource("/edev"), Step 3's
	// Notify would fan out to zero receivers.
	listed, err := srv.Stores.Subscriptions.ListByResource(ctx, "/edev")
	if err != nil {
		t.Fatalf("MAINT-001 Step 1b: ListByResource: %v", err)
	}
	if len(listed) != 1 {
		t.Fatalf("MAINT-001 Step 1b: ListByResource(/edev) returned %d, want 1", len(listed))
	}

	del(ctx, t, srv, mgr)

	if _, err := srv.Stores.EndDevices.Get(context.Background(), maint001EndDeviceID); err == nil {
		t.Errorf("MAINT-001 Step 2: EndDevice %q still present after OOB delete", maint001EndDeviceID)
	}

	// Step 3a: the EndDeviceList subscriber is notified.
	got, ok := receiver.Wait(1, 2*time.Second)
	if !ok {
		t.Fatalf("MAINT-001 Step 3a: timed out waiting for Notification; received %d, want >=1", len(got))
	}
	rec := got[0]
	if rec.Notification == nil {
		t.Fatalf("MAINT-001 Step 3a: Notification body did not parse: %q", string(rec.Body))
	}
	if rec.Notification.SubscribedResource != "/edev" {
		t.Errorf("MAINT-001 Step 3a: SubscribedResource = %q, want %q",
			rec.Notification.SubscribedResource, "/edev")
	}
	if rec.Notification.Status != 0 {
		t.Errorf("MAINT-001 Step 3a: Status = %d, want 0 (Default Status: the list changed)",
			rec.Notification.Status)
	}

	// Step 3b: aggregator GETs the deleted href; expect 404 and no
	// Location echo of the deleted id.
	deletedURL := srv.BaseURL + "/edev/" + maint001EndDeviceID
	getResp := doSubRequest(t, srv, http.MethodGet, deletedURL, "", nil)
	defer getResp.Body.Close()
	if getResp.StatusCode != http.StatusNotFound {
		t.Errorf("MAINT-001 Step 3b: GET %s status = %d, want 404", deletedURL, getResp.StatusCode)
	}
	if loc := getResp.Header.Get("Location"); loc != "" {
		t.Errorf("MAINT-001 Step 3b: GET-after-delete returned Location=%q", loc)
	}
}

// adminDeleteRouter mounts the admin plane over srv's stores with the delete
// route notifying through mgr, the wiring server.Run does over its own
// notifier.
func adminDeleteRouter(srv *csiptest.BootedServer, mgr *coresub.Manager) http.Handler {
	srv.Stores.EndDeviceDelete = assembly.EndDeviceDeleteHandler(server.NewCoreStores(srv.Stores), adminplane.AdaptNotifier(mgr))
	router, _ := adminplane.BuildAdminRouter(maint001AdminKey, nil, srv.Stores, "GCM",
		auth.NewTicketStore(time.Minute), nil, nil, nil)
	return router
}

// TestMAINT_001_InverterMaintenanceAdminRoute is MAINT-001 with Step 2 as an
// authenticated DELETE /api/devices/{id} on the admin plane.
func TestMAINT_001_InverterMaintenanceAdminRoute(t *testing.T) {
	t.Parallel()
	runMAINT001(t, func(ctx context.Context, t *testing.T, srv *csiptest.BootedServer, mgr *coresub.Manager) {
		req := httptest.NewRequest(http.MethodDelete, "/api/devices/"+maint001EndDeviceID, nil)
		req.Header.Set("Authorization", "Bearer "+maint001AdminKey)
		rec := httptest.NewRecorder()
		adminDeleteRouter(srv, mgr).ServeHTTP(rec, req)
		if rec.Code != http.StatusNoContent {
			t.Fatalf("MAINT-001 Step 2: admin DELETE status = %d, want 204: %s", rec.Code, rec.Body.String())
		}
	})
}

// TestMAINT_001_AdminDeleteRefusesWithoutCredential: a request with no real
// credential, from a remote address and from loopback (where the bypass
// admits other routes), or with a wrong Bearer, deletes nothing.
func TestMAINT_001_AdminDeleteRefusesWithoutCredential(t *testing.T) {
	t.Parallel()
	owner := csiptest.NewDeviceIdentity(t, "MAINT-001-REFUSE")
	srv := csiptest.BootServer(t, csiptest.WithDeviceIdentity(owner))
	router := adminDeleteRouter(srv, nil)

	cases := []struct {
		id, name, remote, bearer string
	}{
		{"edev-refuse-remote", "remote, no credential", "192.0.2.1:4000", ""},
		{"edev-refuse-loopback", "loopback bypass only", "127.0.0.1:4000", ""},
		{"edev-refuse-bearer", "wrong bearer", "192.0.2.1:4000", "Bearer not-the-key"},
	}
	for _, tc := range cases {
		seedOwnedEndDevice(t, srv.Stores.EndDevices, tc.id, owner)
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodDelete, "/api/devices/"+tc.id, nil)
			req.RemoteAddr = tc.remote
			if tc.bearer != "" {
				req.Header.Set("Authorization", tc.bearer)
			}
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)
			if rec.Code != http.StatusUnauthorized {
				t.Errorf("status = %d, want 401: %s", rec.Code, rec.Body.String())
			}
			if _, err := srv.Stores.EndDevices.Get(context.Background(), tc.id); err != nil {
				t.Errorf("EndDevice %q gone after a refused request: %v", tc.id, err)
			}
		})
	}
}
