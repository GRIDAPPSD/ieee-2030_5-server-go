package wadl_test

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/test/conformance/wadl"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/test/csip/csiptest"
)

// expectedGap is what the sweep measures for one WADL resource today. A resource
// with no entry must be fully inside the method-mode policy (policy.go), reached
// by the crawl, and refuse the ordinary device on every manager-only write it
// declares.
//
// The map below is a RATCHET, not a target: the gap is real and tracked, and
// pinning it per resource keeps it from widening unnoticed and keeps a fix from
// going unrecorded. With the schema every field is an equality, so a fix pins
// its own resource's entry in the PR that makes it and touches no other lines.
// Write each entry on several lines and lower it to zero rather than deleting it
// while the epic runs, so neighbouring entries stay separate hunks in a merge.
//
// The end state is an empty map and len(outside) == 0: the last slice deletes
// expectedGaps and this type and the tests assert zero outright.
//
// This is names, not a transcription: resource ids are what the standard calls
// them, and no method list or path is committed.
type expectedGap struct {
	// Methods outside the policy, by wx:mode. A method of a resource the crawl
	// did not reach counts here too, since it is not probed.
	Mandatory, Optional, Deprecated, Error int
	// Unreached marks a resource with a Mandatory or Optional method that the
	// crawl misses though the schema names a link for it.
	Unreached bool
	// Unlinked marks a resource with a Mandatory or Optional method that the
	// schema names no link for, so no crawl reaches it and the sweep probes it at
	// its sample path. It is a pinned mark: unlinkedMarks checks it against the
	// schema, and the sweep does not measure it.
	Unlinked bool
	// RestrictedNotForbidden counts the manager-only writes probed as the device
	// that were not refused with a 403 from an authority check.
	RestrictedNotForbidden int
}

