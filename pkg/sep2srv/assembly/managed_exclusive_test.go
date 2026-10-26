package assembly_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/activity"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/assembly"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/devicelog"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/storetest"
)

// A paired DER is refused on its own certificate (CSIP IG 3.2: one and only
// one scenario per DER; the pair is the utility's designation). The fleet
// pairs victimLFDI, secondChildLFDI and callerLFDI.

func serveHandler(t *testing.T, h http.Handler) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv
}

const forbiddenBody = "forbidden\n"

var fleetPairedLFDIs = []string{victimLFDI, secondChildLFDI, callerLFDI}

// TestManagedExclusive_PairedCallerIsRefusedOnEveryRoute sweeps every pattern
// the router mounts, so a route added later is covered without an edit here.
// It cannot run in parallel: it swaps the process-wide log output.
func TestManagedExclusive_PairedCallerIsRefusedOnEveryRoute(t *testing.T) {
	fleet := newManagementFleet(t)
	buf := captureLog(t)
	handler, patterns := assembly.BuildProtocolRouter(assembly.RouterConfig{}, fleet.stores, gateTestPolicy(), "serverSFDI", "serverLFDI", nil)
	if len(patterns) < 50 {
		t.Fatalf("router mounts %d patterns; the sweep would be vacuous", len(patterns))
	}
	srv := serveHandler(t, handler)

	for _, lfdi := range fleetPairedLFDIs {
		for _, p := range patterns {
			req := patternRequest(t, srv.URL, p)
			req.Header.Set(gateIdentityHeader, lfdi)
			if status, raw := sendGateRequest(t, req); status != http.StatusForbidden || string(raw) != forbiddenBody {
				t.Errorf("%s as paired %s: status %d body %q, want 403 %q", p, lfdi, status, raw, forbiddenBody)
			}
		}
		if !lineWith(buf.String(), denialLogMarker, `caller="`+lfdi+`"`, "reason=managed-direct") {
			t.Errorf("no denial line naming %s and reason managed-direct; log=%q", lfdi, buf.String())
		}
	}

	// The aggregator acting for the same devices is unchanged.
	for _, path := range []string{"/dcap", "/edev/" + victimID, "/edev/" + victimID + "/der"} {
		if status, raw := gateRequest(t, srv, http.MethodGet, path, managerLFDI, ""); status != http.StatusOK {
			t.Errorf("aggregator GET %s: status %d, want 200; body=%q", path, status, raw)
		}
	}
	if list := listAs(t, srv, "", managerLFDI); list.All != 3 {
		t.Errorf("aggregator list: all=%d, want its own device and both managed ones", list.All)
	}
}

// TestManagedExclusive_UnpairedCallerIsNotRefusedAsManagedDirect is the
// control for the sweep above: the same patterns as a caller with no pair are
// never refused for that reason, and at least one is served.
func TestManagedExclusive_UnpairedCallerIsNotRefusedAsManagedDirect(t *testing.T) {
	fleet := newManagementFleet(t)
	buf := captureLog(t)
	handler, patterns := assembly.BuildProtocolRouter(assembly.RouterConfig{}, fleet.stores, gateTestPolicy(), "serverSFDI", "serverLFDI", nil)
	srv := serveHandler(t, handler)

	served := 0
	for _, p := range patterns {
		req := patternRequest(t, srv.URL, p)
		req.Header.Set(gateIdentityHeader, recordlessLFDI)
		if status, _ := sendGateRequest(t, req); status == http.StatusOK {
			served++
		}
	}
	if served == 0 {
		t.Error("no pattern was served to the unpaired caller; the sweep proves nothing")
	}
	if strings.Contains(buf.String(), "managed-direct") {
		t.Errorf("an unpaired caller was refused as managed-direct; log=%q", buf.String())
	}
}

// The refusal line names the full request path and the caller; the mount
// prefix and an empty id would name nothing. It cannot run in parallel: it
// swaps the process-wide log output.
func TestManagedExclusive_DenialLineNamesPathAndCaller(t *testing.T) {
	fleet := newManagementFleet(t)
	srv := gateServer(t, fleet.stores, gateTestPolicy())
	buf := captureLog(t)

	path := "/edev/" + victimID + "/der"
	if status, _ := gateRequest(t, srv, http.MethodGet, path, victimLFDI, ""); status != http.StatusForbidden {
		t.Fatalf("paired GET %s: status %d, want 403", path, status)
	}
	if !lineWith(buf.String(), denialLogMarker, `GET "`+path+`"`, `caller="`+victimLFDI+`"`, "reason=managed-direct") {
		t.Errorf("no denial line with the method, full path, caller and reason; log=%q", buf.String())
	}
}

