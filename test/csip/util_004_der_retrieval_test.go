//go:build csip_test_hooks

// CSIP V1.2 §9.4 — UTIL-004 Utility-Aggregator DER Retrieval.
//
// UTIL-004 is the end-to-end DERControl event flow for the
// Utility-Aggregator profile:
//
//  1. Aggregator has open Subscriptions on each managed inverter's
//     DERProgramList (UTIL-003 pattern, replayed inline here).
//  2. Server creates a DERControl on each topology node (SY/FDx/SPxx/DEV)
//     for each managed inverter, via the IEEE-024 test mutation hook
//     `/test/mutations/derctl-add` (build tag csip_test_hooks required).
//  3. (Spec)  Server fires Notifications to the aggregator's open
//     subscriptions.
//  4. Aggregator POSTs Response (received/started/completed) for each
//     managed inverter against /rsps/{rspsId}/rsp.
//  5. Server's response list reflects the POSTs.
//
// IEEE-093 update — Step 3 notification fan-out is now wired. The
// IEEE-024 derctl-add mutation hook calls ResourceNotifier.Notify on
// the parent DERProgramList href after a successful Create, and the
// csiptest.BootServer wires a real subscription.Manager into the test
// surface. Step 3 below stands up a small in-process HTTP receiver
// (utilNotificationReceiver, defined in this file) and points the per
// inverter Subscription's NotificationURI at it; after step 2 commits
// the 16 controls, the receiver records one Notification per managed
// inverter per derctl-add on the subscribed (SY-level) DERProgramList
// href — 4 inverters × 1 SY-level DERControl = 4 Notifications.
//
// Out-of-scope for IEEE-093 (and future Pike tickets):
//   - Receiver-side TLS verification — the receiver is plain HTTP.
//     UTIL-004 asserts the server emitted the Notification, not that
//     the spec's mTLS hop survives. A separate ticket can drive that.
//   - Notifications for non-subscribed FSA levels (FDx / SPxx / DEV).
//     UTIL-003 subscribes only the SY level; the other three
//     derctl-add calls happen but no Subscription is registered on
//     those hrefs so Manager.Notify does not fan out. This is by
//     design — the procedure exercises the priority-chain top.
package csip_test

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"gitlab.pnnl.gov/arista/ieee-2030_5/ieee-2030_5-core/pkg/sep2"
)

// utilMutationToken is the shared secret for `/test/mutations/*` under
// csip_test_hooks. Provided to the booted server via SEP2_TEST_MUTATION_TOKEN
// and sent on every mutation request as X-CSIP-Test-Token.
const utilMutationToken = "util004-test-token"

// utilResponseSetID is the {rspsId} path segment UTIL-004 POSTs Response
// objects under. One set serves all 4 managed inverters; each inverter's
// Response carries a unique Subject mRID so the GET list assertion can
// match by inverter.
const utilResponseSetID = "util004"

// utilDERControlBaseID is the per-node DERControl ID the mutation hook
// stores under. UTIL-004 creates one DERControl per (inverter, FSA-node)
// pair, so 4 inverters × 4 FSA levels = 16 controls. Per-pair ID is
// derived from `{edevID}-{fsaID}`.
func utilDERControlID(edevID, fsaID string) string {
	return fmt.Sprintf("ctl-%s-%s", edevID, fsaID)
}