// expectedGaps is measured against the server csiptest boots, with the route set
// core's assembly registers.
var expectedGaps = map[string]expectedGap{
	"AccountBalance": {
		Mandatory:  2,
		Deprecated: 1,
		Error:      2,
		Unreached:  true,
	},
	"ActiveBillingPeriodList": {
		Mandatory: 2,
		Error:     3,
		Unreached: true,
	},
	"ActiveDERControlList": {
		Error: 3,
	},
	"ActiveEndDeviceControlList": {
		Mandatory: 2,
		Error:     3,
		Unreached: true,
	},
	"ActiveSupplyInterruptionOverrideList": {
		Mandatory: 2,
		Error:     3,
		Unreached: true,
	},
	"ActiveTextMessageList": {
		Error: 1,
	},
	"ActiveTimeTariffIntervalList": {
		Mandatory: 2,
		Error:     3,
		Unreached: true,
	},
	"AssociatedDERProgramList": {
		Optional:               1,
		RestrictedNotForbidden: 1,
	},
	"AssociatedUsagePoint": {
		Optional:               1,
		RestrictedNotForbidden: 1,
	},
	"BillingPeriod": {
		Mandatory:  2,
		Deprecated: 2,
		Error:      1,
		Unreached:  true,
	},
	"BillingPeriodList": {
		Mandatory:  2,
		Deprecated: 1,
		Error:      2,
		Unreached:  true,
	},
	"BillingReading": {
		Mandatory:  2,
		Deprecated: 2,
		Error:      1,
		Unreached:  true,
	},
	"BillingReadingList": {
		Mandatory:  2,
		Deprecated: 1,
		Error:      2,
		Unreached:  true,
	},
	"BillingReadingSet": {
		Mandatory:  2,
		Deprecated: 1,
		Error:      2,
		Unreached:  true,
	},
	"BillingReadingSetList": {
		Mandatory:  2,
		Deprecated: 1,
		Error:      2,
		Unreached:  true,
	},
	"ConsumptionTariffInterval": {
		Mandatory:  2,
		Deprecated: 1,
		Error:      2,
		Unreached:  true,
	},
	"ConsumptionTariffIntervalList": {
		Mandatory:  2,
		Deprecated: 1,
		Error:      2,
		Unreached:  true,
	},
	"CreditRegister": {
		Mandatory: 2,
		Optional:  1,
		Error:     2,
		Unreached: true,
	},
	"CreditRegisterList": {
		Mandatory: 2,
		Optional:  1,
		Error:     2,
		Unreached: true,
	},
	"CurrentDERProgram": {
		Optional:               1,
		RestrictedNotForbidden: 1,
	},
	"CustomerAccount": {
		Mandatory:  2,
		Deprecated: 1,
		Error:      2,
		Unreached:  true,
	},
	"CustomerAccountList": {
		Mandatory:  2,
		Deprecated: 1,
		Error:      2,
		Unreached:  true,
	},
	"CustomerAgreement": {
		Mandatory:  2,
		Deprecated: 1,
		Error:      2,
		Unreached:  true,
	},
	"CustomerAgreementList": {
		Mandatory:  2,
		Deprecated: 1,
		Error:      2,
		Unreached:  true,
	},
	"DER": {
		Optional: 1,
	},
	"DERControl": {
		Optional:               1,
		Error:                  2,
		RestrictedNotForbidden: 1,
	},
	"DERControlList": {
		Optional:               1,
		Error:                  2,
		RestrictedNotForbidden: 1,
	},
	"DERControlResponse": {
		Optional:  3,
		Unreached: true,
	},
	"DERCurve": {
		Optional:               1,
		Error:                  2,
		RestrictedNotForbidden: 1,
	},
	"DERCurveList": {
		Optional:               1,
		Error:                  2,
		RestrictedNotForbidden: 1,
	},
	"DERList": {
		Optional: 1,
	},
	"DERProgram": {
		Optional:               1,
		Error:                  2,
		RestrictedNotForbidden: 1,
	},
	"DERProgramList": {
		Optional:               1,
		Error:                  2,
		RestrictedNotForbidden: 1,
	},
	"DefaultDERControl": {
		Optional:               1,
		Error:                  2,
		RestrictedNotForbidden: 1,
	},
	"DemandResponseProgram": {
		Mandatory:  2,
		Deprecated: 1,
		Error:      2,
		Unreached:  true,
	},
	"DemandResponseProgramList": {
		Mandatory:  2,
		Deprecated: 1,
		Error:      2,
		Unreached:  true,
	},
	"DeviceInformation": {
		Mandatory: 1,
		Optional:  1,
		Error:     1,
	},
	"DrResponse": {
		Optional:  3,
		Unreached: true,
	},
	"EndDeviceControl": {
		Mandatory:  2,
		Deprecated: 1,
		Error:      2,
		Unreached:  true,
	},
	"EndDeviceControlList": {
		Mandatory:  2,
		Deprecated: 1,
		Error:      2,
		Unreached:  true,
	},
	"File": {
		Mandatory:  2,
		Deprecated: 2,
		Error:      1,
		Unreached:  true,
	},
	"FileList": {
		Mandatory:  2,
		Deprecated: 1,
		Error:      2,
		Unreached:  true,
	},
	"FileStatus": {
		Mandatory: 3,
		Error:     2,
		Unreached: true,
	},
	"FlowReservationRequest": {
		Mandatory: 1,
		Optional:  1,
	},
	"FlowReservationRequestList": {
		Mandatory: 1,
	},
	"FlowReservationResponseResponse": {
		Optional:  3,
		Unreached: true,
	},
	"HistoricalReading": {
		Mandatory:  2,
		Deprecated: 1,
		Error:      2,
		Unreached:  true,
	},
	"HistoricalReadingList": {
		Mandatory:  2,
		Deprecated: 1,
		Error:      2,
		Unreached:  true,
	},
	"IPAddr": {
		Mandatory: 2,
		Optional:  2,
		Error:     1,
		Unreached: true,
	},
	"IPAddrList": {
		Mandatory: 2,
		Optional:  1,
		Error:     2,
		Unreached: true,
	},
	"IPInterface": {
		Mandatory: 2,
		Optional:  2,
		Error:     1,
		Unreached: true,
	},
	"IPInterfaceList": {
		Mandatory: 2,
		Optional:  1,
		Error:     2,
		Unreached: true,
	},
	"LLInterface": {
		Mandatory: 2,
		Optional:  2,
		Error:     1,
		Unreached: true,
	},
	"LLInterfaceList": {
		Mandatory: 2,
		Optional:  1,
		Error:     2,
		Unreached: true,
	},
	"LoadShedAvailability": {
		Mandatory: 3,
		Optional:  1,
		Error:     1,
		Unreached: true,
	},
	"LoadShedAvailabilityList": {
		Mandatory: 3,
		Error:     2,
		Unreached: true,
	},
	"MeterReading": {
		Mandatory: 0,
	},
	"MeterReadingList": {
		Optional:               1,
		RestrictedNotForbidden: 1,
	},
	"MirrorUsagePoint": {
		Mandatory: 1,
	},
	"MirrorUsagePointList": {
		Mandatory: 1,
	},
	"Neighbor": {
		Mandatory: 2,
		Optional:  2,
		Error:     1,
		Unreached: true,
	},
	"NeighborList": {
		Mandatory: 2,
		Optional:  1,
		Error:     2,
		Unreached: true,
	},
	"Notification": {
		Deprecated: 2,
		Error:      3,
	},
	"NotificationList": {
		Mandatory: 0,
		Unlinked:  true,
	},
	"PrepayOperationStatus": {
		Mandatory:  2,
		Deprecated: 1,
		Error:      2,
		Unreached:  true,
	},
	"Prepayment": {
		Mandatory:  2,
		Deprecated: 1,
		Error:      2,
		Unreached:  true,
	},
	"PrepaymentList": {
		Mandatory:  2,
		Deprecated: 1,
		Error:      2,
		Unreached:  true,
	},
	"PriceResponse": {
		Optional:  3,
		Unreached: true,
	},
	"PriceResponseCfg": {
		Mandatory: 4,
		Error:     1,
		Unreached: true,
	},
	"PriceResponseCfgList": {
		Mandatory: 3,
		Error:     2,
		Unreached: true,
	},
	"ProjectionReading": {
		Mandatory:  2,
		Deprecated: 2,
		Error:      1,
		Unreached:  true,
	},
	"ProjectionReadingList": {
		Mandatory:  2,
		Deprecated: 1,
		Error:      2,
		Unreached:  true,
	},
	"RPLInstance": {
		Mandatory: 2,
		Optional:  2,
		Error:     1,
		Unreached: true,
	},
	"RPLInstanceList": {
		Mandatory: 2,
		Optional:  1,
		Error:     2,
		Unreached: true,
	},
	"RPLSourceRoutes": {
		Mandatory: 2,
		Optional:  2,
		Error:     1,
		Unreached: true,
	},
	"RPLSourceRoutesList": {
		Mandatory: 2,
		Optional:  1,
		Error:     2,
		Unreached: true,
	},
	"RateComponent": {
		Mandatory:  2,
		Deprecated: 1,
		Error:      2,
		Unreached:  true,
	},
	"RateComponentList": {
		Mandatory:  2,
		Deprecated: 1,
		Error:      2,
		Unreached:  true,
	},
	"Reading": {
		Mandatory: 0,
	},
	"ReadingList": {
		Optional:               1,
		RestrictedNotForbidden: 1,
	},
	"ReadingSet": {
		Mandatory: 0,
	},
	"ReadingSetList": {
		Optional:               1,
		RestrictedNotForbidden: 1,
	},
	"ReadingType": {
		Mandatory: 0,
	},
	"Response": {
		Optional: 1,
	},
	"ServiceSupplier": {
		Mandatory:  2,
		Deprecated: 1,
		Error:      2,
		Unreached:  true,
	},
	"Subscription": {
		Mandatory: 4,
		Unreached: true,
	},
	"SubscriptionList": {
		Mandatory: 3,
		Unreached: true,
	},
	"SupplyInterruptionOverride": {
		Mandatory:  2,
		Deprecated: 2,
		Error:      1,
		Unreached:  true,
	},
	"SupplyInterruptionOverrideList": {
		Mandatory:  2,
		Deprecated: 1,
		Error:      2,
		Unreached:  true,
	},
	"SupportedLocale": {
		Mandatory: 4,
		Error:     1,
		Unreached: true,
	},
	"SupportedLocaleList": {
		Mandatory: 3,
		Error:     2,
		Unreached: true,
	},
	"TargetReading": {
		Mandatory:  2,
		Deprecated: 1,
		Error:      2,
		Unreached:  true,
	},
	"TargetReadingList": {
		Mandatory:  2,
		Deprecated: 1,
		Error:      2,
		Unreached:  true,
	},
	"TariffProfile": {
		Mandatory:  2,
		Deprecated: 1,
		Error:      2,
		Unreached:  true,
	},
	"TariffProfileList": {
		Mandatory:  2,
		Deprecated: 1,
		Error:      2,
		Unreached:  true,
	},
	"TextMessage": {
		Mandatory: 2,
		Error:     1,
		Unreached: true,
	},
	"TextMessageList": {
		Deprecated: 1,
	},
	"TextResponse": {
		Optional:  3,
		Unreached: true,
	},
	"TimeTariffInterval": {
		Mandatory:  2,
		Deprecated: 1,
		Error:      2,
		Unreached:  true,
	},
	"TimeTariffIntervalList": {
		Mandatory:  2,
		Deprecated: 1,
		Error:      2,
		Unreached:  true,
	},
	"UsagePoint": {
		Mandatory: 0,
	},
}

