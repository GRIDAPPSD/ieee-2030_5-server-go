// #147 - shared aggregator-operation helpers for AGG-001..AGG-012
// (CSIP V1.2 Section 10.1-Section 10.12).
//
// These helpers sit on top of the #139 aggregator-topology fixture
// (bootAggregatorTopology + the aggInverter* constants in
// util_topology_test.go). They cover the two shapes the AGG cluster
// shares:
//
//  1. injectEventSpec - overlay a Spec of DDERCs/DERControls onto an
//     already-booted topology. AGG-002..AGG-012 each declare what
//     events to inject across the topology nodes and then walk
//     /edev/{id}/fsa/{fsaId}/derp/{derpId}/{dderc,derc} on every
//     managed inverter to assert the wire shape.
//
//  2. walkAggregatorDERControlListAcrossInverters / walkAggregator-
//     DefaultDERControlAcrossInverters - verify that the injected
//     events surface on each of the 4 managed inverters at the named
//     FSA level (SY/FDx/SPxx/DEV).
//
// Anti-abstraction rule (per #147 ticket): every AGG-* procedure
// gets its own named Go test function. These helpers are setup +
// assertion plumbing, not a parametric "run AGG-N" loop.
package csip_test

import (
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/test/csip/csiptest"
)

// aggMRIDPrefix is the prefix every AGG test stamps onto MRIDs it
// injects, so multiple AGG sub-tests in one process never collide and
// failures name the offending test.
func aggMRIDPrefix(test string) string {
	return "AGG-" + test
}

// injectEventSpec calls csiptest.LoadSpec to overlay DDERCs/DERControls
// onto the already-loaded aggregator-topology stores. The caller passes
// the booted server and the spec; if loader errors, the test fails
// loudly with the originating fixture context.
//
// The spec must reference (end_device_id, fsa_id, der_program_id)
// triples that exist on the aggregator topology. fsa_id == der_program_id
// in the #139 fixture (SY="0" / FDx="1" / SPxx="2" / DEV="3").
func injectEventSpec(t *testing.T, srv *csiptest.BootedServer, spec *csiptest.Spec) {
	t.Helper()
	target := &csiptest.Target{
		EndDevices: srv.Stores.EndDevices,
		FSAs:       srv.Stores.FSAs,
		// #165 wrapped the scoped DERProgram store in
		// *memory.DERProgramStore for disk persistence. The loader Target
		// keys on the store.ScopedStore contract, which the wrapper
		// satisfies directly, so the wrapper goes in whole rather than
		// being unwrapped. See #175.
		DERPrograms:        srv.Stores.DERPrograms,
		DERControls:        srv.Stores.DERControls,
		DefaultDERControls: srv.Stores.DefaultDERControls,
		DERCurves:          srv.Stores.DERCurves,
	}
	if err := csiptest.LoadSpec(context.Background(), target, spec); err != nil {
		t.Fatalf("injectEventSpec: LoadSpec: %v", err)
	}
}

// walkAggregatorDERControlList walks /edev/{edev}/fsa/{fsa}/derp/{derp}/derc
// and returns the list. Used by AGG-002..012 to assert wire shape per
// managed inverter at a named topology node.
func walkAggregatorDERControlList(t *testing.T, ctx context.Context, c *csiptest.Client, edevID, fsaID, derpID string) sep2.DERControlList {
	t.Helper()
	href := fmt.Sprintf("/edev/%s/fsa/%s/derp/%s/derc?l=255", edevID, fsaID, derpID)
	var list sep2.DERControlList
	if err := c.WalkLink(ctx, sep2.Link{Href: href}, &list); err != nil {
		t.Fatalf("walk %s: %v", href, err)
	}
	return list
}

// walkAggregatorDefaultDERControl walks /edev/{edev}/fsa/{fsa}/derp/{derp}/dderc.
// AGG-002/004/006/etc. assert the DDERC surfaces on every managed
// inverter.
func walkAggregatorDefaultDERControl(t *testing.T, ctx context.Context, c *csiptest.Client, edevID, fsaID, derpID string) sep2.DefaultDERControl {
	t.Helper()
	href := fmt.Sprintf("/edev/%s/fsa/%s/derp/%s/dderc", edevID, fsaID, derpID)
	var dderc sep2.DefaultDERControl
	if err := c.WalkLink(ctx, sep2.Link{Href: href}, &dderc); err != nil {
		t.Fatalf("walk %s: %v", href, err)
	}
	return dderc
}

