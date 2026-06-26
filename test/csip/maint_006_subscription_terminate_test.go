//go:build csip_test_hooks

// CSIP V1.2 §11.6 — Subscription Terminate (Server Refuses Retry).
//
// MAINT-006 asserts that after the server cancels a subscription (via
// the IEEE-079 /test/mutations/subscription-cancel mutation), a
// subsequent client POST /edev/{id}/sub that resolves to the same
// subscription ID is refused with HTTP 409 Conflict.
//
// Scope boundary: the V1.2 spec also calls out a client-side polling
// fallback when subscriptions are refused. That behavior is plan-1's
// problem (client-side conformance). MAINT-006 here asserts only the
// server-side refusal semantics. See IEEE-091 ticket "Scope boundary".
//
// V1.2 procedure step → assertion mapping:
//
//	Step 1: client POSTs a Subscription with X-CSIP-Test-Subscription-ID
//	        header to pin a deterministic ID (the IEEE-079 hook). 201
//	        Created; sub lives in the store.
//	                                          ──► POST /edev/{id}/sub + hdr
//	Step 2: server cancels via the mutation hook.
//	                                          ──► POST /test/mutations/subscription-cancel
//	                                          ──► 204 No Content
//	                                          ──► store no longer has the sub
//	                                          ──► tombstone set contains the id
//	Step 3: client retries the same Subscription with the same pinned
//	        ID. Server refuses with 409 Conflict.
//	                                          ──► POST /edev/{id}/sub + hdr (same id)
//	                                          ──► 409 Conflict
//
// Pike-back from IEEE-087 flagged that HandleDeleteSubscription doesn't
// POST a final Removed-Notification. That gap is unrelated to MAINT-006
// (the spec's care-about is refusal of retry, not a removal notice);
// filed as a follow-up rather than fixing in scope. See journal entry.
//
// Build-tag: csip_test_hooks for the subscription-cancel mutation AND
// the X-CSIP-Test-Subscription-ID + tombstone hooks in coresub.
//
// IEEE-091 / Phase 6.

package csip_test

import (
	"bytes"
	"context"
	"encoding/xml"
	"net/http"
	"strings"
	"testing"

	"gitlab.pnnl.gov/arista/ieee-2030_5/ieee-2030_5-core/pkg/sep2"
	coresub "gitlab.pnnl.gov/arista/ieee-2030_5/ieee-2030_5-core/pkg/sep2srv/handlers/subscription"
	"gitlab.pnnl.gov/arista/ieee-2030_5/ieee-2030_5-core/pkg/store"
	"github.com/GRIDAPPSD/ieee-2030_5-go/test/csip/csiptest"
)

const (
	maint006EndDeviceID        = "edev-maint006"
	maint006SubscriptionID     = "sub-maint006-pinned"
	maint006TestSubIDHeader    = "X-CSIP-Test-Subscription-ID"
	maint006SubscribedResource = "/edev/edev-maint006/fsa"
)

// TestMAINT_006_SubscriptionTerminate implements CSIP V1.2 §11.6.
func TestMAINT_006_SubscriptionTerminate(t *testing.T) {
	// Note: cannot t.Parallel — this test mutates the process-global
	// coresub canceled set via MarkSubscriptionCanceled.
	// Running in parallel with another tagged test that pins a colliding
	// subscription ID would cross-contaminate the set. We compensate
	// with ResetCanceledSubscriptions at teardown so subsequent
	// serial tests start with a fresh tombstone set.
	t.Cleanup(coresub.ResetCanceledSubscriptions)

	srv := csiptest.BootServer(t)

	// Step 1: POST /edev/{id}/sub with pinned ID header. The
	// production handler honors the X-CSIP-Test-Subscription-ID
	// header only when the csip_test_hooks build tag is set; the
	// override is wired in core's pkg/sep2srv/handlers/subscription/subscription_test_hook.go.
	sub := sep2.Subscription{
		SubscribedResource: maint006SubscribedResource,
		NotificationURI:    "http://example.test/notify",
		Encoding:           sep2.EncodingXML,
	}
	location := postSubscriptionWithPinnedID(t, srv, maint006EndDeviceID, &sub, maint006SubscriptionID, http.StatusCreated)
	if !strings.HasSuffix(location, "/"+maint006SubscriptionID) {
		t.Fatalf("MAINT-006 Step 1: Location = %q, want suffix /%s", location, maint006SubscriptionID)
	}

	// Sanity: store has the sub at the pinned id.
	ctx := context.Background()
	if _, err := srv.Stores.Subscriptions.Get(ctx, maint006SubscriptionID); err != nil {
		t.Fatalf("MAINT-006 Step 1: store missing pinned sub: %v", err)
	}

	// Step 2: server cancels via mutation hook.
	status, body := postMutationJSON(t, srv, mutateSubscriptionCancel, map[string]string{
		"subscription_id": maint006SubscriptionID,
	})
	if status != http.StatusNoContent {
		t.Fatalf("MAINT-006 Step 2: status = %d, want 204: %s", status, string(body))
	}

	// Sub is gone from the store.
	if _, err := srv.Stores.Subscriptions.Get(ctx, maint006SubscriptionID); err == nil {
		t.Errorf("MAINT-006 Step 2: sub still present after cancel")
	} else if err != store.ErrNotFound { //nolint:errorlint // sentinel: pkg/store returns the bare sentinel, not a wrapping
		t.Errorf("MAINT-006 Step 2: unexpected error post-cancel: %v", err)
	}

	// Tombstone set contains the id (visible via the test-only helper).
	if !coresub.IsSubscriptionCanceled(maint006SubscriptionID) {
		t.Errorf("MAINT-006 Step 2: tombstone set does not contain %q", maint006SubscriptionID)
	}

	// Step 3: re-POST same Subscription with same pinned id → 409.
	_ = postSubscriptionWithPinnedID(t, srv, maint006EndDeviceID, &sub, maint006SubscriptionID, http.StatusConflict)
}

// postSubscriptionWithPinnedID POSTs a Subscription with the
// X-CSIP-Test-Subscription-ID header so the build-tagged handler pins
// the subscription's id to the given value. Asserts the expected HTTP
// status and, on 201, returns the Location header.
//
// Status assertion is parameterized so the same helper drives both the
// initial create (201) and the post-cancel refusal (409). Misuse with
// any other expected status is still safe — t.Fatal-shaped failure.
func postSubscriptionWithPinnedID(t *testing.T, srv *csiptest.BootedServer, edevID string, sub *sep2.Subscription, pinnedID string, wantStatus int) string {
	t.Helper()
	body, err := xml.Marshal(sub)
	if err != nil {
		t.Fatalf("marshal Subscription: %v", err)
	}
	url := srv.BaseURL + "/edev/" + edevID + "/sub"
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		t.Fatalf("build POST: %v", err)
	}
	req.Header.Set("Content-Type", "application/sep+xml")
	req.Header.Set(maint006TestSubIDHeader, pinnedID)
	resp, err := srv.Client().HTTPClient().Do(req)
	if err != nil {
		t.Fatalf("POST %s: %v", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != wantStatus {
		t.Fatalf("POST %s with pinned id %q: status = %d, want %d",
			url, pinnedID, resp.StatusCode, wantStatus)
	}
	return resp.Header.Get("Location")
}
