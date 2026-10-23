package assembly_test

import (
	"net/http"
	"strings"
	"testing"
)

const rotatedManagerLFDI = "AB00000000000000000000000000000000000099"

// TestManagement_RotatedManagerIsRefusedUntilRekeyed pins the #1087 rotation
// rule: a pair is keyed on the LFDI, so a manager whose certificate rotated
// is refused with a logged reason (never silently served as another device),
// the pair moves only when an operator rekeys it, and the old identity then
// loses the grant. It cannot run in parallel: it swaps the process-wide log
// output.
func TestManagement_RotatedManagerIsRefusedUntilRekeyed(t *testing.T) {
	fleet := newManagementFleet(t)
	srv := gateServer(t, fleet.stores, gateTestPolicy())
	buf := captureLog(t)
	path := "/edev/" + victimID

	if status, raw := gateRequest(t, srv, http.MethodGet, path, managerLFDI, ""); status != http.StatusOK {
		t.Fatalf("before rotation: manager GET %s status %d, want 200; body=%s", path, status, raw)
	}

	status, raw := gateRequest(t, srv, http.MethodGet, path, rotatedManagerLFDI, "")
	if status != http.StatusForbidden {
		t.Fatalf("rotated manager before rekey: GET %s status %d, want 403; body=%s", path, status, raw)
	}
	assertNoFleetData(t, "rotated manager before rekey", raw)
	if !lineWith(buf.String(), denialLogMarker, `caller="`+rotatedManagerLFDI+`"`, `id="`+victimID+`"`, "reason=not-owner-or-manager") {
		t.Errorf("no denial line naming the rotated caller and its reason; log=%q", buf.String())
	}
	if status, raw := gateRequest(t, srv, http.MethodGet, "/edev", rotatedManagerLFDI, ""); status != http.StatusOK ||
		strings.Contains(string(raw), "/edev/"+victimID+"\"") {
		t.Errorf("rotated manager before rekey: GET /edev status %d, body must not list /edev/%s; body=%s", status, victimID, raw)
	}
	if got, err := fleet.managers.ManagerOf(t.Context(), victimLFDI); err != nil || got != managerLFDI {
		t.Fatalf("pair moved on its own: ManagerOf = %q, %v; want %q", got, err, managerLFDI)
	}

	if err := fleet.managers.RekeyManager(t.Context(), managerLFDI, rotatedManagerLFDI); err != nil {
		t.Fatalf("RekeyManager: %v", err)
	}

	status, raw = gateRequest(t, srv, http.MethodGet, path, rotatedManagerLFDI, "")
	if status != http.StatusOK {
		t.Fatalf("rotated manager after rekey: GET %s status %d, want 200; body=%s", path, status, raw)
	}
	if !strings.Contains(string(raw), `href="`+path+`"`) {
		t.Errorf("rotated manager after rekey: response does not carry href %s; body=%s", path, raw)
	}
	status, raw = gateRequest(t, srv, http.MethodGet, path, managerLFDI, "")
	if status != http.StatusForbidden {
		t.Errorf("old identity after rekey: GET %s status %d, want 403; body=%s", path, status, raw)
	}
}