// aggSubscribableResource lists one of the 6 subscribable resources the
// V1.2 Section 10.1 AGG-001 procedure exercises against each managed inverter.
// Field names match the relative href the per-inverter aggregator
// subscription targets.
type aggSubscribableResource struct {
	Name string // human label for subtest naming
	Href string // POST target SubscribedResource (relative to baseURL)
}

// aggSubscribableResourcesForInverter returns the canonical 6 resources
// AGG-001 subscribes to per managed inverter. Each is a top-of-chain
// list URL because the aggregator subscription notifies on additions /
// deletions to that list. SY-level FSA ("0") is used as the entry
// point - the server scopes DERPrograms by EndDevice so any FSA's
// DERProgramList href reaches all 4 programs (CORE-010 documented this).
func aggSubscribableResourcesForInverter(edevID string) []aggSubscribableResource {
	return []aggSubscribableResource{
		{Name: "edevList", Href: "/edev"},
		{Name: "endDevice", Href: fmt.Sprintf("/edev/%s", edevID)},
		{Name: "fsaList", Href: fmt.Sprintf("/edev/%s/fsa", edevID)},
		{Name: "derProgramList", Href: fmt.Sprintf("/edev/%s/fsa/%s/derp", edevID, aggFSAIDSY)},
		{Name: "derProgram", Href: fmt.Sprintf("/edev/%s/fsa/%s/derp/%s", edevID, aggFSAIDSY, aggFSAIDSY)},
		{Name: "derControlList", Href: fmt.Sprintf("/edev/%s/fsa/%s/derp/%s/derc", edevID, aggFSAIDSY, aggFSAIDSY)},
	}
}

// aggregatorNotificationURI is the per-test aggregator callback URL for
// AGG-001 subscriptions. The procedure exercises subscription acceptance
// only - delivery is gated on #12 follow-ups outside #147 scope.
const aggregatorNotificationURI = "https://aggregator.example/notify/agg"

