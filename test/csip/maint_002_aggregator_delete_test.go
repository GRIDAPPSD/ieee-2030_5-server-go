package csip_test

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2tls"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/test/csip/csiptest"
)

const (
	maint002AggID     = "edev-maint002-agg"
	maint002ManagedID = "edev-maint002-managed"
	maint002OtherID   = "edev-maint002-other"

	maint002ManagedLFDI = "D200000000000000000000000000000000000002"
	maint002OtherLFDI   = "D300000000000000000000000000000000000003"
)

// TestMAINT_002_AggregatorDeletesAManagedDevice drives CSIP V1.2 section 11.2
// as the aggregator: the DELETE names a device the utility paired to it, not
// the aggregator's own record (IG 5.3.1.2).
func TestMAINT_002_AggregatorDeletesAManagedDevice(t *testing.T) {
	t.Parallel()

	agg := csiptest.NewDeviceIdentity(t, "MAINT-002-AGGREGATOR")
	stores := csiptest.NewFreshStores()
	srv := csiptest.BootServer(t, csiptest.WithStores(stores), csiptest.WithDeviceIdentity(agg))
	receiver := csiptest.NewNotificationReceiver(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	seed := func(id, lfdi, sfdi string) {
		t.Helper()
		dev := sep2.EndDevice{}
		dev.Href = "/edev/" + id
		dev.LFDI, dev.SFDI = lfdi, sfdi
		if err := srv.Stores.EndDevices.Create(ctx, id, dev); err != nil {
			t.Fatalf("seed EndDevice %s: %v", id, err)
		}
	}
	managedSFDI, err := sep2tls.SFDIFromLFDI(maint002ManagedLFDI)
	if err != nil {
		t.Fatalf("derive the managed SFDI: %v", err)
	}
	seed(maint002AggID, agg.LFDI, agg.SFDI)
	seed(maint002ManagedID, maint002ManagedLFDI, managedSFDI)
	seed(maint002OtherID, maint002OtherLFDI, "")
	if err := srv.Stores.EndDeviceManagers.Assign(ctx, agg.LFDI, maint002ManagedLFDI); err != nil {
		t.Fatalf("pair the managed device to the aggregator: %v", err)
	}

	sub := sep2.Subscription{
		SubscribedResource: "/edev",
		NotificationURI:    receiver.URL(),
		Encoding:           sep2.EncodingXML,
	}
	if loc := postSubscriptionExpect201(t, srv, maint002AggID, &sub); loc == "" {
		t.Fatalf("MAINT-002 Step 1: empty Location header")
	}

	// A device the aggregator does not manage is refused and left alone.
	otherURL := srv.BaseURL + "/edev/" + maint002OtherID
	resp := doSubRequest(t, srv, http.MethodDelete, otherURL, "", nil)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("MAINT-002 Step 2: DELETE of an unmanaged device status = %d, want 403", resp.StatusCode)
	}
	if _, err := srv.Stores.EndDevices.Get(ctx, maint002OtherID); err != nil {
		t.Errorf("MAINT-002 Step 2: the unmanaged record is gone after a refused DELETE: %v", err)
	}

	// Step 3: the managed device is deleted.
	managedURL := srv.BaseURL + "/edev/" + maint002ManagedID
	resp = doSubRequest(t, srv, http.MethodDelete, managedURL, "", nil)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("MAINT-002 Step 3: DELETE %s status = %d, want 204", managedURL, resp.StatusCode)
	}

	// Step 4: /edev subscribers are notified that the list changed.
	got, ok := receiver.Wait(1, 2*time.Second)
	if !ok {
		t.Fatalf("MAINT-002 Step 4: timed out waiting for Notification; received %d, want >=1", len(got))
	}
	if n := got[0].Notification; n == nil || n.SubscribedResource != "/edev" || n.Status != 0 {
		t.Errorf("MAINT-002 Step 4: Notification = %+v, want SubscribedResource /edev with Default status", n)
	}

	// Step 5: a later GET answers 404, and the pair went with the record.
	getResp := doSubRequest(t, srv, http.MethodGet, managedURL, "", nil)
	_ = getResp.Body.Close()
	if getResp.StatusCode != http.StatusNotFound {
		t.Errorf("MAINT-002 Step 5: GET-after-delete status = %d, want 404", getResp.StatusCode)
	}
	if _, err := srv.Stores.EndDeviceManagers.ManagerOf(ctx, maint002ManagedLFDI); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("MAINT-002 Step 5: ManagerOf(deleted device) = %v, want ErrNotFound", err)
	}
	if _, err := srv.Stores.EndDevices.Get(ctx, maint002AggID); err != nil {
		t.Errorf("MAINT-002 Step 5: the aggregator's own record is gone: %v", err)
	}
}
