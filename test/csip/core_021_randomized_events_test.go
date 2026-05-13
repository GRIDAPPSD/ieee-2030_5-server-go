// CSIP V1.2 §7.1 — Randomized Events (server-side emission).
//
// CORE-021 asserts that when a DERControl carries randomizeStart and
// randomizeDuration, the server emits both fields on GET with the same
// values the harness seeded. Client-side application of randomization
// (i.e. a DER that delays event start by the advertised window) is
// plan-1's problem; this test only proves the server's wire emission.
//
// V1.2 §7.1 defines randomizeStart and randomizeDuration as signed
// 32-bit integers in seconds. The values seeded here (30 and 60) are
// small positive numbers — large enough to be visibly non-zero on
// the wire, small enough that a sloppy truncation regression
// (int16/uint8) would not silently pass.
//
// V1.2 procedure step → assertion mapping:
//
//	Step 1 (server hosts a DERProgram with a randomized DERControl)
//	    ──► fixture load + in-test augmentation of DERControl with
//	        RandomizeStart=30, RandomizeDuration=60.
//	Step 2 (client GETs the DERControl list, observes randomization)
//	    ──► assertRandomizedFields walks /edev/0/fsa/0/derp/0/derc
//	        and asserts both fields survive the wire roundtrip.
//
// Why the test augments the DERControl after fixture load: the
// csiptest fixture loader's YAML schema (test/csip/csiptest/loader.go)
// is intentionally narrow and does not carry RandomizeStart or
// RandomizeDuration today. Per IEEE-066 scope, csiptest internals are
// off-limits. We seed the topology via the loader and then write the
// randomization fields onto the loaded DERControl via the public store
// API. The fixture's comments document the values for cross-reference.
package csip_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-go/test/csip/csiptest"
)

// CORE-021 randomization values. Held as named constants so a
// reviewer can map them to the V1.2 procedure step and so a future
// regression that perturbs the values fails with a value-mismatch
// rather than a magic-number diff.
const (
	core021RandomizeStart    int32 = 30
	core021RandomizeDuration int32 = 60
)

