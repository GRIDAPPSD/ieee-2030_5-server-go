package assembly_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/activity"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/assembly"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/devicelog"
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

func TestManagedExclusive_StoreFailureAnswers500(t *testing.T) {
	fleet := newManagementFleet(t)
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