// The 500 is logged with the caller and path through the denial budget. It
// cannot run in parallel: it swaps the process-wide log output.
func TestManagedExclusive_StoreFailureAnswers500(t *testing.T) {
	fleet := newManagementFleet(t)
	buf := captureLog(t)
	fault := &storetest.Fault{}
	fleet.stores.EndDeviceManagers = faultyManagers{inner: fleet.managers, fault: fault}
	srv := gateServer(t, fleet.stores, gateTestPolicy())
	fault.Arm(storetest.ErrBackendUnavailable)

	for _, lfdi := range []string{victimLFDI, recordlessLFDI} {
		status, raw := gateRequest(t, srv, http.MethodGet, "/dcap", lfdi, "")
		if status != http.StatusInternalServerError {
			t.Errorf("GET /dcap as %s with the management store failing: status %d, want 500; body=%q", lfdi, status, raw)
		}
		if strings.Contains(string(raw), storetest.ErrBackendUnavailable.Error()) {
			t.Errorf("store error text reached the client; body=%q", raw)
		}
	}

	if !lineWith(buf.String(), denialLogMarker, `GET "/dcap"`, `caller="`+victimLFDI+`"`, "reason=management-store-error") {
		t.Errorf("no budgeted line naming the caller and path for the store failure; log=%q", buf.String())
	}

	// No identity means no lookup, so the anonymous /tm is unaffected.
	if status, raw := gateRequest(t, srv, http.MethodGet, "/tm", "", ""); status != http.StatusOK {
		t.Errorf("anonymous GET /tm with the management store failing: status %d, want 200; body=%q", status, raw)
	}
}

func TestManagedExclusive_AnonymousTimeIsStillServed(t *testing.T) {
	t.Parallel()
	srv := gateServer(t, newManagementFleet(t).stores, gateTestPolicy())
	if status, raw := gateRequest(t, srv, http.MethodGet, "/tm", "", ""); status != http.StatusOK || !strings.Contains(string(raw), "<Time") {
		t.Errorf("anonymous GET /tm: status %d body %q, want 200 and a Time document", status, raw)
	}
}

func TestManagedExclusive_RefusedRequestMovesLastSeen(t *testing.T) {
	t.Parallel()
	fleet := newManagementFleet(t)
	rec := activity.New()
	handler, _ := assembly.BuildProtocolRouter(assembly.RouterConfig{Activity: rec}, fleet.stores, gateTestPolicy(), "serverSFDI", "serverLFDI", nil)
	srv := serveHandler(t, handler)

	if status, _ := gateRequest(t, srv, http.MethodGet, "/dcap", victimLFDI, ""); status != http.StatusForbidden {
		t.Fatalf("paired GET /dcap: status %d, want 403", status)
	}
	if _, n, ok := rec.Last(victimLFDI); !ok || n != 1 {
		t.Errorf("Last(paired) count = %d ok=%v, want the refused request recorded once", n, ok)
	}
}

func TestManagedExclusive_RefusalIsALineInTheDeviceLog(t *testing.T) {
	t.Parallel()
	srv, dir, closeLog := logServer(t, gateTestPolicy())

	if status, _ := gateRequest(t, srv, http.MethodGet, "/edev/"+victimID, victimLFDI, ""); status != http.StatusForbidden {
		t.Fatalf("paired GET /edev/%s: status %d, want 403", victimID, status)
	}
	closeLog()

	var found int
	for _, name := range logDirNames(t, dir) {
		_, lines := readLogLines(t, filepath.Join(dir, name))
		for _, l := range lines {
			if l["kind"] == devicelog.KindRefused && l["actor"] == victimLFDI && l["reason"] == "managed-direct" && l["status"] == float64(http.StatusForbidden) {
				found++
			}
		}
	}
	if found != 1 {
		t.Errorf("found %d refused lines naming managed-direct for %s, want 1 (files %v)", found, victimLFDI, logDirNames(t, dir))
	}
}

func TestManagedExclusive_RemovingThePairRestoresDirectAccess(t *testing.T) {
	t.Parallel()
	fleet := newManagementFleet(t)
	srv := gateServer(t, fleet.stores, gateTestPolicy())
	path := "/edev/" + victimID

	if status, _ := gateRequest(t, srv, http.MethodGet, path, victimLFDI, ""); status != http.StatusForbidden {
		t.Fatalf("paired GET %s: status %d, want 403", path, status)
	}
	if err := fleet.managers.Unassign(context.Background(), victimLFDI); err != nil {
		t.Fatalf("Unassign: %v", err)
	}
	if status, raw := gateRequest(t, srv, http.MethodGet, path, victimLFDI, ""); status != http.StatusOK {
		t.Errorf("GET %s after the pair was removed: status %d, want 200; body=%q", path, status, raw)
	}
}