// EnvArtifact names an optional file to write the full row-by-row sweep to.
//
// It is opt-in and defaults to off. The output is a record of what THIS server
// did, not a copy of the standard, but it is still verbose enough that writing
// it unasked into a repo working tree would be a nuisance.
const EnvArtifact = "SEP2_WADL_SWEEP_OUT"

// bootTarget boots a real server over real mTLS and returns it as a sweep
// target.
//
// The server is booted through csiptest, the same helper the CSIP suite uses,
// so this harness measures the same server the rest of the conformance tests
// measure. It listens on a kernel-assigned loopback port and is torn down by
// t.Cleanup, so nothing is left running and no port is chosen by hand.
func bootTarget(t *testing.T, links *wadl.LinkSchema) *wadl.Target {
	t.Helper()

	srv := csiptest.BootServer(t, csiptest.WithDeviceLog())
	if len(srv.MountedPatterns) == 0 {
		t.Fatal("the booted server reported no mounted patterns, so the route cross-check would be vacuous")
	}
	return &wadl.Target{
		BaseURL:         srv.BaseURL,
		Client:          srv.HTTPClient(),
		MountedPatterns: srv.MountedPatterns,
		Links:           links,
		Stores:          srv.Stores,
		ManagerClient:   srv.NewManagerClient(t),
		ManagedDeviceID: csiptest.ManagedDeviceID,
	}
}

