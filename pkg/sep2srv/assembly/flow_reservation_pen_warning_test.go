package assembly_test

import (
	"strings"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/assembly"
)

// TestBuildProtocolRouter_WarnsWhenFlowReservationPENUnset is fix round 2's
// "never fail a POST for a missing PEN" criterion: an unset PEN still mounts
// every flow reservation route (a POST is served, just with a non-conformant
// mRID), and the operator finds out via one startup log line rather than a
// per-request failure. captureLog is denial_log_test.go's helper; this test
// does not run in parallel with the others in this file, for the same
// process-wide-log-swap reason they do not.
func TestBuildProtocolRouter_WarnsWhenFlowReservationPENUnset(t *testing.T) {
	const marker = "no PEN configured for FlowReservationResponse mRIDs"

	// Control first, on one buffer: a configured PEN must not warn.
	buf := captureLog(t)
	pen := uint32(0x40732001)
	assembly.BuildProtocolRouter(assembly.RouterConfig{PEN: &pen}, testStores(), testAuthPolicy(), "serverSFDI", "serverLFDI", nil)
	if strings.Contains(buf.String(), marker) {
		t.Fatalf("control: a configured PEN logged %q; log=%q", marker, buf.String())
	}

	// Same buffer, a second boot with PEN unset: the warning must now appear,
	// and appear exactly once for this boot (the buffer only ever held the
	// control's output beforehand, so one occurrence here is one occurrence
	// added by this call).
	assembly.BuildProtocolRouter(assembly.RouterConfig{}, testStores(), testAuthPolicy(), "serverSFDI", "serverLFDI", nil)
	logged := buf.String()
	if got := strings.Count(logged, marker); got != 1 {
		t.Fatalf("warning occurrences after the unset boot = %d, want exactly 1; log=%q", got, logged)
	}

	// 0 is IANA-reserved; internal/dercontrol.Config already treats it as
	// unset, and this must warn the same way, not silently accept it as a
	// real PEN.
	zero := uint32(0)
	assembly.BuildProtocolRouter(assembly.RouterConfig{PEN: &zero}, testStores(), testAuthPolicy(), "serverSFDI", "serverLFDI", nil)
	if got := strings.Count(buf.String(), marker); got != 2 {
		t.Errorf("warning occurrences after the zero-PEN boot = %d, want exactly 2; log=%q", got, buf.String())
	}
}
