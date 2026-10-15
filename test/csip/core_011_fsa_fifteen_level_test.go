// CSIP V1.2 section 6.4 - FSA Hierarchies, 15-level.
//
// Setup step 3 builds fifteen FunctionSetAssignments following the CSIP
// topology/non-topology recommendations. Procedure step 7 sorts the FSAs by
// mRID to find priority, step 8 GETs the chosen FSA's DERProgramList, and
// step 9 picks the highest priority program in it.
//
// The fixture is fifteen-level-fsa.yaml: one EndDevice, fifteen FSAs, one
// DERProgram per FSA with primacy 0..14. FSA ids are zero-padded ("00".."14")
// so store-key order is the priority chain. Each FSA lists only its own
// program, and across the walk the chain 0..14 is reached once each; the
// shared runFSAHierarchy in core_010_fsa_seven_level_test.go carries it.
//
// The default paging limit (paging.DefaultLimit = 10) does NOT fit the
// full 15 entries. The helper uses ?l=255 (paging.MaxLimit) on each
// list walk so this test exercises the same paging surface as CORE-004
// - a regression in ParseQuery would surface here as a short list.
package csip_test

import (
	"path/filepath"
	"testing"
)

// fifteenLevelDescriptions is the in-order list of FSA descriptions the
// fifteen-level-fsa.yaml fixture assigns to its 15 FSAs. Order matches
// the fixture's primacy chain 0..14: utility / horizon at the top
// (day-ahead, hour-ahead, RTO real-time, balancing area), through
// substation / feeder / lateral / zone, down to service-point / meter /
// aggregator / site / device-group / immediate at the bottom.
var fifteenLevelDescriptions = []string{
	"L0-day-ahead",
	"L1-hour-ahead",
	"L2-rto-realtime",
	"L3-balancing-area",
	"L4-substation",
	"L5-feeder",
	"L6-lateral",
	"L7-zone",
	"L8-transformer",
	"L9-service-point",
	"L10-meter",
	"L11-aggregator",
	"L12-site",
	"L13-device-group",
	"L14-immediate",
}

// TestCORE_011_FSAHierarchy15Level implements CSIP V1.2 section 6.4.
func TestCORE_011_FSAHierarchy15Level(t *testing.T) {
	t.Parallel()
	runFSAHierarchy(t, fsaHierarchyParams{
		fixture:      filepath.Join("fixtures", "fifteen-level-fsa.yaml"),
		descriptions: fifteenLevelDescriptions,
	})
}