// TestWADLConformanceSweep walks the booted server against every method the
// normative WADL declares.
//
// What this test GATES on is deliberately narrow: the integrity of the sweep
// itself, plus failures that are wrong under any reading of the standard (a
// 5xx, a transport failure). It does NOT fail on every non-conformant row,
// because the conformance gap is a known, tracked body of work and a test that
// is red by design gets muted. The gap is reported in full instead, and the
// ratchet below keeps it from silently widening.
func TestWADLConformanceSweep(t *testing.T) {
	model := wadl.MustLoadModel(t)
	target := bootTarget(t, wadl.SchemaForTest(t))

	res := wadl.Sweep(target, model)

	for _, line := range res.Summary() {
		t.Log(line)
	}

	if path := os.Getenv(EnvArtifact); path != "" {
		writeArtifact(t, path, res)
	}

	// 1. Harness integrity. The two unrouted discriminators are independent
	//    on purpose; when they disagree, the harness or a middleware is
	//    wrong, and every routing verdict in the run is suspect. This is
	//    checked BEFORE any conformance number is believed.
	if bad := res.RoutingDisagreements(); len(bad) > 0 {
		for _, row := range bad {
			t.Errorf("routing disagreement on %s %s: wire verdict %q but the router's own enumeration says mounted=%v",
				row.Method, row.Path, row.Verdict, *row.MountedByRouter)
		}
		t.Fatal("the wire evidence and the router's route enumeration disagree, so the routing verdicts in this run cannot be trusted")
	}

	// 1b. The crawl itself. A walk from /dcap that reaches no resource has
	//     found nothing, and every row after it measures sample paths only.
	if res.CrawlReached == 0 || res.LinksFollowed() == 0 {
		t.Fatalf("the link crawl from /dcap followed %d links and reached %d WADL resources, so the sweep scored sample paths and not what a client finds",
			res.LinksFollowed(), res.CrawlReached)
	}

	// 2. Seed failures. A refused seed leaves the subtree hanging off it
	//    unwalked, which shrinks the sweep without failing anything else.
	for _, row := range res.Filter(func(r wadl.Row) bool {
		return r.Phase == wadl.PhaseSeed && r.Verdict != wadl.VerdictSeeded
	}) {
		t.Errorf("seed failed: %s %s (%s) returned status %d, verdict %q: %s",
			row.Method, row.Path, row.Element, row.Status, row.Verdict, row.TransportError)
	}

	// 2b. Unseeded probes. A routed row that answers 404 means the sweep
	//     probed a resource nothing created, so it tested the store and not
	//     the handler. Every probed resource has a seed in seedSet; a new
	//     fixture goes there.
	for _, row := range res.Filter(func(r wadl.Row) bool { return r.Verdict == wadl.VerdictRoutedNotFound }) {
		t.Errorf("routed but not found: %s %s (WADL path %s, mode %s) has no seeded instance",
			row.Method, row.Path, row.WADLPath, row.Mode)
	}

	// 3. Transport failures. A request that never completed is not a
	//    conformance observation, it is a broken run.
	for _, row := range res.Filter(func(r wadl.Row) bool { return r.TransportError != "" }) {
		t.Errorf("transport failure on %s %s: %s", row.Method, row.Path, row.TransportError)
	}

	// 4. Server errors. A 5xx is a defect under every wx:mode, including
	//    Error mode, where the standard asks for a 400 or a 405 and never
	//    for a panic.
	for _, row := range res.Filter(func(r wadl.Row) bool { return r.Status >= 500 }) {
		t.Errorf("server error: %s %s returned %d (WADL path %s, mode %s)",
			row.Method, row.Path, row.Status, row.WADLPath, row.Mode)
	}
}

