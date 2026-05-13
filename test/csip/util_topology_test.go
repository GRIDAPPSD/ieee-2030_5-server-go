// IEEE-089 — shared aggregator-topology helpers for UTIL-001..UTIL-004
// (CSIP V1.2 §9.1-§9.4) and the future AGG-001..012 wiring (IEEE-090).
//
// The fixture at test/csip/fixtures/aggregator-topology.yaml is "the
// heaviest fixture in the matrix" per Noor's V1.2 coverage matrix
// Section 2: 5 EndDevices (1 aggregator EDFI + 4 managed inverters
// EDA1/EDA2/EDB1/EDB2), a 4-level FSA chain per managed inverter
// (SY → FDx → SPxx → DEV), and 7 distinct node-level DERPrograms
// across the topology.
//
// This file is harness scaffolding shared across UTIL-* (and soon
// AGG-*) test files. It is intentionally narrow: it knows the YAML
// layout of aggregator-topology.yaml at the constant level (the
// EndDevice IDs, the FSA/DERP IDs, the topology metadata) so each
// procedure test reads as "boot, walk, assert" rather than rebuilding
// the topology constants inline.
//
// Pike's rule for the file: every constant here is a contract with the
// fixture YAML. A fixture edit that renames or renumbers anything
// forces a matching edit here; the YAML and this file are two halves
// of the same boundary.

package csip_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-go/test/csip/csiptest"
)

// aggregatorTopologyFixture is the path callers pass to csiptest.Load.
// Relative to the test/csip working directory.
var aggregatorTopologyFixture = filepath.Join("fixtures", "aggregator-topology.yaml")

// Aggregator topology — EndDevice IDs. These are the path segments
// downstream tests use to assemble URLs like /edev/1/fsa/2/derp/2/derc.
const (
	aggEDFI = "0" // EDFI — the aggregator itself.
	aggEDA1 = "1" // EDA1 — managed inverter under FDA/SPA1.
	aggEDA2 = "2" // EDA2 — managed inverter under FDA/SPA2.
	aggEDB1 = "3" // EDB1 — managed inverter under FDB/SPB1.
	aggEDB2 = "4" // EDB2 — managed inverter under FDB/SPB2.
)

// aggManagedInverters is the canonical order UTIL-002/003/004 iterate
// over. Exposed as a slice so test bodies don't re-derive it.
var aggManagedInverters = []string{aggEDA1, aggEDA2, aggEDB1, aggEDB2}

// Aggregator topology — FSA IDs per managed inverter. Identical
// across inverters by design: the FSA tree (SY/FDx/SPxx/DEV) is the
// shared utility hierarchy and the store-key sort lines up with the
// priority chain.
const (
	aggFSAIDSY   = "0" // System level (highest priority — primacy 0).
	aggFSAIDFD   = "1" // Feeder level (primacy 1).
	aggFSAIDSP   = "2" // Service Point level (primacy 2).
	aggFSAIDDEV  = "3" // Device level (primacy 3, lowest priority).
	aggFSACount  = 4   // Per-inverter FSA count.
	aggDERPCount = 4   // Per-inverter DERProgram count.
)

// aggInverterFSAIDs is the per-inverter FSA ID list in store-key /
// priority-chain order. UTIL-001 iterates over this to assert the
// chain SY → FDx → SPxx → DEV.
//
//nolint:unused // referenced by future UTIL-NNN tests; retained for ordering reference
var aggInverterFSAIDs = []string{aggFSAIDSY, aggFSAIDFD, aggFSAIDSP, aggFSAIDDEV}

// aggInverterDescriptions maps the per-inverter FSA index to the
// description the fixture assigns. For SY/FDx the description is
// shared across inverters (the utility-side levels are common); for
// SPxx/DEV the description is per-inverter.
//
// inverterID → []descriptions in FSA store-key order.
var aggInverterDescriptions = map[string][]string{
	aggEDA1: {"SY", "FDA", "SPA1", "DEV-EDA1"},
	aggEDA2: {"SY", "FDA", "SPA2", "DEV-EDA2"},
	aggEDB1: {"SY", "FDB", "SPB1", "DEV-EDB1"},
	aggEDB2: {"SY", "FDB", "SPB2", "DEV-EDB2"},
}

// bootAggregatorTopology seeds a fresh BootServer's stores from
// aggregator-topology.yaml and returns the booted server. Every UTIL-*
// test begins with this; AGG-* will too.
//
// The helper does not customise the BootServer — it accepts variadic
// BootOptions so callers needing CCM mode, an external client cert, or
// a non-default ServerConfig can supply them.
func bootAggregatorTopology(t *testing.T, opts ...csiptest.BootOption) *csiptest.BootedServer {
	t.Helper()
	ctx := context.Background()

	stores := csiptest.NewFreshStores()
	target := &csiptest.Target{
		EndDevices:         stores.EndDevices,
		FSAs:               stores.FSAs,
		DERPrograms:        stores.DERPrograms.ScopedStore, // IEEE-097 wrapper; IEEE-104.
		DERControls:        stores.DERControls,
		DefaultDERControls: stores.DefaultDERControls,
		DERCurves:          stores.DERCurves,
	}
	if err := csiptest.Load(ctx, target, aggregatorTopologyFixture); err != nil {
		t.Fatalf("load aggregator topology fixture %s: %v", aggregatorTopologyFixture, err)
	}

	allOpts := append([]csiptest.BootOption{csiptest.WithStores(stores)}, opts...)
	return csiptest.BootServer(t, allOpts...)
}