// TestUTIL_004_DERRetrieval implements CSIP V1.2 §9.4.
func TestUTIL_004_DERRetrieval(t *testing.T) {
	// The mutation hook is gated on SEP2_TEST_MUTATION_TOKEN — see
	// internal/server/test_mutations.go RegisterMutationHandlers. Set
	// before BootServer so the server picks it up. Use t.Setenv so the
	// child test's env state is scoped, but note: csip_test_hooks reads
	// the env at boot time inside RegisterMutationHandlers, and the
	// process-wide env is shared across t.Parallel() siblings. UTIL-004
	// runs serially (no t.Parallel()) to avoid stomping the env.
	t.Setenv("SEP2_TEST_MUTATION_TOKEN", utilMutationToken)

	ctx := context.Background()
	srv := bootAggregatorTopology(t)
	client := srv.Client()
	rawClient := srv.HTTPClient()

	// IEEE-093: stand up a notification receiver before the
	// subscriptions are POSTed. The receiver's URL becomes each
	// Subscription's NotificationURI so the BootServer's Manager
	// fan-out path (step 3) lands here. The receiver is plain HTTP
	// (httptest.NewServer) — UTIL-004 asserts the server emitted the
	// Notification, not the spec's mTLS hop. Drop in IEEE-087's
	// csiptest.NotificationReceiver once #152 lands.
	receiver := newUTILNotificationReceiver(t)

	// Track which SY-level DERProgramList hrefs were subscribed; the
	// step-3 assertion checks for exactly these resource hrefs.
	subscribedHrefs := make(map[string]string, len(aggManagedInverters))

	// Step 1: open one Subscription per managed inverter against its
	// SY-level DERProgramList. We re-walk to discover the href rather
	// than hard-coding, so a fixture URL renaming surfaces here too.
	for _, edevID := range aggManagedInverters {
		fsaHref := fmt.Sprintf("/edev/%s/fsa?l=255", edevID)
		var fsaList sep2.FunctionSetAssignmentsList
		if err := client.WalkLink(ctx, sep2.Link{Href: fsaHref}, &fsaList); err != nil {
			t.Fatalf("step 1: WalkLink %s: %v", fsaHref, err)
		}
		progLink := fsaList.FunctionSetAssignments[0].DERProgramListLink
		if progLink == nil {
			t.Fatalf("step 1: edev=%q SY-FSA missing DERProgramListLink", edevID)
		}
		// IEEE-093: POST the subscription with the receiver's URL so
		// Manager.Notify fan-out reaches an in-test recorder.
		postSubscriptionToURI(t, ctx, rawClient, srv.BaseURL, edevID, progLink.Href, receiver.URL())
		subscribedHrefs[edevID] = progLink.Href
	}

	// Step 2: create a DERControl on each FSA node for each managed
	// inverter via the IEEE-024 mutation hook. 16 controls total
	// (4 inverters × 4 FSA levels SY/FDx/SPxx/DEV).
	mutationURL := srv.BaseURL + "/test/mutations/derctl-add"
	for _, edevID := range aggManagedInverters {
		for _, fsaID := range aggInverterFSAIDs {
			ctlID := utilDERControlID(edevID, fsaID)
			// DERProgram ID matches FSA ID per fixture convention.
			postDERControlMutation(t, ctx, rawClient, mutationURL,
				edevID, fsaID, fsaID, ctlID)
		}
	}

	// Step 2 (cont.): verify each control surfaces via the public GET
	// /edev/{id}/fsa/{fsaId}/derp/{derpId}/derc list. Sanity: the
	// mutation actually committed.
	//
	// The mutation hook stores the JSON-decoded sep2.DERControl as-is
	// — it does NOT inject an Href on the stored body (production
	// PUT/POST handlers do). So we identify the just-created control
	// by its MRID, which we set in postDERControlMutation.
	for _, edevID := range aggManagedInverters {
		for _, fsaID := range aggInverterFSAIDs {
			listHref := fmt.Sprintf("/edev/%s/fsa/%s/derp/%s/derc?l=255",
				edevID, fsaID, fsaID)
			var ctlList sep2.DERControlList
			if err := client.WalkLink(ctx, sep2.Link{Href: listHref}, &ctlList); err != nil {
				t.Fatalf("step 2-verify: WalkLink %s: %v", listHref, err)
			}
			wantMRID := fmt.Sprintf("UTIL-004-CTL-%s-%s", edevID, fsaID)
			ok := false
			for _, c := range ctlList.DERControl {
				if c.MRID == wantMRID {
					ok = true
					break
				}
			}
			if !ok {
				t.Errorf("step 2-verify: edev=%q fsa=%q: DERControl mRID=%q not in list (got %d entries)",
					edevID, fsaID, wantMRID, len(ctlList.DERControl))
			}
		}
	}

	// Step 3 (IEEE-093): assert the notification fan-out reached the
	// receiver. One Notification per managed inverter is expected —
	// the SY-level derctl-add fires Notify on the subscribed
	// DERProgramList href; the other three FSA levels have no
	// subscription registered against them. The Manager dispatches on
	// a bounded worker pool, so we Wait for the deliveries to land
	// before snapshotting.
	got, ok := receiver.Wait(len(aggManagedInverters), 2*time.Second)
	if !ok {
		t.Fatalf("step 3: receiver got %d Notifications, want %d before 2s timeout",
			len(got), len(aggManagedInverters))
	}
	// Build a (resource-href → seen) presence map keyed by inverter so
	// a missing inverter names itself in the failure message.
	seenHrefs := map[string]bool{}
	for _, n := range got {
		if n.Notification == nil {
			t.Errorf("step 3: received un-parseable Notification body: %q", string(n.Body))
			continue
		}
		seenHrefs[n.Notification.SubscribedResource] = true
		if n.Notification.Status != sep2.NotificationStatusChanged {
			t.Errorf("step 3: subscribedResource=%q Status=%d, want %d (Changed)",
				n.Notification.SubscribedResource, n.Notification.Status, sep2.NotificationStatusChanged)
		}
	}
	for _, edevID := range aggManagedInverters {
		want := subscribedHrefs[edevID]
		if !seenHrefs[want] {
			t.Errorf("step 3: edev=%q: no Notification for SubscribedResource=%q (saw hrefs: %v)",
				edevID, want, seenHrefs)
		}
	}

	// Step 4 + 5: aggregator POSTs Response (received → started →
	// completed) for each managed inverter, then GET the response list
	// and assert all three statuses are present per inverter.
	statusProgression := []uint8{
		sep2.ResponseStatusEventReceived,
		sep2.ResponseStatusEventStarted,
		sep2.ResponseStatusEventCompleted,
	}
	for _, edevID := range aggManagedInverters {
		// One Response per inverter per status. Subject = the
		// inverter's first-FSA DERControl mRID (a stable per-inverter
		// reference the aggregator would respond against; we use the
		// SY-level FSA's control mRID derived above).
		subject := utilDERControlID(edevID, aggFSAIDSY)
		for _, status := range statusProgression {
			postResponseAck(t, ctx, rawClient, srv.BaseURL,
				utilResponseSetID, status, subject)
		}
	}

	// Step 5: assert the response list reflects the POSTs for every
	// managed inverter at every status.
	listURL := fmt.Sprintf("%s/rsps/%s/rsp?l=255", srv.BaseURL, utilResponseSetID)
	getReq, err := http.NewRequestWithContext(ctx, http.MethodGet, listURL, nil)
	if err != nil {
		t.Fatalf("step 5: build GET %s: %v", listURL, err)
	}
	getResp, err := rawClient.Do(getReq)
	if err != nil {
		t.Fatalf("step 5: GET %s: %v", listURL, err)
	}
	defer func() { _ = getResp.Body.Close() }()
	if getResp.StatusCode != http.StatusOK {
		t.Fatalf("step 5: GET %s: status = %d, want 200", listURL, getResp.StatusCode)
	}
	raw, err := io.ReadAll(getResp.Body)
	if err != nil {
		t.Fatalf("step 5: read body: %v", err)
	}
	var list sep2.ResponseList
	if err := xml.Unmarshal(raw, &list); err != nil {
		t.Fatalf("step 5: unmarshal ResponseList: %v", err)
	}

	// Build a (subject, status) presence map and verify every inverter
	// got every status. Procedure-style assertion: each inverter is
	// individually verified so a single missing entry names the
	// inverter + status pair.
	seen := map[string]map[uint8]bool{}
	for _, r := range list.Response {
		if r.Status == nil {
			continue
		}
		if _, ok := seen[r.Subject]; !ok {
			seen[r.Subject] = map[uint8]bool{}
		}
		seen[r.Subject][*r.Status] = true
	}
	for _, edevID := range aggManagedInverters {
		subject := utilDERControlID(edevID, aggFSAIDSY)
		for _, status := range statusProgression {
			if !seen[subject][status] {
				t.Errorf("step 5: edev=%q subject=%q missing Response with status=%d",
					edevID, subject, status)
			}
		}
	}
}