// TestMethodModePolicyRatchet pins the gap per WADL resource: the methods outside
// the method-mode policy, the resources the crawl misses, and the manager-only
// writes the device is not refused. With the schema each resource must equal its
// expectedGaps entry; without it the sweep is strict, so the measure is only a
// ceiling and the not-reached counts are left out, but a resource the crawl misses
// that no entry marks Unreached or Unlinked still fails.
func TestMethodModePolicyRatchet(t *testing.T) {
	model := wadl.MustLoadModel(t)
	links := wadl.SchemaForTest(t)
	res := wadl.Sweep(bootTarget(t, links), model)

	outside := res.OutsidePolicy(model)
	for _, line := range wadl.PolicyBreakdown(outside) {
		t.Log("outside policy: " + line)
	}
	t.Logf("methods outside the method-mode policy: %d of %d declared", len(outside), len(model.Methods))

	exact := links != nil
	measured, err := measureGaps(res, links, outside)
	if err != nil {
		t.Fatal(err)
	}
	if !exact {
		for _, msg := range lostLinks(res, expectedGaps) {
			t.Error(msg)
		}
	}
	if exact {
		for _, msg := range unlinkedMarks(expectedGaps, links) {
			t.Error(msg)
		}
	}
	for _, msg := range diffGaps(measured, expectedGaps, exact) {
		t.Error(msg)
	}
}

