package assembly_test

import (
	"log"
	"net/http"
	"strings"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/assembly"
)

// Log-reading tests. None of them runs in parallel: each swaps the
// process-wide log output.

const denialLogMarker = "ownership gate denied"

func captureLog(t *testing.T) *logProbeSafeBuffer {
	t.Helper()
	buf := &logProbeSafeBuffer{}
	prevOut, prevFlags := log.Writer(), log.Flags()
	log.SetFlags(0)
	log.SetOutput(buf)
	t.Cleanup(func() {
		log.SetOutput(prevOut)
		log.SetFlags(prevFlags)
	})
	return buf
}

// lineWith reports whether one captured line carries every fragment.
func lineWith(captured string, fragments ...string) bool {
	for _, line := range strings.Split(captured, "\n") {
		matched := line != ""
		for _, f := range fragments {
			if !strings.Contains(line, f) {
				matched = false
				break
			}
		}
		if matched {
			return true
		}
	}
	return false
}

func TestOwnershipGate_DenialsAreLoggedWithoutRecordContents(t *testing.T) {
	stores := seededGateStores(t)
	seedDevice(t, stores.EndDevices, "5", "", "")
	srv := gateServer(t, stores, gateTestPolicy())
	buf := captureLog(t)

	for _, p := range []struct {
		name, path, asLFDI string
		want               int
		fragments          []string
	}{
		{"not owner", "/edev/" + victimID, callerLFDI, http.StatusForbidden,
			[]string{denialLogMarker, "GET /edev/{id}", `caller="` + callerLFDI + `"`, `id="` + victimID + `"`, "reason=not-owner-or-manager"}},
		{"no identity", "/edev/" + victimID, "", http.StatusForbidden,
			[]string{denialLogMarker, `caller=""`, `id="` + victimID + `"`, "reason=no-identity"}},
		{"absent device", "/edev/404/fsa", callerLFDI, http.StatusNotFound,
			[]string{denialLogMarker, "GET /edev/{id}/fsa", `id="404"`, "reason=absent"}},
		{"record with no LFDI", "/edev/5", callerLFDI, http.StatusForbidden,
			[]string{denialLogMarker, `id="5"`, "reason=record-has-no-lfdi"}},
	} {
		status, raw := gateRequest(t, srv, http.MethodGet, p.path, p.asLFDI, "")
		if status != p.want {
			t.Errorf("%s: GET %s: status %d, want %d; body=%q", p.name, p.path, status, p.want, raw)
		}
		if !lineWith(buf.String(), p.fragments...) {
			t.Errorf("%s: no denial line carrying %q; log=%q", p.name, p.fragments, buf.String())
		}
	}
	for _, leaked := range []string{victimLFDI, victimSFDI, callerSFDI, "1600000000"} {
		if strings.Contains(buf.String(), leaked) {
			t.Errorf("the denial log carries record or identity content %q; log=%q", leaked, buf.String())
		}
	}
}

func TestOwnershipGate_DenialLogVolumeIsBounded(t *testing.T) {
	srv := gateServer(t, seededGateStores(t), gateTestPolicy())
	buf := captureLog(t)

	const probes = 500
	for i := 0; i < probes; i++ {
		if status, raw := gateRequest(t, srv, http.MethodGet, "/edev/"+victimID, callerLFDI, ""); status != http.StatusForbidden {
			t.Fatalf("probe %d: status %d, want 403; body=%q", i, status, raw)
		}
	}
	lines := strings.Count(buf.String(), denialLogMarker)
	if lines == 0 || lines >= probes/10 {
		t.Errorf("%d denials wrote %d denial lines; want at least one and far fewer than one per denial", probes, lines)
	}
	t.Logf("%d denials wrote %d denial lines", probes, lines)
}

func TestOwnershipGate_OneCallerCannotHideAnotherCallersDenials(t *testing.T) {
	srv := gateServer(t, seededGateStores(t), gateTestPolicy())
	buf := captureLog(t)

	const probes = 500
	for i := 0; i < probes; i++ {
		if status, raw := gateRequest(t, srv, http.MethodGet, "/edev/"+victimID, callerLFDI, ""); status != http.StatusForbidden {
			t.Fatalf("noisy probe %d: status %d, want 403; body=%q", i, status, raw)
		}
	}
	noisy := strings.Count(buf.String(), `caller="`+callerLFDI+`"`)
	if noisy == 0 || noisy >= probes {
		t.Fatalf("control: the noisy caller wrote %d of %d denial lines; want some written and some suppressed", noisy, probes)
	}

	status, raw := gateRequest(t, srv, http.MethodGet, "/edev/"+callerID, victimLFDI, "")
	if status != http.StatusForbidden {
		t.Fatalf("second caller: status %d, want 403; body=%q", status, raw)
	}
	if !lineWith(buf.String(), denialLogMarker, `caller="`+victimLFDI+`"`, `id="`+callerID+`"`, "reason=not-owner-or-manager") {
		t.Errorf("the second caller's refusal line is missing after another caller's burst; log=%q", buf.String())
	}
}

// TestManagement_RegistrationIsRefusedByTheGate distinguishes the gate from
// the Registration handler's own check: both refuse a manager, and only the
// gate writes a denial line.
func TestManagement_RegistrationIsRefusedByTheGate(t *testing.T) {
	srv := gateServer(t, newManagementFleet(t).stores, gateTestPolicy())
	buf := captureLog(t)

	status, raw := gateRequest(t, srv, http.MethodGet, "/edev/"+victimID+"/rg", managerLFDI, "")
	if status != http.StatusForbidden {
		t.Errorf("manager GET /edev/%s/rg: status %d, want 403; body=%q", victimID, status, raw)
	}
	if !lineWith(buf.String(), denialLogMarker, "GET /edev/{id}/rg", `caller="`+managerLFDI+`"`, "reason=not-owner") ||
		lineWith(buf.String(), denialLogMarker, "GET /edev/{id}/rg", "reason=not-owner-or-manager") {
		t.Errorf("the manager's Registration read was not refused by the ownership gate as a non-delegated route; log=%q", buf.String())
	}
}

func TestBuildProtocolRouter_LogsANilIdentity(t *testing.T) {
	buf := captureLog(t)
	const marker = "AuthPolicy.Identity is nil"

	assembly.BuildProtocolRouter(assembly.RouterConfig{}, testStores(), testAuthPolicy(), "serverSFDI", "serverLFDI", nil)
	if strings.Contains(buf.String(), marker) {
		t.Fatalf("control: a wired Identity logged %q; log=%q", marker, buf.String())
	}

	policy := testAuthPolicy()
	policy.Identity = nil
	assembly.BuildProtocolRouter(assembly.RouterConfig{}, testStores(), policy, "serverSFDI", "serverLFDI", nil)
	if !strings.Contains(buf.String(), marker) {
		t.Errorf("a nil AuthPolicy.Identity was substituted without a log line; log=%q", buf.String())
	}
}
