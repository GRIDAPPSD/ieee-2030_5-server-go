// CSIP V1.2 §7.2 — Responses.
//
// CORE-022 asserts that a CSIP server accepts Response POSTs against a
// ResponseSet's /rsps/{rspsId}/rsp endpoint with the four
// status-progression values the V1.2 procedure exercises:
//
//	Received  = 1  (sep2.ResponseStatusEventReceived)
//	Started   = 2  (sep2.ResponseStatusEventStarted)
//	Completed = 3  (sep2.ResponseStatusEventCompleted)
//	Cancelled = 6  (sep2.ResponseStatusEventCancelled — Table 31
//	                "Cancelled" row)
//
// V1.2 procedure step → assertion mapping:
//
//	Step 1 (POST Response status=1 (Received))   ──► postResponse, expects 201 + Location
//	Step 2 (POST Response status=2 (Started))    ──► postResponse, expects 201 + Location
//	Step 3 (POST Response status=3 (Completed))  ──► postResponse, expects 201 + Location
//	Step 4 (POST Response status=6 (Cancelled))  ──► postResponse, expects 201 + Location
//	Step 5 (GET /rsps/{rspsId}/rsp and assert all four are present)
//	                                             ──► assertAllStatusesPresent
//
// Constant-vs-wire-value alignment: prior to #109 the sep2
// ResponseStatus* constants were off-by-one against IEEE 2030.5-2023
// §10.10 Table 31, so this test used raw uint8 literals 1/2/3/6.
// #109 renumbered the constants to match Table 31 wire values
// (EventReceived=1, EventStarted=2, EventCompleted=3, EventCancelled=6)
// so this test now references the named constants directly. The
// CSIP V1.2 procedure step 4 says "Acknowledged" in prose; the wire
// value is 6, which Table 31 names "Cancelled" — the V1.2 procedure
// text predates the Table 31 naming. The wire value is what matters.
//
// Why no V1.2 §7.2 mention of HTTP 200: the procedure expects 201
// Created on POST per IEEE 2030.5 §6.4.3 (create-via-POST returns
// 201 with Location). The CSIP harness's existing HandlePostResponse
// returns 201 explicitly, so the test asserts 201 strictly. If a
// future server convention shifts to 200, this test fails loudly
// rather than silently — that is the right behavior for a
// conformance test pinned to a procedure step.
//
// Router fix bundled in this PR: GET /rsps/{rspsId}/rsp was wired via
// scopedListHandler, which keys on PathValue("id"). Under the
// {rspsId} placeholder that always reads as "" — so the POST under
// rspsId="set1" was stored, but the GET scoped to "" returned an
// empty list. The fix inlines a small closure that reads the correct
// path value. See PR description for the one-line change in
// internal/server/router.go.
package csip_test

import (
	"bytes"
	"context"
	"encoding/xml"
	"io"
	"net/http"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/test/csip/csiptest"
)

// V1.2 §7.2 status wire values exercised by CORE-022.
//
// #109 aligned the sep2.ResponseStatus* Go constants with Table 31
// wire values (Received=1, Started=2, Completed=3, Cancelled=6) so the
// procedure-required statuses are now expressed via the named
// constants directly.
const (
	core022StatusReceived  = sep2.ResponseStatusEventReceived  // 1
	core022StatusStarted   = sep2.ResponseStatusEventStarted   // 2
	core022StatusCompleted = sep2.ResponseStatusEventCompleted // 3
	core022StatusCancelled = sep2.ResponseStatusEventCancelled // 6
)

// core022ResponseSetID is the {rspsId} path segment we POST under. The
// global ResponseSets store does not require pre-creation of the set
// for POST to succeed — HandlePostResponse stores under the path's
// rspsId verbatim. Tests for ResponseSet lifecycle (create, list,
// delete) live elsewhere.
const core022ResponseSetID = "set1"