// TestRestrictedWritesForbidDevice sweeps with a manager certificate and shows what
// an ordinary device is answered on each write the server restricts to a manager.
// The manager's own answer is scored by the policy ratchet like any other row, and
// the device's by the per-resource RestrictedNotForbidden count.
func TestRestrictedWritesForbidDevice(t *testing.T) {
	model := wadl.MustLoadModel(t)
	res := wadl.Sweep(bootTarget(t, wadl.SchemaForTest(t)), model)

	asManager := len(res.Filter(func(r wadl.Row) bool { return r.AsManager }))
	probed := res.DeviceProbed()
	if asManager == 0 || asManager != probed {
		t.Fatalf("%d restricted writes were made as the manager and %d probed as the device: "+
			"the two must pair, or the 403 assertion is vacuous", asManager, probed)
	}

	t.Logf("restricted writes: %d declared, %d probed as manager and device, %d of those without a device 403",
		wadl.RestrictedWrites(model), probed, res.DeviceProbedNotForbidden())
}

// measureGaps splits a sweep into one expectedGap per resource that has any gap.
// Without a schema (links nil) resources the crawl missed are left out of every
// field: the strict sweep cannot tell an unlinked resource from a missed one, so
// those counts are not comparable. lostLinks covers that case. An outside row
// with a mode the switch does not know is an error, never dropped.
func measureGaps(res *wadl.Results, links *wadl.LinkSchema, outside []wadl.PolicyRow) (map[string]expectedGap, error) {
	exact := links != nil
	out := map[string]expectedGap{}
	for _, row := range outside {
		if row.Verdict == wadl.VerdictNotReached && !exact {
			continue
		}
		g := out[row.ResourceID]
		switch row.Mode {
		case wadl.ModeMandatory:
			g.Mandatory++
		case wadl.ModeOptional:
			g.Optional++
		case wadl.ModeDeprecated:
			g.Deprecated++
		case wadl.ModeError:
			g.Error++
		default:
			return nil, fmt.Errorf("resource %s has an outside-policy row in mode %q, which has no field in expectedGap",
				row.ResourceID, row.Mode)
		}
		out[row.ResourceID] = g
	}
	if exact {
		for _, row := range res.NotReachedRows() {
			g := out[row.ResourceID]
			g.Unreached = true
			out[row.ResourceID] = g
		}
	}
	for _, row := range res.DeviceProbedNotForbiddenRows() {
		g := out[row.ResourceID]
		g.RestrictedNotForbidden++
		out[row.ResourceID] = g
	}
	return out, nil
}

// unlinkedMarks reports each pinned Unlinked entry whose resource the schema does
// name a link for, so a mark cannot silence lostLinks for a resource that is
// linked.
func unlinkedMarks(pinned map[string]expectedGap, links *wadl.LinkSchema) []string {
	var msgs []string
	for id, g := range pinned {
		if g.Unlinked && links.Names(id) {
			msgs = append(msgs, fmt.Sprintf("resource %s: pinned Unlinked, but the schema names a link for it", id))
		}
	}
	sort.Strings(msgs)
	return msgs
}

// lostLinks reports each resource the crawl missed that its pinned entry does not
// mark Unreached or Unlinked. It reads only the entry marks, so it holds without
// the schema: a link the server stops emitting turns a reached resource into one
// of these, whatever other gaps its entry pins.
func lostLinks(res *wadl.Results, pinned map[string]expectedGap) []string {
	var msgs []string
	for _, row := range res.NotReachedRows() {
		if g := pinned[row.ResourceID]; g.Unreached || g.Unlinked {
			continue
		}
		msgs = append(msgs, fmt.Sprintf("resource %s: the crawl did not reach it and its entry does not mark it Unreached or Unlinked: a link the server used to emit is gone",
			row.ResourceID))
	}
	return msgs
}

