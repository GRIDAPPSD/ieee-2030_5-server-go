package assembly_test

import (
	"context"
	"encoding/xml"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/assembly"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

// POST /edev by a manager registers a managed device (#1087 slice 1), driven
// through the assembled router with the identity carried as production does.

const (
	// managedNewLFDI is paired to managerLFDI by each test and has no record.
	managedNewLFDI = recordlessLFDI
	// managedNewSFDI is the SFDI of managedNewLFDI, worked out by hand from
	// the leading 36 bits (0xC90000000) and the digit-sum check digit, not
	// taken from the code under test.
	managedNewSFDI = "539555266563"
)

func managerPostServer(t *testing.T, cfg assembly.RouterConfig) (*httptest.Server, managementFleet) {
	t.Helper()
	fleet := newManagementFleet(t)
	if err := fleet.managers.Assign(context.Background(), managerLFDI, managedNewLFDI); err != nil {
		t.Fatalf("pair the new device: %v", err)
	}
	// The fleet's records occupy ids 1 to 10; burn the allocator past them so a
	// new device lands on a vacant id.
	idx := memory.NewEndDeviceIndex()
	for i := 0; i < 20; i++ {
		if _, err := idx.Allocate(fmt.Sprintf("burn-%d", i)); err != nil {
			t.Fatalf("burn index: %v", err)
		}
	}
	fleet.stores.EndDeviceIndexes = idx
	handler, _ := assembly.BuildProtocolRouter(cfg, fleet.stores, gateTestPolicy(), "serverSFDI", "serverLFDI", nil)
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return srv, fleet
}

func edevBody(lfdi, sfdi string) string {
	var b strings.Builder
	b.WriteString(`<EndDevice xmlns="urn:ieee:std:2030.5:ns">`)
	if lfdi != "" {
		b.WriteString("<lFDI>" + lfdi + "</lFDI>")
	}
	if sfdi != "" {
		b.WriteString("<sFDI>" + sfdi + "</sFDI>")
	}
	b.WriteString(`</EndDevice>`)
	return b.String()
}

func decodeDevice(t *testing.T, raw []byte) sep2.EndDevice {
	t.Helper()
	var dev sep2.EndDevice
	if err := xml.Unmarshal(raw, &dev); err != nil {
		t.Fatalf("decode EndDevice: %v; body=%s", err, raw)
	}
	return dev
}

func TestManagerPost_CreatesTheManagedRecordListsItAndRepeatsToTheSameHref(t *testing.T) {
	t.Parallel()
	srv, fleet := managerPostServer(t, assembly.RouterConfig{})

	status, raw := gateRequest(t, srv, http.MethodPost, "/edev", managerLFDI, edevBody(managedNewLFDI, ""))
	if status != http.StatusCreated {
		t.Fatalf("manager POST: status %d, want 201; body=%s", status, raw)
	}
	created := decodeDevice(t, raw)
	if created.LFDI != managedNewLFDI || created.SFDI != managedNewSFDI {
		t.Errorf("response LFDI %q SFDI %q, want %q and %q", created.LFDI, created.SFDI, managedNewLFDI, managedNewSFDI)
	}

	stored, err := fleet.stores.EndDevices.GetByLFDI(context.Background(), managedNewLFDI)
	if err != nil {
		t.Fatalf("stored record by managed LFDI: %v", err)
	}
	if stored.LFDI != managedNewLFDI || stored.SFDI != managedNewSFDI || stored.Href != created.Href {
		t.Errorf("stored LFDI %q SFDI %q href %q, want %q %q %q", stored.LFDI, stored.SFDI, stored.Href, managedNewLFDI, managedNewSFDI, created.Href)
	}

	status, raw = gateRequest(t, srv, http.MethodGet, "/edev", managerLFDI, "")
	if status != http.StatusOK {
		t.Fatalf("manager GET /edev: status %d; body=%s", status, raw)
	}
	var list sep2.EndDeviceList
	if err := xml.Unmarshal(raw, &list); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	found := false
	for _, d := range list.EndDevice {
		if d.LFDI == managedNewLFDI && d.Href == created.Href {
			found = true
		}
	}
	if !found {
		t.Errorf("GET /edev from the manager does not list %s at %s; body=%s", managedNewLFDI, created.Href, raw)
	}

	status, raw = gateRequest(t, srv, http.MethodPost, "/edev", managerLFDI, edevBody(managedNewLFDI, ""))
	if status != http.StatusOK {
		t.Fatalf("repeat POST: status %d, want 200; body=%s", status, raw)
	}
	if again := decodeDevice(t, raw); again.Href != created.Href {
		t.Errorf("repeat POST href %q, want %q", again.Href, created.Href)
	}
}

func TestManagerPost_RefusesAnLFDIItDoesNotManageAndWritesNothing(t *testing.T) {
	t.Parallel()
	srv, fleet := managerPostServer(t, assembly.RouterConfig{})
	const stranger = "D100000000000000000000000000000000000001"

	for name, lfdi := range map[string]string{
		"unpaired":                stranger,
		"managed by someone else": callerLFDI,
	} {
		before := countEndDevices(t, fleet.stores.EndDevices)
		status, raw := gateRequest(t, srv, http.MethodPost, "/edev", managerLFDI, edevBody(lfdi, ""))
		if status != http.StatusForbidden {
			t.Errorf("%s: status %d, want 403; body=%s", name, status, raw)
		}
		if got := countEndDevices(t, fleet.stores.EndDevices); got != before {
			t.Errorf("%s: record count %d, want %d (a refusal writes nothing)", name, got, before)
		}
	}
	if _, err := fleet.stores.EndDevices.GetByLFDI(context.Background(), stranger); err == nil {
		t.Errorf("a record exists for %s after the refusal", stranger)
	}
}

func countEndDevices(t *testing.T, s store.EndDeviceStore) int {
	t.Helper()
	res, err := s.List(context.Background(), store.ListOptions{})
	if err != nil {
		t.Fatalf("list EndDevices: %v", err)
	}
	return int(res.All)
}

func TestManagerPost_OwnLFDIInShortLowerCaseRegistersTheCaller(t *testing.T) {
	t.Parallel()
	srv, fleet := managerPostServer(t, assembly.RouterConfig{})
	const self = "00CD0000000000000000000000000000000000EF"
	short := strings.ToLower(self[2:])

	status, raw := gateRequest(t, srv, http.MethodPost, "/edev", self, edevBody(short, ""))
	if status != http.StatusCreated {
		t.Fatalf("status %d, want 201; body=%s", status, raw)
	}
	if dev := decodeDevice(t, raw); dev.LFDI != self {
		t.Errorf("stored LFDI %q, want the caller's %q", dev.LFDI, self)
	}
	if _, err := fleet.stores.EndDevices.GetByLFDI(context.Background(), self); err != nil {
		t.Errorf("no record under the caller's LFDI: %v", err)
	}
}

func TestManagerPost_SFDIMustMatchTheDerivedValue(t *testing.T) {
	t.Parallel()
	srv, fleet := managerPostServer(t, assembly.RouterConfig{})

	status, raw := gateRequest(t, srv, http.MethodPost, "/edev", managerLFDI, edevBody(managedNewLFDI, "111111111116"))
	if status != http.StatusBadRequest {
		t.Fatalf("wrong sFDI: status %d, want 400; body=%s", status, raw)
	}
	if _, err := fleet.stores.EndDevices.GetByLFDI(context.Background(), managedNewLFDI); err == nil {
		t.Error("a record was written for a refused sFDI")
	}

	status, raw = gateRequest(t, srv, http.MethodPost, "/edev", managerLFDI, edevBody(managedNewLFDI, managedNewSFDI))
	if status != http.StatusCreated {
		t.Fatalf("matching sFDI: status %d, want 201; body=%s", status, raw)
	}
}

func TestManagerPost_MalformedLFDIAnswers400(t *testing.T) {
	t.Parallel()
	srv, _ := managerPostServer(t, assembly.RouterConfig{})
	for _, bad := range []string{"not-hex", "ABC", strings.Repeat("A", 42)} {
		if status, raw := gateRequest(t, srv, http.MethodPost, "/edev", managerLFDI, edevBody(bad, "")); status != http.StatusBadRequest {
			t.Errorf("lFDI %q: status %d, want 400; body=%s", bad, status, raw)
		}
	}
}

func TestManagerPost_SFDICollisionWithAnotherLFDIAnswers409(t *testing.T) {
	t.Parallel()
	srv, fleet := managerPostServer(t, assembly.RouterConfig{})
	// Shares the first 9 hex characters with managedNewLFDI, so the same SFDI.
	holder := managedNewLFDI[:9] + strings.Repeat("1", 31)
	seedDevice(t, fleet.stores.EndDevices, "50", holder, managedNewSFDI)

	status, raw := gateRequest(t, srv, http.MethodPost, "/edev", managerLFDI, edevBody(managedNewLFDI, ""))
	if status != http.StatusConflict || strings.TrimSpace(string(raw)) != "device SFDI conflict" {
		t.Fatalf("status %d body %q, want 409 and the SFDI conflict message", status, raw)
	}
	if _, err := fleet.stores.EndDevices.GetByLFDI(context.Background(), managedNewLFDI); err == nil {
		t.Error("a record was written despite the SFDI collision")
	}
	if got, _ := fleet.stores.EndDevices.GetBySFDI(context.Background(), managedNewSFDI); got.LFDI != holder {
		t.Errorf("SFDI holder is now %q, want the original %q", got.LFDI, holder)
	}
}

func TestManagerPost_LFDIHeldInAnotherCaseAnswers409(t *testing.T) {
	t.Parallel()
	srv, fleet := managerPostServer(t, assembly.RouterConfig{})
	lower := strings.ToLower(managedNewLFDI)
	// A different SFDI, so only the case-insensitive LFDI check can see it.
	seedDevice(t, fleet.stores.EndDevices, "50", lower, "")

	status, raw := gateRequest(t, srv, http.MethodPost, "/edev", managerLFDI, edevBody(managedNewLFDI, ""))
	if status != http.StatusConflict || strings.TrimSpace(string(raw)) != "device LFDI conflict" {
		t.Fatalf("status %d body %q, want 409 and the LFDI conflict message", status, raw)
	}
	if _, err := fleet.stores.EndDevices.GetByLFDI(context.Background(), managedNewLFDI); err == nil {
		t.Error("an upper-case record was minted beside the lower-case one")
	}
}

func TestManagerPost_OptionOffRefusesTheManagerAddButNotSelfRegistration(t *testing.T) {
	t.Parallel()
	srv, fleet := managerPostServer(t, assembly.RouterConfig{InBandManagementOff: true})

	status, raw := gateRequest(t, srv, http.MethodPost, "/edev", managerLFDI, edevBody(managedNewLFDI, ""))
	if status != http.StatusForbidden {
		t.Fatalf("manager POST with the option off: status %d, want 403; body=%s", status, raw)
	}
	if _, err := fleet.stores.EndDevices.GetByLFDI(context.Background(), managedNewLFDI); err == nil {
		t.Error("a record was written with the option off")
	}

	const self = "00CD0000000000000000000000000000000000EF"
	if status, raw := gateRequest(t, srv, http.MethodPost, "/edev", self, ""); status != http.StatusCreated {
		t.Errorf("self registration with the option off: status %d, want 201; body=%s", status, raw)
	}
}

// Not parallel: it captures the process log.
func TestManagerPost_RefusalsShareTheBoundedDenialLog(t *testing.T) {
	srv, _ := managerPostServer(t, assembly.RouterConfig{})
	var buf strings.Builder
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })

	gateRequest(t, srv, http.MethodPost, "/edev", managerLFDI, edevBody("not-hex", ""))
	for i := 0; i < 12; i++ {
		lfdi := fmt.Sprintf("D1%038X", i)
		if status, _ := gateRequest(t, srv, http.MethodPost, "/edev", managerLFDI, edevBody(lfdi, "")); status != http.StatusForbidden {
			t.Fatalf("unmanaged POST %d: status %d, want 403", i, status)
		}
	}

	var lines, invalid, notManager int
	for _, l := range strings.Split(buf.String(), "\n") {
		if !strings.Contains(l, "ownership gate denied POST /edev") {
			continue
		}
		lines++
		if strings.Contains(l, "reason=invalid-lfdi") {
			invalid++
		}
		if strings.Contains(l, "reason=not-manager") {
			notManager++
		}
	}
	// One caller's budget is 5 lines per window, shared by every reason.
	if lines != 5 || invalid != 1 || notManager != 4 {
		t.Errorf("denial lines %d (invalid-lfdi %d, not-manager %d), want 5 (1, 4) for 13 refusals", lines, invalid, notManager)
	}
}