// TestCORE_022_Responses implements CSIP V1.2 §7.2 (Response POST +
// retrievable via GET, across the four procedure-required statuses).
func TestCORE_022_Responses(t *testing.T) {
	t.Parallel()

	// Build a CA + device cert under our control. BootServer would
	// otherwise generate its own ephemeral device cert and not expose
	// it, but we want a single *http.Client wired with a controlled
	// identity for both POSTs and GETs — same pattern CORE-009 uses
	// for its PUT flow.
	_, caCertFile, clientCert := mustBuildClientPKI(t)
	srv := csiptest.BootServer(t,
		csiptest.WithClientCAsFile(caCertFile),
		csiptest.WithClientCert(clientCert),
	)
	httpClient := buildClient(t, srv.RootCA, clientCert)
	ctx := context.Background()

	// V1.2 §7.2 walks the four statuses in order — the test mirrors
	// that order so a test log read top-to-bottom matches the V1.2
	// procedure read top-to-bottom.
	postSteps := []struct {
		stepName string
		status   uint8
		subject  string // mRID the Response refers to
	}{
		{stepName: "Step1_Received", status: core022StatusReceived, subject: "evt-A"},
		{stepName: "Step2_Started", status: core022StatusStarted, subject: "evt-A"},
		{stepName: "Step3_Completed", status: core022StatusCompleted, subject: "evt-A"},
		{stepName: "Step4_Cancelled", status: core022StatusCancelled, subject: "evt-A"},
	}

	for _, step := range postSteps {
		step := step
		t.Run(step.stepName, func(t *testing.T) {
			location := postResponse(t, httpClient, srv.BaseURL, core022ResponseSetID, step.status, step.subject)
			if location == "" {
				t.Fatalf("POST %s: empty Location header", step.stepName)
			}
		})
	}

	// Step 5: GET the response list and assert all four statuses are
	// present. The POSTs above ran as subtests without t.Parallel, so
	// t.Run is synchronous and the GET runs after the last POST.
	var list sep2.ResponseList
	listHref := "/rsps/" + core022ResponseSetID + "/rsp"
	getResponseList(t, ctx, httpClient, srv.BaseURL+listHref, &list)
	assertAllStatusesPresent(t, list, []uint8{
		core022StatusReceived,
		core022StatusStarted,
		core022StatusCompleted,
		core022StatusCancelled,
	})
}

// postResponse marshals a Response with the given status + subject and
// POSTs it to <baseURL>/rsps/{rspsId}/rsp. Returns the value of the
// Location header on success. Asserts 201 Created strictly (V1.2 §7.2
// + IEEE 2030.5 §6.4.3 — create-via-POST returns 201).
//
// The body is built via xml.Marshal on a sep2.Response — same path
// HandlePostResponse decodes, so a marshalling regression on either
// side surfaces here as a 400 BadRequest, which is exactly the failure
// mode we want.
func postResponse(t *testing.T, client *http.Client, baseURL, rspsID string, status uint8, subject string) string {
	t.Helper()

	st := status
	rsp := sep2.Response{
		Status:  &st,
		Subject: subject,
	}
	body, err := xml.Marshal(&rsp)
	if err != nil {
		t.Fatalf("marshal Response status=%d: %v", status, err)
	}

	url := baseURL + "/rsps/" + rspsID + "/rsp"
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		t.Fatalf("build POST %s: %v", url, err)
	}
	req.Header.Set("Content-Type", "application/sep+xml")

	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("POST %s: %v", url, err)
	}
	defer func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST %s status=%d: code = %d, want 201", url, status, resp.StatusCode)
	}
	return resp.Header.Get("Location")
}

// getResponseList issues a GET against url and unmarshals the body
// into dest as XML. Asserts 200 OK. A focused helper rather than a
// reach into csiptest's WalkLink because POST flow already requires a
// controlled *http.Client, and reusing it for the GET keeps the
// transport story for CORE-022 single-rooted.
func getResponseList(t *testing.T, ctx context.Context, client *http.Client, url string, dest *sep2.ResponseList) {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		t.Fatalf("build GET %s: %v", url, err)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s: status = %d, want 200", url, resp.StatusCode)
	}
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read GET %s: %v", url, err)
	}
	if err := xml.Unmarshal(raw, dest); err != nil {
		t.Fatalf("unmarshal GET %s: %v", url, err)
	}
}

// assertAllStatusesPresent verifies the ResponseList contains exactly
// one Response per wantStatus value. Spelled out as a helper so a
// reviewer can map the assertions to the V1.2 §7.2 conformance
// criteria without scrolling through transport boilerplate.
//
// "Exactly one per status" is a stricter assertion than the procedure
// strictly requires (V1.2 §7.2 only says each status is retrievable,
// not that there is exactly one). The stricter version is the right
// shape for a server-side conformance test: every status we POSTed
// should appear, no duplicates injected by the handler, no extras
// from cross-test bleed. If a future procedure interpretation needs
// "at least one" semantics, loosen here.
func assertAllStatusesPresent(t *testing.T, list sep2.ResponseList, wantStatuses []uint8) {
	t.Helper()

	if list.All != uint32(len(wantStatuses)) {
		t.Errorf("ResponseList.All = %d, want %d", list.All, len(wantStatuses))
	}
	if len(list.Response) != len(wantStatuses) {
		t.Fatalf("len(list.Response) = %d, want %d", len(list.Response), len(wantStatuses))
	}

	// Count occurrences per status value. A map keeps the assertion
	// order-independent — the store does not guarantee insertion
	// order across versions.
	seen := make(map[uint8]int, len(wantStatuses))
	for i, r := range list.Response {
		if r.Status == nil {
			t.Errorf("Response[%d].Status = nil, want one of %v", i, wantStatuses)
			continue
		}
		seen[*r.Status]++
	}
	for _, want := range wantStatuses {
		if seen[want] != 1 {
			t.Errorf("Response status=%d count = %d, want 1 (seen map: %v)", want, seen[want], seen)
		}
	}
}
