package wadl

import (
	"net/http"
	"reflect"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/routefamily"
)

// TestInsidePolicy pins the policy per mode, including the two places it differs
// from the verdict alone: Deprecated accepts 405, and nothing but conformant
// satisfies Mandatory, Optional or Error.
func TestInsidePolicy(t *testing.T) {
	tests := []struct {
		name string
		row  Row
		want bool
	}{
		{"mandatory conformant", Row{Phase: PhaseWADL, Method: "GET", Mode: ModeMandatory, Verdict: VerdictConformant}, true},
		{"mandatory 405 is outside", Row{Phase: PhaseWADL, Method: "GET", Mode: ModeMandatory, Verdict: VerdictMethodRefused}, false},
		{"optional 405 is outside", Row{Phase: PhaseWADL, Method: "PUT", Mode: ModeOptional, Verdict: VerdictMethodRefused}, false},
		{"optional unrouted is outside", Row{Phase: PhaseWADL, Method: "PUT", Mode: ModeOptional, Verdict: VerdictUnrouted}, false},
		{"deprecated 405 is inside", Row{Phase: PhaseWADL, Method: "GET", Mode: ModeDeprecated, Verdict: VerdictDeprecatedRefused}, true},
		{"deprecated conformant is inside", Row{Phase: PhaseWADL, Method: "GET", Mode: ModeDeprecated, Verdict: VerdictConformant}, true},
		{"deprecated 404 is outside", Row{Phase: PhaseWADL, Method: "GET", Mode: ModeDeprecated, Verdict: VerdictUnrouted}, false},
		{"error conformant is inside", Row{Phase: PhaseWADL, Method: "DELETE", Mode: ModeError, Verdict: VerdictConformant}, true},
		{"error 404 is outside", Row{Phase: PhaseWADL, Method: "DELETE", Mode: ModeError, Verdict: VerdictEMode404}, false},
		{"not_reached resource row is not a method", Row{Phase: PhaseWADL, Mode: ModeMandatory, Verdict: VerdictNotReached}, true},
		{"another phase is not judged", Row{Phase: PhaseDiscovery, Method: "GET", Verdict: VerdictDeadLinkUnrouted}, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := insidePolicy(tc.row); got != tc.want {
				t.Errorf("insidePolicy() = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestOutsidePolicyCountsNotReachedMethods shows the count adds one entry per
// method of an unreached resource, and that one more violating row adds exactly
// one entry.
func TestOutsidePolicyCountsNotReachedMethods(t *testing.T) {
	m := &Model{Methods: []Method{
		{ResourceID: "A", Path: "/a", Verb: "GET", Mode: ModeMandatory},
		{ResourceID: "A", Path: "/a", Verb: "PUT", Mode: ModeOptional},
		{ResourceID: "B", Path: "/b", Verb: "GET", Mode: ModeMandatory},
	}}
	res := &Results{Rows: []Row{
		{Phase: PhaseWADL, ResourceID: "A", WADLPath: "/a", Verdict: VerdictNotReached, Mode: ModeMandatory},
		{Phase: PhaseWADL, ResourceID: "B", WADLPath: "/b", Method: "GET", Mode: ModeMandatory, Verdict: VerdictConformant},
	}}
	got := res.OutsidePolicy(m)
	want := []PolicyRow{
		{ResourceID: "A", Mode: ModeMandatory, Verdict: VerdictNotReached},
		{ResourceID: "A", Mode: ModeOptional, Verdict: VerdictNotReached},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("OutsidePolicy() = %v, want %v", got, want)
	}

	res.Rows = append(res.Rows, Row{Phase: PhaseWADL, ResourceID: "B", WADLPath: "/b", Method: "PUT", Mode: ModeOptional, Verdict: VerdictMethodRefused})
	if n := len(res.OutsidePolicy(m)); n != len(want)+1 {
		t.Errorf("one added violating row moved the count to %d, want %d", n, len(want)+1)
	}
}

// TestRestrictedWriteSelection covers what counts as a manager-only write.
func TestRestrictedWriteSelection(t *testing.T) {
	tests := []struct {
		m    Method
		want bool
	}{
		{Method{ResourceID: "Registration", Verb: http.MethodPut, Mode: ModeOptional}, true},
		{Method{ResourceID: "Registration", Verb: http.MethodGet, Mode: ModeMandatory}, false},
		{Method{ResourceID: "Registration", Verb: http.MethodDelete, Mode: ModeDeprecated}, false},
		{Method{ResourceID: "EndDevice", Verb: http.MethodPut, Mode: ModeOptional}, false},
		{Method{ResourceID: "Reading", Verb: http.MethodDelete, Mode: ModeOptional}, true},
	}
	for _, tc := range tests {
		if got := restrictedWrite(tc.m); got != tc.want {
			t.Errorf("restrictedWrite(%s %s %s) = %v, want %v", tc.m.ResourceID, tc.m.Verb, tc.m.Mode, got, tc.want)
		}
	}
}

// TestRouteRefusal403IsNotAnAuthorityForbid proves the device assertion does
// not accept the 403 the server gives a path no route family names.
func TestRouteRefusal403IsNotAnAuthorityForbid(t *testing.T) {
	for _, tc := range []struct {
		name string
		r    response
		want Verdict
	}{
		{"authority 403", response{status: http.StatusForbidden, body: []byte("forbidden")}, VerdictDeviceForbidden},
		{"route refusal 403", response{status: http.StatusForbidden, body: []byte(routefamily.RefusalBody)}, VerdictDeviceNotForbidden},
		{"405", response{status: http.StatusMethodNotAllowed}, VerdictDeviceNotForbidden},
	} {
		if got := deviceVerdict(tc.r); got != tc.want {
			t.Errorf("%s: deviceVerdict() = %q, want %q", tc.name, got, tc.want)
		}
	}
}

// TestClassifyDeprecated405 pins that a Deprecated method answered 405 is named
// as such, while the same answer on an Optional method stays a refusal.
func TestClassifyDeprecated405(t *testing.T) {
	r := response{status: http.StatusMethodNotAllowed}
	if got := classify(Method{Verb: "GET", Mode: ModeDeprecated}, "/x", r, "", nil); got != VerdictDeprecatedRefused {
		t.Errorf("deprecated 405 = %q, want %q", got, VerdictDeprecatedRefused)
	}
	if got := classify(Method{Verb: "PUT", Mode: ModeOptional}, "/x", r, "", nil); got != VerdictMethodRefused {
		t.Errorf("optional 405 = %q, want %q", got, VerdictMethodRefused)
	}
}

// TestDeviceProbedNotForbiddenRows shows the count is the length of the rows, so
// no caller restates the selection: only restricted-phase rows without an
// authority 403 count, and a row of another phase never does.
func TestDeviceProbedNotForbiddenRows(t *testing.T) {
	res := &Results{Rows: []Row{
		{Phase: PhaseRestricted, ResourceID: "A", Verdict: VerdictDeviceForbidden},
		{Phase: PhaseRestricted, ResourceID: "B", Verdict: VerdictDeviceNotForbidden},
		{Phase: PhaseWADL, ResourceID: "C", Verdict: VerdictConformant},
	}}
	rows := res.DeviceProbedNotForbiddenRows()
	if len(rows) != 1 || rows[0].ResourceID != "B" {
		t.Fatalf("DeviceProbedNotForbiddenRows() = %v, want only resource B", rows)
	}
	if n := res.DeviceProbedNotForbidden(); n != len(rows) {
		t.Errorf("DeviceProbedNotForbidden() = %d, want %d (the rows)", n, len(rows))
	}
}

// TestNotReachedRowsAreResourceRows shows the selection keeps only the WADL-phase
// not_reached rows.
func TestNotReachedRowsAreResourceRows(t *testing.T) {
	res := &Results{Rows: []Row{
		{Phase: PhaseWADL, ResourceID: "A", Verdict: VerdictNotReached},
		{Phase: PhaseRestricted, ResourceID: "B", Verdict: VerdictNotReached},
		{Phase: PhaseWADL, ResourceID: "C", Verdict: VerdictConformant},
	}}
	rows := res.NotReachedRows()
	if len(rows) != 1 || rows[0].ResourceID != "A" {
		t.Fatalf("NotReachedRows() = %v, want only resource A", rows)
	}
}
