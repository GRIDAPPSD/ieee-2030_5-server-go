// CSIP V1.2 §9.3 — UTIL-003 Utility-Aggregator Group Assignment Retrieval.
//
// UTIL-003 proves the server accepts and persists Subscriptions an
// Aggregator opens against each managed inverter's DERProgramList. The
// procedure reads:
//
//  1. Aggregator GETs /edev (already exercised by UTIL-001/002 — UTIL-003
//     re-walks to discover the per-inverter DERProgramList href via
//     FSA chain).
//  2. For each managed inverter (EDA1..EDB2):
//     - Walk /edev/{id}/fsa → DERProgramListLink for the SY FSA (the
//     top-level chain entry; the server scopes DERPrograms by
//     EndDevice so any FSA's link works — see CORE-010).
//     - POST a Subscription targeting that DERProgramList URL.
//  3. Server returns 201 Created + Location header for each.
//  4. GET /edev/{id}/sub returns the just-created Subscription in the
//     list under that EndDevice scope.
//
// What the test pins down:
//   - The /edev/{id}/sub route accepts subscriptions for the aggregator
//     across 4 distinct managed inverters in parallel (the spec allows
//     concurrent subscriptions; we run the 4 inverter subtests with
//     t.Parallel() to surface any race in the SubscriptionStore).
//   - The server preserves the subscribedResource URI on the wire.
//
// IEEE-013 (subscription deliver context) is the hard prerequisite for
// the *delivery* side; UTIL-003 itself does not assert delivery, only
// that subscriptions are accepted. UTIL-004 exercises the deliver-then-
// notify path end-to-end.
//
// Out of scope:
//   - Verifying Notification fan-out on resource mutation (UTIL-004).
//   - Aggregator-side subscription bookkeeping (client-side concern).
package csip_test

import (
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
)

// utilSubscriptionNotificationURI is the aggregator-supplied callback
// URL. UTIL-003 does not exercise delivery; the URI is recorded as
// part of Subscription state and asserted on the GET round-trip.
const utilSubscriptionNotificationURI = "https://aggregator.example/notify"

// TestUTIL_003_GroupAssignmentRetrieval implements CSIP V1.2 §9.3.
func TestUTIL_003_GroupAssignmentRetrieval(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	srv := bootAggregatorTopology(t)
	client := srv.Client()
	rawClient := srv.HTTPClient()

	// Step 1: collect per-inverter DERProgramList Hrefs.
	progHrefs := map[string]string{}
	for _, edevID := range aggManagedInverters {
		fsaHref := fmt.Sprintf("/edev/%s/fsa?l=255", edevID)
		var fsaList sep2.FunctionSetAssignmentsList
		if err := client.WalkLink(ctx, sep2.Link{Href: fsaHref}, &fsaList); err != nil {
			t.Fatalf("step 1: WalkLink %s: %v", fsaHref, err)
		}
		if len(fsaList.FunctionSetAssignments) == 0 {
			t.Fatalf("step 1: edev=%q has empty FSAList", edevID)
		}
		// SY-level FSA is at index 0 (store-key order).
		link := fsaList.FunctionSetAssignments[0].DERProgramListLink
		if link == nil || link.Href == "" {
			t.Fatalf("step 1: edev=%q SY-FSA missing DERProgramListLink", edevID)
		}
		progHrefs[edevID] = link.Href
	}

	// Step 2 + 3: POST a Subscription per inverter in parallel — surfaces
	// SubscriptionStore races under -race.
	for _, edevID := range aggManagedInverters {
		edevID := edevID
		subscribedResource := progHrefs[edevID]
		t.Run("subscribe_edev_"+edevID, func(t *testing.T) {
			t.Parallel()
			postAndVerifySubscription(t, ctx, rawClient, srv.BaseURL, edevID, subscribedResource)
		})
	}
}

// postAndVerifySubscription POSTs one subscription against /edev/{id}/sub
// with SubscribedResource = resource, asserts 201 + non-empty Location,
// then GETs /edev/{id}/sub and asserts the new subscription is present
// in the list under the expected scope. Surfaces three failure modes
// distinctly:
//
//   - Server refused the POST (mode → status != 201).
//   - Server accepted but did not persist (mode → GET list excludes it).
//   - Server persisted under the wrong scope (mode → GET on a different
//     edev contains it; not asserted explicitly because the per-edev
//     subtests run in parallel and each only walks its own list, which
//     is a per-scope assertion by construction).
func postAndVerifySubscription(t *testing.T, ctx context.Context, client *http.Client, baseURL, edevID, resource string) {
	t.Helper()

	sub := sep2.Subscription{
		SubscribedResource: resource,
		NotificationURI:    utilSubscriptionNotificationURI,
		Encoding:           0, // 0 = XML per sep2 SubscriptionEncodingType
		Limit:              1,
	}
	body, err := xml.Marshal(&sub)
	if err != nil {
		t.Fatalf("marshal Subscription: %v", err)
	}

	postURL := fmt.Sprintf("%s/edev/%s/sub", baseURL, edevID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, postURL, bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST %s: build request: %v", postURL, err)
	}
	req.Header.Set("Content-Type", "application/sep+xml")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("POST %s: %v", postURL, err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST %s: status = %d, want 201", postURL, resp.StatusCode)
	}
	loc := resp.Header.Get("Location")
	if loc == "" {
		t.Fatalf("POST %s: empty Location header", postURL)
	}

	// GET /edev/{id}/sub and assert the new sub is present with the
	// supplied subscribed-resource URI. We GET the list (not the
	// Location) because (a) the list is the procedure's verification
	// surface and (b) it pins down list-handler scoping in one go.
	listURL := fmt.Sprintf("%s/edev/%s/sub?l=255", baseURL, edevID)
	getReq, err := http.NewRequestWithContext(ctx, http.MethodGet, listURL, nil)
	if err != nil {
		t.Fatalf("GET %s: build request: %v", listURL, err)
	}
	getResp, err := client.Do(getReq)
	if err != nil {
		t.Fatalf("GET %s: %v", listURL, err)
	}
	defer func() { _ = getResp.Body.Close() }()
	if getResp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s: status = %d, want 200", listURL, getResp.StatusCode)
	}
	raw, err := io.ReadAll(getResp.Body)
	if err != nil {
		t.Fatalf("GET %s: read body: %v", listURL, err)
	}
	var list sep2.SubscriptionList
	if err := xml.Unmarshal(raw, &list); err != nil {
		t.Fatalf("GET %s: unmarshal SubscriptionList: %v", listURL, err)
	}
	found := false
	for _, s := range list.Subscription {
		if s.SubscribedResource == resource && s.NotificationURI == utilSubscriptionNotificationURI {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("GET %s: subscription with subscribedResource=%q not present in list (got %d entries)",
			listURL, resource, len(list.Subscription))
	}
}
