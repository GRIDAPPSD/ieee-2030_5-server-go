package assembly_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/assembly"
)

func edevDelRequest(t *testing.T, srv *httptest.Server, method, path string) (int, string) {
	t.Helper()
	req, err := http.NewRequest(method, srv.URL+path, nil)
	if err != nil {
		t.Fatalf("new %s %s: %v", method, path, err)
	}
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read %s %s: %v", method, path, err)
	}
	return resp.StatusCode, string(b)
}

func seedDERsAndSubscription(t *testing.T, stores *assembly.Stores, edevID string) {
	t.Helper()
	ctx := context.Background()
	const key = "default"
	parent := edevID + "/1"
	if err := stores.DERs.Create(ctx, edevID, "1", derWithHref("/edev/"+edevID+"/der/1")); err != nil {
		t.Fatalf("seed DER: %v", err)
	}
	if err := stores.DERCapabilities.Create(ctx, parent, key, sep2.DERCapability{}); err != nil {
		t.Fatalf("seed dercap: %v", err)
	}
	if err := stores.DERSettings.Create(ctx, parent, key, sep2.DERSettings{}); err != nil {
		t.Fatalf("seed derg: %v", err)
	}
	if err := stores.DERStatuses.Create(ctx, parent, key, sep2.DERStatus{}); err != nil {
		t.Fatalf("seed ders: %v", err)
	}
	if err := stores.DERAvailabilities.Create(ctx, parent, key, sep2.DERAvailability{}); err != nil {
		t.Fatalf("seed dera: %v", err)
	}
	sub := sep2.Subscription{SubscribedResource: "/edev/" + edevID + "/der/1/ders"}
	sub.Href = "/edev/" + edevID + "/sub/s1"
	if err := stores.Subscriptions.Create(ctx, edevID+"-s1", sub); err != nil {
		t.Fatalf("seed subscription: %v", err)
	}
}

// TestEndDeviceDelete_RemovesDERsAndSubscriptionsBeforeTheIndexIsReused pins
// GRIDAPPSD/ieee-2030_5-server-go#1100: after DELETE and a re-registration at
// the same key, the DER list, the four DER singletons and the subscription
// list answer empty or 404. A second device whose id shares a string prefix
// ("e10" against "e1") keeps everything it had.
func TestEndDeviceDelete_RemovesDERsAndSubscriptionsBeforeTheIndexIsReused(t *testing.T) {
	t.Parallel()

	stores := testStores()
	seedOwnedDevices(t, stores.EndDevices, "e1", "e10")
	seedDERsAndSubscription(t, stores, "e1")
	seedDERsAndSubscription(t, stores, "e10")

	handler, _ := assembly.BuildProtocolRouter(assembly.RouterConfig{}, stores, testAuthPolicy(), testSFDI, testLFDI, nil)
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	ctx := context.Background()

	// Control: the records are served before the delete, so the empties
	// below mean the cascade ran.
	if code, body := edevDelRequest(t, srv, http.MethodGet, "/edev/e1/der"); code != http.StatusOK || !strings.Contains(body, "/edev/e1/der/1") {
		t.Fatalf("control: GET /edev/e1/der = %d %q, want 200 listing /edev/e1/der/1", code, body)
	}
	if recs, err := stores.Subscriptions.ListByDeviceWithIDs(ctx, "e1"); err != nil || len(recs) != 1 {
		t.Fatalf("control: e1 subscriptions = %d, %v, want 1", len(recs), err)
	}

	if code, _ := edevDelRequest(t, srv, http.MethodDelete, "/edev/e1"); code != http.StatusNoContent {
		t.Fatalf("DELETE /edev/e1 = %d, want 204", code)
	}
	seedOwnedDevices(t, stores.EndDevices, "e1")

	if n, err := stores.DERs.Count(ctx, "e1"); err != nil || n != 0 {
		t.Errorf("DERs under e1 = %d, %v, want 0", n, err)
	}
	if code, body := edevDelRequest(t, srv, http.MethodGet, "/edev/e1/der"); code != http.StatusOK || strings.Contains(body, "/der/1") {
		t.Errorf("GET /edev/e1/der after re-register = %d %q, want 200 with no DER", code, body)
	}
	// The singleton routes answer 200 with an empty default when nothing is
	// stored, so the stores are what shows the records are gone.
	for name, count := range map[string]func(parent string) (uint32, error){
		"dercap": func(p string) (uint32, error) { return stores.DERCapabilities.Count(ctx, p) },
		"derg":   func(p string) (uint32, error) { return stores.DERSettings.Count(ctx, p) },
		"ders":   func(p string) (uint32, error) { return stores.DERStatuses.Count(ctx, p) },
		"dera":   func(p string) (uint32, error) { return stores.DERAvailabilities.Count(ctx, p) },
	} {
		if n, err := count("e1/1"); err != nil || n != 0 {
			t.Errorf("%s under e1/1 after delete = %d, %v, want 0", name, n, err)
		}
		if n, err := count("e10/1"); err != nil || n != 1 {
			t.Errorf("%s under e10/1 = %d, %v, want 1", name, n, err)
		}
	}
	if recs, err := stores.Subscriptions.ListByDeviceWithIDs(ctx, "e1"); err != nil || len(recs) != 0 {
		t.Errorf("e1 subscriptions after delete = %d, %v, want 0", len(recs), err)
	}
	if recs, err := stores.Subscriptions.ListByResource(ctx, "/edev/e1/der/1/ders"); err != nil || len(recs) != 0 {
		t.Errorf("subscriptions on the dead resource = %d, %v, want 0", len(recs), err)
	}

	// The neighbour sharing the "e1" string prefix is untouched.
	if n, err := stores.DERs.Count(ctx, "e10"); err != nil || n != 1 {
		t.Errorf("DERs under e10 = %d, %v, want 1", n, err)
	}
	if recs, err := stores.Subscriptions.ListByDeviceWithIDs(ctx, "e10"); err != nil || len(recs) != 1 {
		t.Errorf("e10 subscriptions = %d, %v, want 1", len(recs), err)
	}
}

func derWithHref(href string) sep2.DER {
	var d sep2.DER
	d.Href = href
	return d
}