// postAggregatorSubscription is the AGG-001 variant of UTIL-003's
// postAndVerifySubscription: it POSTs a Subscription to
// /edev/{edevID}/sub with a configurable SubscribedResource, asserts
// 201 + non-empty Location, and confirms the new entry surfaces in the
// per-inverter subscription list.
//
// AGG-001 calls this 6 times per managed inverter (one per subscribable
// resource) across 4 inverters - 24 POSTs total. We run the per-inverter
// loop in parallel under t.Run subtests so the SubscriptionStore is
// hammered concurrently (race-detector gate).
func postAggregatorSubscription(t *testing.T, ctx context.Context, client *http.Client, baseURL, edevID, resource string) {
	t.Helper()

	sub := sep2.Subscription{
		SubscribedResource: resource,
		NotificationURI:    aggregatorNotificationURI,
		Encoding:           0, // XML
		Limit:              1,
	}
	body, err := xml.Marshal(&sub)
	if err != nil {
		t.Fatalf("aggregator subscription marshal (resource=%q): %v", resource, err)
	}
	postURL := fmt.Sprintf("%s/edev/%s/sub", baseURL, edevID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, postURL, bytes.NewReader(body))
	if err != nil {
		t.Fatalf("aggregator subscription build POST %s: %v", postURL, err)
	}
	req.Header.Set("Content-Type", "application/sep+xml")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("aggregator subscription POST %s: %v", postURL, err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("aggregator subscription POST %s: status=%d, want 201", postURL, resp.StatusCode)
	}
	if resp.Header.Get("Location") == "" {
		t.Fatalf("aggregator subscription POST %s: empty Location header", postURL)
	}

	// GET /edev/{id}/sub and assert the new sub is present with the
	// expected SubscribedResource - pins down both persistence and
	// scope. Per-inverter subtests run in parallel, so this is also
	// the race-detector probe surface.
	listURL := fmt.Sprintf("%s/edev/%s/sub?l=255", baseURL, edevID)
	getReq, err := http.NewRequestWithContext(ctx, http.MethodGet, listURL, nil)
	if err != nil {
		t.Fatalf("aggregator subscription build GET %s: %v", listURL, err)
	}
	getResp, err := client.Do(getReq)
	if err != nil {
		t.Fatalf("aggregator subscription GET %s: %v", listURL, err)
	}
	defer func() { _ = getResp.Body.Close() }()
	if getResp.StatusCode != http.StatusOK {
		t.Fatalf("aggregator subscription GET %s: status=%d, want 200", listURL, getResp.StatusCode)
	}
	raw, err := io.ReadAll(getResp.Body)
	if err != nil {
		t.Fatalf("aggregator subscription read GET %s: %v", listURL, err)
	}
	var list sep2.SubscriptionList
	if err := xml.Unmarshal(raw, &list); err != nil {
		t.Fatalf("aggregator subscription unmarshal %s: %v", listURL, err)
	}
	found := false
	for _, s := range list.Subscription {
		if s.SubscribedResource == resource && s.NotificationURI == aggregatorNotificationURI {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("aggregator subscription not surfaced in GET %s: resource=%q (got %d entries)",
			listURL, resource, len(list.Subscription))
	}
}

// assertAggregatorSubscriptionsPresent GETs /edev/{edevID}/sub and
// asserts strict per-inverter membership: exactly the supplied
// wantResources are present, with no extras and no foreign-edev
// leakage. Used by AGG-001 after the parallel per-inverter subscription
// burst joins.
//
// Pre-#168, the SubscriptionStore returned the union of all POSTed
// subscriptions for every /edev/{id}/sub GET, and this helper accepted
// the union as long as each wanted href was present (24 entries for
// each of the 4 inverters after a 24-POST burst). #168 scoped
// GET /edev/{id}/sub to the EndDevice {id}, so this helper now enforces
// the strict membership the V1.2 Section 10.1 procedure implies.
func assertAggregatorSubscriptionsPresent(t *testing.T, ctx context.Context, client *http.Client, baseURL, edevID string, wantResources []string) {
	t.Helper()
	listURL := fmt.Sprintf("%s/edev/%s/sub?l=255", baseURL, edevID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, listURL, nil)
	if err != nil {
		t.Fatalf("count_gate build GET %s: %v", listURL, err)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("count_gate GET %s: %v", listURL, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("count_gate GET %s: status=%d, want 200", listURL, resp.StatusCode)
	}
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("count_gate read %s: %v", listURL, err)
	}
	var list sep2.SubscriptionList
	if err := xml.Unmarshal(raw, &list); err != nil {
		t.Fatalf("count_gate unmarshal %s: %v", listURL, err)
	}

	// #168: strict per-inverter scoping. Every entry the server
	// returns must belong to this inverter (Href prefix /edev/{edevID}/sub/)
	// and must be one of this inverter's aggregator subscriptions
	// (NotificationURI matches aggregatorNotificationURI). Anything
	// else is a cross-EndDevice leak.
	wantHrefPrefix := fmt.Sprintf("/edev/%s/sub/", edevID)
	seen := make(map[string]bool, len(list.Subscription))
	for _, s := range list.Subscription {
		if s.NotificationURI != aggregatorNotificationURI {
			t.Errorf("inverter=%q /sub list contains non-aggregator entry: SubscribedResource=%q NotificationURI=%q",
				edevID, s.SubscribedResource, s.NotificationURI)
			continue
		}
		if !strings.HasPrefix(s.Href, wantHrefPrefix) {
			t.Errorf("inverter=%q /sub list leaked foreign entry: Href=%q (want prefix %q)",
				edevID, s.Href, wantHrefPrefix)
			continue
		}
		if seen[s.SubscribedResource] {
			t.Errorf("inverter=%q /sub list has duplicate SubscribedResource=%q", edevID, s.SubscribedResource)
		}
		seen[s.SubscribedResource] = true
	}

	for _, want := range wantResources {
		if !seen[want] {
			t.Errorf("inverter=%q /sub list missing SubscribedResource=%q (had %d entries)",
				edevID, want, len(list.Subscription))
		}
	}
	// Exact count: this inverter's list must contain exactly the wanted
	// 6 aggregator subscriptions - no extras, no foreign-edev bleed.
	if list.All != uint32(len(wantResources)) {
		t.Errorf("inverter=%q SubscriptionList.All = %d, want %d (per-EndDevice scope)",
			edevID, list.All, len(wantResources))
	}
	if len(list.Subscription) != len(wantResources) {
		t.Errorf("inverter=%q len(SubscriptionList.Subscription) = %d, want %d (per-EndDevice scope)",
			edevID, len(list.Subscription), len(wantResources))
	}
}

// aggOverlapPlan is the per-test value plan for AGG-007..012. SYValue
// goes into the SY-level DERControl's opMod; XfmrValue goes into the
// FDx-level DERControl's opMod (similar variants) or is ignored
// (independent variants - FDx uses OpModConnect=true regardless).
//
// Each named AGG-* test supplies a unique aggOverlapPlan so wire-side
// regressions stay test-scoped: a stomped runner that only matched
// AGG-008's values would leave AGG-007 and AGG-009 untouched.
type aggOverlapPlan struct {
	SYValue   int16
	XfmrValue int16
}

// runAggregatorOverlapSimilar is the shared body for AGG-007..009. Each
// caller supplies a test ID (string, used in MRIDs + DERControl IDs)
// and a value plan. The body injects 1 DDERC + 1 DERControl at SY AND
// 1 DDERC + 1 DERControl at FDx, all DERControls carrying OpModFixedW
// (the "similar" invariant). It then walks every managed inverter at
// both nodes and asserts the wire shape.
//
// Anti-abstraction note: this is setup + assertion plumbing only. The
// 3 AGG-* test functions in agg_00{7,8,9}_*.go remain named, distinct
// Go tests - a conformance reviewer sees TestAGG_007_..., TestAGG_008_...,
// TestAGG_009_... each with its own V1.2 Section 10.X doc comment.
func runAggregatorOverlapSimilar(t *testing.T, testID string, plan aggOverlapPlan) {
	t.Helper()
	ctx := context.Background()

	srv := bootAggregatorTopology(t)
	client := srv.Client()

	spec := &csiptest.Spec{}
	for _, edevID := range aggManagedInverters {
		spec.DefaultDERControls = append(spec.DefaultDERControls,
			csiptest.DefaultDERControlSpec{
				EndDeviceID:  edevID,
				FSAID:        aggFSAIDSY,
				DERProgramID: aggFSAIDSY,
				MRID:         aggMRIDPrefix(testID+"-DDERC-SY-") + edevID,
				DERControlBase: &csiptest.DERControlBaseSpec{
					OpModFixedW: &csiptest.ActivePowerSpec{Multiplier: 0, Value: plan.SYValue + 500},
				},
			},
			csiptest.DefaultDERControlSpec{
				EndDeviceID:  edevID,
				FSAID:        aggFSAIDFD,
				DERProgramID: aggFSAIDFD,
				MRID:         aggMRIDPrefix(testID+"-DDERC-FD-") + edevID,
				DERControlBase: &csiptest.DERControlBaseSpec{
					OpModFixedW: &csiptest.ActivePowerSpec{Multiplier: 0, Value: plan.XfmrValue + 500},
				},
			},
		)
		spec.DERControls = append(spec.DERControls,
			csiptest.DERControlSpec{
				EndDeviceID:  edevID,
				FSAID:        aggFSAIDSY,
				DERProgramID: aggFSAIDSY,
				ID:           "agg" + testID + "-" + edevID + "-sy",
				DERControlBase: &csiptest.DERControlBaseSpec{
					OpModFixedW: &csiptest.ActivePowerSpec{Multiplier: 0, Value: plan.SYValue},
				},
			},
			csiptest.DERControlSpec{
				EndDeviceID:  edevID,
				FSAID:        aggFSAIDFD,
				DERProgramID: aggFSAIDFD,
				ID:           "agg" + testID + "-" + edevID + "-fd",
				DERControlBase: &csiptest.DERControlBaseSpec{
					OpModFixedW: &csiptest.ActivePowerSpec{Multiplier: 0, Value: plan.XfmrValue},
				},
			},
		)
	}
	injectEventSpec(t, srv, spec)

	for _, edevID := range aggManagedInverters {
		edevID := edevID
		t.Run("inverter_"+edevID, func(t *testing.T) {
			t.Parallel()
			for _, n := range []struct {
				fsaID    string
				wantVal  int16
				ddercTag string
			}{
				{aggFSAIDSY, plan.SYValue, "SY"},
				{aggFSAIDFD, plan.XfmrValue, "FD"},
			} {
				dderc := walkAggregatorDefaultDERControl(t, ctx, client, edevID, n.fsaID, n.fsaID)
				wantMRID := aggMRIDPrefix(testID+"-DDERC-"+n.ddercTag+"-") + edevID
				if dderc.MRID != wantMRID {
					t.Errorf("node=%s DDERC.MRID = %q, want %q", n.fsaID, dderc.MRID, wantMRID)
				}
				if dderc.DERControlBase == nil || dderc.DERControlBase.OpModFixedW == nil {
					t.Errorf("node=%s DDERC missing OpModFixedW (similar invariant)", n.fsaID)
				}
				list := walkAggregatorDERControlList(t, ctx, client, edevID, n.fsaID, n.fsaID)
				if list.All != 1 {
					t.Errorf("node=%s DERControlList.All = %d, want 1", n.fsaID, list.All)
				}
				if len(list.DERControl) != 1 {
					t.Fatalf("node=%s len(DERControlList.DERControl) = %d, want 1",
						n.fsaID, len(list.DERControl))
				}
				dc := list.DERControl[0]
				if dc.DERControlBase == nil || dc.DERControlBase.OpModFixedW == nil {
					t.Errorf("node=%s DERControl[0] missing OpModFixedW (similar invariant)", n.fsaID)
					continue
				}
				if dc.DERControlBase.OpModFixedW.Value != n.wantVal {
					t.Errorf("node=%s DERControl[0].OpModFixedW.Value = %d, want %d",
						n.fsaID, dc.DERControlBase.OpModFixedW.Value, n.wantVal)
				}
			}
		})
	}
}

// runAggregatorOverlapIndependent is the shared body for AGG-010..012.
// Same fixture shape as runAggregatorOverlapSimilar except the FDx-node
// DERControl uses OpModConnect (a different control axis) instead of
// OpModFixedW. AGG-010..012 assert each node carries ONLY its own
// opMod - a regression that bled OpModConnect into the SY DERControl
// would surface as a missing OpModFixedW assertion.
func runAggregatorOverlapIndependent(t *testing.T, testID string, plan aggOverlapPlan) {
	t.Helper()
	ctx := context.Background()

	srv := bootAggregatorTopology(t)
	client := srv.Client()

	connect := true
	spec := &csiptest.Spec{}
	for _, edevID := range aggManagedInverters {
		// SY: DDERC + DERControl with OpModFixedW.
		spec.DefaultDERControls = append(spec.DefaultDERControls,
			csiptest.DefaultDERControlSpec{
				EndDeviceID:  edevID,
				FSAID:        aggFSAIDSY,
				DERProgramID: aggFSAIDSY,
				MRID:         aggMRIDPrefix(testID+"-DDERC-SY-") + edevID,
				DERControlBase: &csiptest.DERControlBaseSpec{
					OpModFixedW: &csiptest.ActivePowerSpec{Multiplier: 0, Value: plan.SYValue + 1000},
				},
			},
		)
		spec.DERControls = append(spec.DERControls,
			csiptest.DERControlSpec{
				EndDeviceID:  edevID,
				FSAID:        aggFSAIDSY,
				DERProgramID: aggFSAIDSY,
				ID:           "agg" + testID + "-" + edevID + "-sy",
				DERControlBase: &csiptest.DERControlBaseSpec{
					OpModFixedW: &csiptest.ActivePowerSpec{Multiplier: 0, Value: plan.SYValue},
				},
			},
		)
		// FDx: DDERC + DERControl with OpModConnect (independent mode).
		spec.DefaultDERControls = append(spec.DefaultDERControls,
			csiptest.DefaultDERControlSpec{
				EndDeviceID:  edevID,
				FSAID:        aggFSAIDFD,
				DERProgramID: aggFSAIDFD,
				MRID:         aggMRIDPrefix(testID+"-DDERC-FD-") + edevID,
				DERControlBase: &csiptest.DERControlBaseSpec{
					OpModConnect: &connect,
				},
			},
		)
		spec.DERControls = append(spec.DERControls,
			csiptest.DERControlSpec{
				EndDeviceID:  edevID,
				FSAID:        aggFSAIDFD,
				DERProgramID: aggFSAIDFD,
				ID:           "agg" + testID + "-" + edevID + "-fd",
				DERControlBase: &csiptest.DERControlBaseSpec{
					OpModConnect: &connect,
				},
			},
		)
	}
	injectEventSpec(t, srv, spec)

	for _, edevID := range aggManagedInverters {
		edevID := edevID
		t.Run("inverter_"+edevID, func(t *testing.T) {
			t.Parallel()

			// SY node: OpModFixedW present, OpModConnect absent.
			ddercSY := walkAggregatorDefaultDERControl(t, ctx, client, edevID, aggFSAIDSY, aggFSAIDSY)
			wantSY := aggMRIDPrefix(testID+"-DDERC-SY-") + edevID
			if ddercSY.MRID != wantSY {
				t.Errorf("SY DDERC.MRID = %q, want %q", ddercSY.MRID, wantSY)
			}
			if ddercSY.DERControlBase == nil || ddercSY.DERControlBase.OpModFixedW == nil {
				t.Errorf("SY DDERC missing OpModFixedW (independent invariant: SY uses FixedW)")
			}
			if ddercSY.DERControlBase != nil && ddercSY.DERControlBase.OpModConnect != nil {
				t.Errorf("SY DDERC unexpectedly carries OpModConnect (independent-mode bleed)")
			}
			listSY := walkAggregatorDERControlList(t, ctx, client, edevID, aggFSAIDSY, aggFSAIDSY)
			if len(listSY.DERControl) != 1 {
				t.Fatalf("SY DERControlList len = %d, want 1", len(listSY.DERControl))
			}
			if listSY.DERControl[0].DERControlBase == nil ||
				listSY.DERControl[0].DERControlBase.OpModFixedW == nil ||
				listSY.DERControl[0].DERControlBase.OpModFixedW.Value != plan.SYValue {
				t.Errorf("SY DERControl[0] OpModFixedW dropped or wrong value")
			}
			if listSY.DERControl[0].DERControlBase != nil &&
				listSY.DERControl[0].DERControlBase.OpModConnect != nil {
				t.Errorf("SY DERControl[0] unexpectedly carries OpModConnect (independent-mode bleed)")
			}

			// FDx node: OpModConnect present, OpModFixedW absent.
			ddercFD := walkAggregatorDefaultDERControl(t, ctx, client, edevID, aggFSAIDFD, aggFSAIDFD)
			wantFD := aggMRIDPrefix(testID+"-DDERC-FD-") + edevID
			if ddercFD.MRID != wantFD {
				t.Errorf("FDx DDERC.MRID = %q, want %q", ddercFD.MRID, wantFD)
			}
			if ddercFD.DERControlBase == nil || ddercFD.DERControlBase.OpModConnect == nil ||
				!*ddercFD.DERControlBase.OpModConnect {
				t.Errorf("FDx DDERC missing OpModConnect=true (independent invariant: FDx uses Connect)")
			}
			if ddercFD.DERControlBase != nil && ddercFD.DERControlBase.OpModFixedW != nil {
				t.Errorf("FDx DDERC unexpectedly carries OpModFixedW (independent-mode bleed)")
			}
			listFD := walkAggregatorDERControlList(t, ctx, client, edevID, aggFSAIDFD, aggFSAIDFD)
			if len(listFD.DERControl) != 1 {
				t.Fatalf("FDx DERControlList len = %d, want 1", len(listFD.DERControl))
			}
			if listFD.DERControl[0].DERControlBase == nil ||
				listFD.DERControl[0].DERControlBase.OpModConnect == nil ||
				!*listFD.DERControl[0].DERControlBase.OpModConnect {
				t.Errorf("FDx DERControl[0] OpModConnect=true dropped on the wire")
			}
			if listFD.DERControl[0].DERControlBase != nil &&
				listFD.DERControl[0].DERControlBase.OpModFixedW != nil {
				t.Errorf("FDx DERControl[0] unexpectedly carries OpModFixedW (independent-mode bleed)")
			}
		})
	}
}