// diffGaps compares a measurement to the pinned entries, one message per resource
// that differs, each carrying the entry to write. A resource with no entry is
// pinned at all zero. Unlinked is not measured, so it is taken from the entry. With exact false a measurement may be below its entry, and
// Unreached is not compared.
func diffGaps(measured, pinned map[string]expectedGap, exact bool) []string {
	ids := map[string]bool{}
	for id := range measured {
		ids[id] = true
	}
	for id := range pinned {
		ids[id] = true
	}
	sorted := make([]string, 0, len(ids))
	for id := range ids {
		sorted = append(sorted, id)
	}
	sort.Strings(sorted)

	var msgs []string
	for _, id := range sorted {
		got, want := measured[id], pinned[id]
		got.Unlinked = want.Unlinked
		if exact && got == want {
			continue
		}
		if !exact && !gapExceeds(got, want) {
			continue
		}
		msgs = append(msgs, fmt.Sprintf("resource %s: measured %+v but pinned %+v; write this entry in expectedGaps:\n%s",
			id, got, want, formatGapEntry(id, got)))
	}
	return msgs
}

// gapExceeds reports whether any counted field of got is above want's.
func gapExceeds(got, want expectedGap) bool {
	return got.Mandatory > want.Mandatory || got.Optional > want.Optional ||
		got.Deprecated > want.Deprecated || got.Error > want.Error ||
		got.RestrictedNotForbidden > want.RestrictedNotForbidden
}

// formatGapEntry renders the multi-line, gofmt-aligned entry that pins g. An
// all-zero gap is written as an explicit zero field, never as a deleted entry.
func formatGapEntry(id string, g expectedGap) string {
	type field struct{ name, val string }
	var fields []field
	add := func(name string, n int) {
		if n != 0 {
			fields = append(fields, field{name, strconv.Itoa(n)})
		}
	}
	add("Mandatory", g.Mandatory)
	add("Optional", g.Optional)
	add("Deprecated", g.Deprecated)
	add("Error", g.Error)
	if g.Unreached {
		fields = append(fields, field{"Unreached", "true"})
	}
	if g.Unlinked {
		fields = append(fields, field{"Unlinked", "true"})
	}
	add("RestrictedNotForbidden", g.RestrictedNotForbidden)
	if len(fields) == 0 {
		fields = []field{{"Mandatory", "0"}}
	}
	width := 0
	for _, f := range fields {
		width = max(width, len(f.name))
	}
	var b strings.Builder
	fmt.Fprintf(&b, "\t%q: {\n", id)
	for _, f := range fields {
		fmt.Fprintf(&b, "\t\t%-*s %s,\n", width+1, f.name+":", f.val)
	}
	b.WriteString("\t},")
	return b.String()
}

// TestDiffGaps shows the ratchet fails on a rise, a fall and an unpinned gap, and
// names the entry to write, without needing the WADL.
func TestDiffGaps(t *testing.T) {
	pinned := map[string]expectedGap{"A": {Mandatory: 2, Unreached: true}}
	tests := []struct {
		name     string
		measured map[string]expectedGap
		exact    bool
		want     int
		entry    string
	}{
		{"equal", map[string]expectedGap{"A": {Mandatory: 2, Unreached: true}}, true, 0, ""},
		{"rise", map[string]expectedGap{"A": {Mandatory: 3, Unreached: true}}, true, 1, "\t\tMandatory: 3,"},
		{"fall is unpinned", map[string]expectedGap{"A": {Mandatory: 1, Unreached: true}}, true, 1, "\t\tMandatory: 1,"},
		{"unreached cleared", map[string]expectedGap{"A": {Mandatory: 2}}, true, 1, "\t\tMandatory: 2,"},
		{"gap with no entry", map[string]expectedGap{"A": {Mandatory: 2, Unreached: true}, "B": {Error: 1, RestrictedNotForbidden: 1}}, true, 1,
			"\t\tError:                  1,\n\t\tRestrictedNotForbidden: 1,"},
		{"fix to zero", map[string]expectedGap{}, true, 1, "\t\tMandatory: 0,"},
		{"fall passes without schema", map[string]expectedGap{"A": {Mandatory: 1}}, false, 0, ""},
		{"rise fails without schema", map[string]expectedGap{"A": {Mandatory: 3}}, false, 1, "Mandatory: 3,"},
		{"unreached ignored without schema", map[string]expectedGap{"A": {Mandatory: 2}}, false, 0, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := diffGaps(tc.measured, pinned, tc.exact)
			if len(got) != tc.want {
				t.Fatalf("diffGaps() = %d messages %q, want %d", len(got), got, tc.want)
			}
			if tc.want == 1 && !strings.Contains(got[0], tc.entry) {
				t.Errorf("message %q does not carry the entry %q", got[0], tc.entry)
			}
		})
	}
}