// A quote in the request path must not close the logged path and add a field
// to the denial line. It cannot run in parallel: it swaps the process-wide
// log output.
func TestManagedExclusive_QuoteInPathCannotForgeAField(t *testing.T) {
	fleet := newManagementFleet(t)
	srv := gateServer(t, fleet.stores, gateTestPolicy())
	buf := captureLog(t)

	forged := "/edev/1%22:%20caller=%22FORGED%22%20reason=%22managed-direct"
	if status, _ := gateRequest(t, srv, http.MethodGet, forged, victimLFDI, ""); status != http.StatusForbidden {
		t.Fatalf("paired GET of a path carrying quotes: status %d, want 403", status)
	}
	var lines []string
	for _, l := range strings.Split(buf.String(), "\n") {
		if strings.Contains(l, denialLogMarker) {
			lines = append(lines, l)
		}
	}
	if len(lines) != 1 {
		t.Fatalf("got %d denial lines, want 1: %q", len(lines), buf.String())
	}
	if strings.Contains(lines[0], `caller="FORGED"`) || strings.Count(lines[0], `caller="`) != 1 || strings.Count(lines[0], "reason=") != 2 {
		// reason= appears once as the real field and once inside the escaped path.
		t.Errorf("a quote in the path added a field to the denial line: %q", lines[0])
	}
	if !strings.HasSuffix(lines[0], `caller="`+victimLFDI+`" reason=managed-direct`) {
		t.Errorf("the real caller and reason must close the line: %q", lines[0])
	}
}

// Repeated management store failures write one budgeted line per request up
// to the caller's budget, and no unbudgeted line per request. It cannot run
// in parallel: it swaps the process-wide log output.
func TestManagedExclusive_StoreFailureWritesOnlyBudgetedLines(t *testing.T) {
	fleet := newManagementFleet(t)
	fault := &storetest.Fault{}
	fleet.stores.EndDeviceManagers = faultyManagers{inner: fleet.managers, fault: fault}
	srv := gateServer(t, fleet.stores, gateTestPolicy())
	buf := captureLog(t)
	fault.Arm(storetest.ErrBackendUnavailable)

	const requests = 12
	for range requests {
		status, raw := gateRequest(t, srv, http.MethodGet, "/dcap", victimLFDI, "")
		if status != http.StatusInternalServerError || string(raw) != "internal error\n" {
			t.Fatalf("status %d body %q, want 500 with the fixed body", status, raw)
		}
	}
	var budgeted, unbudgeted int
	for _, l := range strings.Split(buf.String(), "\n") {
		switch {
		case strings.Contains(l, denialLogMarker) && strings.Contains(l, "reason=management-store-error"):
			budgeted++
		case strings.Contains(l, "sep2srv: 500"):
			unbudgeted++
		}
	}
	if budgeted != 5 || unbudgeted != 0 {
		t.Errorf("after %d failures: %d budgeted lines (want the per-caller budget of 5), %d unbudgeted 500 lines (want 0); log=%q", requests, budgeted, unbudgeted, buf.String())
	}
}

// A paired DER cannot delete itself on its own certificate, while its
// manager's delete of it still works and takes the pair with the record. It
// cannot run in parallel: it swaps the process-wide log output.
func TestManagedExclusive_PairedDERSelfDeleteIsRefusedButManagerDeleteWorks(t *testing.T) {
	fleet := newManagementFleet(t)
	srv := managerDeleteServer(t, assembly.RouterConfig{}, fleet)
	buf := captureLog(t)
	path := "/edev/" + victimID

	status, raw := gateRequest(t, srv, http.MethodDelete, path, victimLFDI, "")
	if status != http.StatusForbidden || string(raw) != forbiddenBody {
		t.Fatalf("paired DER DELETE %s: status %d body %q, want 403 %q", path, status, raw, forbiddenBody)
	}
	if !lineWith(buf.String(), denialLogMarker, `DELETE "`+path+`"`, `caller="`+victimLFDI+`"`, "reason=managed-direct") {
		t.Errorf("no managed-direct denial line for the self delete; log=%q", buf.String())
	}
	assertRecordHeld(t, fleet, victimID, victimLFDI)
	assertPairHeld(t, fleet, victimLFDI, managerLFDI)

	if status, raw := gateRequest(t, srv, http.MethodDelete, path, managerLFDI, ""); status != http.StatusNoContent {
		t.Fatalf("manager DELETE %s: status %d, want 204; body=%q", path, status, raw)
	}
	if _, err := fleet.stores.EndDevices.Get(context.Background(), victimID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("record after the manager's delete: err=%v, want ErrNotFound", err)
	}
	if _, err := fleet.managers.ManagerOf(context.Background(), victimLFDI); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("ManagerOf(deleted device) = %v, want ErrNotFound", err)
	}
}
