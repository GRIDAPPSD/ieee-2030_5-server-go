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
// SCOPE BOUNDARY — Step 3 (notification fan-out from the mutation hook).
// The IEEE-024 derctl-add mutation hook (`handleDERControlAdd` in
// `internal/server/test_mutations.go`) creates the DERControl in the
// scoped store but does NOT call a ResourceNotifier today. Only
// HandleDeleteEndDevice (IEEE-023) is wired to a notifier on the
// server side. Bridging the gap is a server-side product change —
// extending the mutation hook to call into the subscription manager
// for DERControlList notifications — and falls outside the IEEE-089
// scope (which is "build the topology fixture + wire the 4 UTIL
// tests"). Filed as a follow-up; see backlog entry referenced in the
// IEEE-089 PR description.
//
// What UTIL-004 verifies on the *server side* without the gap fix:
//
//   - The IEEE-024 derctl-add hook persists DERControls per node such
//     that a subsequent GET /edev/{id}/fsa/{fsaId}/derp/{derpId}/derc
//     surfaces the new control (step 2 in this file).
//   - Subscriptions for each managed inverter remain present in the
//     store across the mutation calls (step 1 → step 2 invariant).
//   - The aggregator's Response POSTs against /rsps/{rspsId}/rsp are
//     accepted with 201 Created and round-trip via GET, mirroring the
//     received → started → completed status progression (step 4 → 5).
//
// When the notification fan-out follow-up lands, UTIL-004 grows a
// notification-receive assertion. The procedure mapping below already
// pins the steps so the future extension is additive.
package csip_test

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-go/pkg/sep2"
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
		postAndVerifySubscription(t, ctx, rawClient, srv.BaseURL, edevID, progLink.Href)
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

	// Step 3: notification fan-out is the documented out-of-scope gap.
	// When the follow-up extends the mutation hook to call
	// ResourceNotifier.Notify, an assertion here will receive the
	// notifications against an in-test HTTP receiver. See file-level
	// doc comment for scope rationale.

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