// writeArtifact records the sweep row by row as JSON Lines.
func writeArtifact(t *testing.T, path string, res *wadl.Results) {
	t.Helper()

	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create %s=%s: %v", EnvArtifact, path, err)
	}
	defer func() {
		if cerr := f.Close(); cerr != nil {
			t.Errorf("close sweep artifact %s: %v", path, cerr)
		}
	}()

	enc := json.NewEncoder(f)
	for _, row := range res.Rows {
		if err := enc.Encode(row); err != nil {
			t.Fatalf("write sweep artifact %s: %v", path, err)
		}
	}
	t.Logf("sweep artifact written: %s (%d rows)", path, len(res.Rows))
}

// TestLostLinksWithoutSchema shows the no-schema check fails on a missed resource
// no entry accounts for, whatever gaps its entry pins.
func TestLostLinksWithoutSchema(t *testing.T) {
	pinned := map[string]expectedGap{
		"Missed":   {Mandatory: 2, Unreached: true},
		"Unlinked": {Mandatory: 0, Unlinked: true},
		"Reached":  {Error: 3},
	}
	row := func(id string) wadl.Row {
		return wadl.Row{Phase: wadl.PhaseWADL, ResourceID: id, Verdict: wadl.VerdictNotReached}
	}
	tests := []struct {
		name string
		rows []wadl.Row
		want []string
	}{
		{"pinned missed and unlinked pass", []wadl.Row{row("Missed"), row("Unlinked")}, nil},
		{"resource with no entry", []wadl.Row{row("Time")}, []string{"Time"}},
		{"reached resource with a gap entry", []wadl.Row{row("Reached")}, []string{"Reached"}},
		{"nothing missed", nil, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var ids []string
			for _, msg := range lostLinks(&wadl.Results{Rows: tc.rows}, pinned) {
				for _, id := range tc.want {
					if strings.Contains(msg, "resource "+id+":") {
						ids = append(ids, id)
					}
				}
			}
			if len(ids) != len(tc.want) {
				t.Fatalf("lostLinks flagged %v, want %v", ids, tc.want)
			}
		})
	}
}

// TestMeasureGapsRejectsAnUnknownMode shows an outside row whose mode the switch
// does not know fails the measurement instead of being dropped.
func TestMeasureGapsRejectsAnUnknownMode(t *testing.T) {
	outside := []wadl.PolicyRow{{ResourceID: "A", Mode: wadl.Mode("X"), Verdict: wadl.VerdictMethodRefused}}
	if _, err := measureGaps(&wadl.Results{}, nil, outside); err == nil {
		t.Fatal("measureGaps accepted a row with an unknown mode")
	}
}

// TestUnlinkedMarksCheckedAgainstSchema shows a pinned Unlinked mark fails for a
// resource the schema names a link for.
func TestUnlinkedMarksCheckedAgainstSchema(t *testing.T) {
	links := wadl.SchemaForTest(t)
	if links == nil {
		t.Skip("no sep.xsd: the marks are checked against the schema")
	}
	if msgs := unlinkedMarks(map[string]expectedGap{"EndDevice": {Unlinked: true}}, links); len(msgs) != 1 {
		t.Errorf("unlinkedMarks flagged %q for a linked resource, want one message", msgs)
	}
	if msgs := unlinkedMarks(map[string]expectedGap{"NotificationList": {Unlinked: true}}, links); len(msgs) != 0 {
		t.Errorf("unlinkedMarks flagged %q for the unlinked NotificationList", msgs)
	}
}