// postDERControlMutation issues a POST against /test/mutations/derctl-add
// with a minimal DERControlBase, authenticated via X-CSIP-Test-Token.
// Asserts 201 Created strictly (the hook returns 201 on success per
// internal/server/test_mutations.go).
func postDERControlMutation(t *testing.T, ctx context.Context, client *http.Client, url, edevID, fsaID, derpID, controlID string) {
	t.Helper()

	mrid := fmt.Sprintf("UTIL-004-CTL-%s-%s", edevID, fsaID)
	connect := true
	body := map[string]any{
		"end_device_id":  edevID,
		"fsa_id":         fsaID,
		"der_program_id": derpID,
		"control_id":     controlID,
		"control": map[string]any{
			"MRID": mrid,
			"DERControlBase": map[string]any{
				"OpModConnect": &connect,
			},
		},
	}
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("derctl-add: marshal: %v", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("derctl-add: build request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-CSIP-Test-Token", utilMutationToken)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("derctl-add %s/%s/%s: %v", edevID, fsaID, controlID, err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("derctl-add %s/%s/%s: status = %d, want 201",
			edevID, fsaID, controlID, resp.StatusCode)
	}
}

// postResponseAck POSTs a sep2.Response with the given status + subject
// to /rsps/{rspsId}/rsp. Asserts 201 Created strictly per IEEE 2030.5
// §6.4.3 (create-via-POST returns 201 + Location).
func postResponseAck(t *testing.T, ctx context.Context, client *http.Client, baseURL, rspsID string, status uint8, subject string) {
	t.Helper()

	st := status
	rsp := sep2.Response{
		Status:  &st,
		Subject: subject,
	}
	body, err := xml.Marshal(&rsp)
	if err != nil {
		t.Fatalf("Response status=%d: marshal: %v", status, err)
	}

	url := fmt.Sprintf("%s/rsps/%s/rsp", baseURL, rspsID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		t.Fatalf("Response POST: build request: %v", err)
	}
	req.Header.Set("Content-Type", "application/sep+xml")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("Response POST %s: %v", url, err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("Response POST status=%d subject=%q: code = %d, want 201",
			status, subject, resp.StatusCode)
	}
}

// --- IEEE-093 step-3 helpers ----------------------------------------------
//
// utilReceivedNotification, utilNotificationReceiver, postSubscriptionToURI
// are local to UTIL-004. They cover exactly the step-3 surface — record
// POSTed Notifications, expose Wait/Snapshot, and POST a Subscription
// with a caller-supplied NotificationURI. When IEEE-087 lands the
// general csiptest.NotificationReceiver helper, delete this block and
// switch UTIL-004 to that. The local version is deliberately minimal
// (no WithStatusCode option, no Reset) — anything beyond step-3 verify
// belongs to the shared helper, not here.

// utilReceivedNotification is one captured POST body and its parsed
// sep2.Notification. Parsed Notification is nil when the body did not
// round-trip; the step-3 assertion treats that as a failure.
type utilReceivedNotification struct {
	Body         []byte
	Notification *sep2.Notification
}

// utilNotificationReceiver is an httptest server that records every
// POSTed Notification. Lifetime is bound to t via t.Cleanup; callers
// must not call Close themselves.
type utilNotificationReceiver struct {
	srv *httptest.Server
	mu  sync.Mutex
	got []utilReceivedNotification
}

// newUTILNotificationReceiver boots an in-process HTTP listener and
// registers t.Cleanup. Every POST records the request body and replies
// 200. The Subscription.NotificationURI in step 1 is pointed at
// URL().
func newUTILNotificationReceiver(t *testing.T) *utilNotificationReceiver {
	t.Helper()
	r := &utilNotificationReceiver{}
	r.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Method != http.MethodPost {
			http.Error(w, "POST only", http.StatusMethodNotAllowed)
			return
		}
		body, err := io.ReadAll(io.LimitReader(req.Body, 1<<16))
		if err != nil {
			http.Error(w, "read body", http.StatusBadRequest)
			return
		}
		var n sep2.Notification
		rec := utilReceivedNotification{Body: body}
		if uerr := xml.Unmarshal(body, &n); uerr == nil {
			rec.Notification = &n
		}
		r.mu.Lock()
		r.got = append(r.got, rec)
		r.mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(r.srv.Close)
	return r
}

// URL returns the receiver's base URL, suitable as a Subscription's
// NotificationURI.
func (r *utilNotificationReceiver) URL() string { return r.srv.URL }

// Wait blocks until at least n Notifications have been recorded or
// timeout elapses. Returns a snapshot and an ok flag — the caller
// decides how to format the failure.
func (r *utilNotificationReceiver) Wait(n int, timeout time.Duration) ([]utilReceivedNotification, bool) {
	deadline := time.Now().Add(timeout)
	for {
		r.mu.Lock()
		count := len(r.got)
		r.mu.Unlock()
		if count >= n {
			r.mu.Lock()
			out := make([]utilReceivedNotification, len(r.got))
			copy(out, r.got)
			r.mu.Unlock()
			return out, true
		}
		if time.Now().After(deadline) {
			r.mu.Lock()
			out := make([]utilReceivedNotification, len(r.got))
			copy(out, r.got)
			r.mu.Unlock()
			return out, false
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// postSubscriptionToURI POSTs one Subscription against /edev/{id}/sub
// with the supplied SubscribedResource and NotificationURI. Asserts
// 201 + a Location header. Mirrors postAndVerifySubscription from
// util_003 but takes the NotificationURI as a parameter so step 3 can
// route fan-outs to a recording receiver instead of the unreachable
// constant in UTIL-003.
func postSubscriptionToURI(t *testing.T, ctx context.Context, client *http.Client, baseURL, edevID, resource, notificationURI string) {
	t.Helper()
	sub := sep2.Subscription{
		SubscribedResource: resource,
		NotificationURI:    notificationURI,
		Encoding:           0, // 0 = XML per sep2 SubscriptionEncodingType
		Limit:              1,
	}
	body, err := xml.Marshal(&sub)
	if err != nil {
		t.Fatalf("postSubscriptionToURI: marshal: %v", err)
	}
	postURL := fmt.Sprintf("%s/edev/%s/sub", baseURL, edevID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, postURL, bytes.NewReader(body))
	if err != nil {
		t.Fatalf("postSubscriptionToURI: build %s: %v", postURL, err)
	}
	req.Header.Set("Content-Type", "application/sep+xml")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("postSubscriptionToURI POST %s: %v", postURL, err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("postSubscriptionToURI POST %s: status = %d, want 201", postURL, resp.StatusCode)
	}
	if loc := resp.Header.Get("Location"); loc == "" {
		t.Fatalf("postSubscriptionToURI POST %s: empty Location header", postURL)
	}
}