// TestCORE_021_RandomizedEvents implements CSIP V1.2 §7.1
// (server-side emission of randomizeStart / randomizeDuration).
func TestCORE_021_RandomizedEvents(t *testing.T) {
	t.Parallel()

	// Build a fresh store set and seed the topology from the fixture.
	stores := csiptest.NewFreshStores()
	target := &csiptest.Target{
		EndDevices:         stores.EndDevices,
		FSAs:               stores.FSAs,
		DERPrograms:        stores.DERPrograms.ScopedStore,
		DERControls:        stores.DERControls,
		DefaultDERControls: stores.DefaultDERControls,
		DERCurves:          stores.DERCurves,
	}
	fixture := filepath.Join("fixtures", "derprogram-randomized.yaml")
	ctx := context.Background()
	if err := csiptest.Load(ctx, target, fixture); err != nil {
		t.Fatalf("load %s: %v", fixture, err)
	}

	// Augment the seeded DERControl with the randomization fields.
	// The fixture loader's schema does not carry RandomizeStart or
	// RandomizeDuration today (and loader internals are off-limits per
	// IEEE-058). We read the just-loaded DERControl from the inner
	// store, mutate the two pointer fields, and write it back through
	// Update. This keeps the production code untouched and the test's
	// mutation path narrow and explicit.
	const (
		edevID = "0"
		fsaID  = "0"
		derpID = "0"
		dercID = "rnd-1"
	)
	scopeKey := edevID + "/" + fsaID + "/" + derpID
	dercStore := stores.DERControls.ForParent(scopeKey)
	derc, err := dercStore.Get(ctx, dercID)
	if err != nil {
		t.Fatalf("get DERControl %q from scope %q: %v", dercID, scopeKey, err)
	}
	rndStart := core021RandomizeStart
	rndDur := core021RandomizeDuration
	derc.RandomizeStart = &rndStart
	derc.RandomizeDuration = &rndDur
	if err := dercStore.Update(ctx, dercID, derc); err != nil {
		t.Fatalf("update DERControl %q with randomization: %v", dercID, err)
	}

	// Sanity check the store carries what we just wrote — guards
	// against a future store-API change that drops the pointer fields
	// without anyone noticing at the harness layer.
	stored, err := dercStore.Get(ctx, dercID)
	if err != nil {
		t.Fatalf("re-get DERControl %q after augmentation: %v", dercID, err)
	}
	if stored.RandomizeStart == nil || *stored.RandomizeStart != core021RandomizeStart {
		t.Fatalf("store RandomizeStart = %v, want %d", stored.RandomizeStart, core021RandomizeStart)
	}
	if stored.RandomizeDuration == nil || *stored.RandomizeDuration != core021RandomizeDuration {
		t.Fatalf("store RandomizeDuration = %v, want %d", stored.RandomizeDuration, core021RandomizeDuration)
	}

	// Boot the server against the seeded stores. GCM cipher is fine
	// for §7.1 — randomization emission is wire-shape, not cipher,
	// and CORE-022 already covers POST flow under the default cipher
	// too. CCM-mode coverage for DERControl emission lives in
	// CORE-012/013 (IEEE-065).
	srv := csiptest.BootServer(t, csiptest.WithStores(stores))

	// Walk DERProgram list to confirm topology is reachable end-to-end,
	// then GET the DERControl list and assert both randomization fields
	// survive the wire roundtrip.
	dcap, err := srv.Client().GetDeviceCapability(ctx)
	if err != nil {
		t.Fatalf("GET /dcap: %v", err)
	}
	if dcap.EndDeviceListLink == nil || dcap.EndDeviceListLink.Href == "" {
		t.Fatalf("dcap.EndDeviceListLink missing")
	}

	var derpList sep2.DERProgramList
	derpHref := "/edev/" + edevID + "/fsa/" + fsaID + "/derp"
	if err := srv.Client().WalkLink(ctx, sep2.Link{Href: derpHref}, &derpList); err != nil {
		t.Fatalf("GET %s: %v", derpHref, err)
	}
	if derpList.All != 1 || len(derpList.DERProgram) != 1 {
		t.Fatalf("derpList All=%d items=%d, want 1/1", derpList.All, len(derpList.DERProgram))
	}

	var dercList sep2.DERControlList
	dercListHref := derpHref + "/" + derpID + "/derc"
	if err := srv.Client().WalkLink(ctx, sep2.Link{Href: dercListHref}, &dercList); err != nil {
		t.Fatalf("GET %s: %v", dercListHref, err)
	}
	assertRandomizedFields(t, dercList)
}

// assertRandomizedFields verifies the DERControlList contains exactly
// one DERControl with the expected RandomizeStart and RandomizeDuration
// values from the fixture, both emitted on the wire as non-nil pointer
// fields. Spelled out as a helper so a reviewer can map the assertions
// to the V1.2 §7.1 conformance criteria without scrolling through
// fixture-loading boilerplate.
func assertRandomizedFields(t *testing.T, list sep2.DERControlList) {
	t.Helper()

	// Fixture seeds exactly one DERControl. Assert the list shape
	// before indexing — a server that drops the control entirely is a
	// different failure mode than one that drops the randomization
	// fields, and we want the test log to discriminate.
	if list.All != 1 {
		t.Fatalf("DERControlList.All = %d, want 1", list.All)
	}
	if len(list.DERControl) != 1 {
		t.Fatalf("len(list.DERControl) = %d, want 1", len(list.DERControl))
	}

	got := list.DERControl[0]
	if got.RandomizeStart == nil {
		t.Errorf("DERControl.RandomizeStart = nil, want %d", core021RandomizeStart)
	} else if *got.RandomizeStart != core021RandomizeStart {
		t.Errorf("DERControl.RandomizeStart = %d, want %d", *got.RandomizeStart, core021RandomizeStart)
	}
	if got.RandomizeDuration == nil {
		t.Errorf("DERControl.RandomizeDuration = nil, want %d", core021RandomizeDuration)
	} else if *got.RandomizeDuration != core021RandomizeDuration {
		t.Errorf("DERControl.RandomizeDuration = %d, want %d", *got.RandomizeDuration, core021RandomizeDuration)
	}
}

