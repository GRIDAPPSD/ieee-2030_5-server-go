// CSIP V1.2 §6.4 — FSA Hierarchies, 15-level.
//
// CORE-011 extends CORE-010's 7-level FSA priority-chain procedure to
// the maximum 15-level chain described in V1.2 §6.4. The wire walk is
// identical — /dcap → /edev → first EndDevice →
// FunctionSetAssignmentsListLink → each FSA → DERProgramListLink → full
// primacy chain — with primacy values ascending 0..14.
//
// The fixture is fifteen-level-fsa.yaml (ships in #59): one
// EndDevice, fifteen FSAs scoped under it, one DERProgram per FSA with
// primacy 0..14 in ID order. FSA IDs are two-digit zero-padded
// ("00".."14") so lexicographic store-key sort matches numeric order
// across the 10-boundary — the test's wire-order assertion relies on
// that.
//
// Procedure step → assertion mapping (per V1.2 §6.4): identical shape
// to CORE-010 against the 15-level fixture. See the doc comment on
// core_010_fsa_seven_level_test.go for the per-step mapping; the
// shared runFSAHierarchy helper carries the implementation.
//
// Server behavior the test pins down — same as CORE-010:
//
//   - FSA list returned in store-key order. Fixture IDs "00".."14" sort
//     to match priority chain 0..14.
//   - DERProgramListLink is scoped by EndDevice only, so every FSA's
//     DERProgramList walk returns all 15 DERPrograms.
//
// The default paging limit (paging.DefaultLimit = 10) does NOT fit the
// full 15 entries. The helper uses ?l=255 (paging.MaxLimit) on each
// list walk so this test exercises the same paging surface as CORE-004
// — a regression in ParseQuery would surface here as a short list.
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

// TestCORE_011_FSAHierarchy15Level implements CSIP V1.2 §6.4.
func TestCORE_011_FSAHierarchy15Level(t *testing.T) {
	t.Parallel()
	runFSAHierarchy(t, fsaHierarchyParams{
		fixture:      filepath.Join("fixtures", "fifteen-level-fsa.yaml"),
		descriptions: fifteenLevelDescriptions,
	})
}
