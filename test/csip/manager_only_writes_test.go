package csip_test

import (
	"context"
	"encoding/xml"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/test/csip/csiptest"
)

// Optional writes that change a credential or server-authored data are for a
// manager certificate; a plain device is refused with 403 and has no way to
// become a manager through them. The lists a device reads show it only what it
// owns or manages. Two plain devices (A, B) and a manager of a third device
// (D) are enough to show each.

type managerWritesFixture struct {
	srv               *csiptest.BootedServer
	a, b, mgr         *http.Client
	aLFDI, bLFDI      string
	mgrLFDI, managedD string
	aEdevPath         string
}

func send(t *testing.T, c *http.Client, srv *csiptest.BootedServer, method, path, body string) (int, string, http.Header) {
	t.Helper()
	var rdr io.Reader
	if body != "" {
		rdr = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, srv.BaseURL+path, rdr)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Accept", "application/sep+xml")
	if body != "" {
		req.Header.Set("Content-Type", "application/sep+xml")
	}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	out, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, string(out), resp.Header
}

func newManagerWritesFixture(t *testing.T) *managerWritesFixture {
	t.Helper()
	srv := csiptest.BootServer(t)
	f := &managerWritesFixture{srv: srv, a: srv.HTTPClient()}
	f.b, _, f.bLFDI = srv.NewDeviceClient(t, "MOW-DEVICE-B")
	f.mgr, _, f.mgrLFDI = srv.NewDeviceClient(t, "MOW-MANAGER")
	_, dSFDI, managedD := srv.NewDeviceClient(t, "MOW-MANAGED-D")
	f.managedD = managedD

	ctx := context.Background()
	enabled := true
	d := sep2.EndDevice{LFDI: f.managedD, SFDI: dSFDI, Enabled: &enabled}
	d.Href = "/edev/managed-d"
	if err := srv.Stores.EndDevices.Create(ctx, "managed-d", d); err != nil {
		t.Fatalf("seed the managed device: %v", err)
	}
	if err := srv.Stores.EndDeviceManagers.Assign(ctx, f.mgrLFDI, f.managedD); err != nil {
		t.Fatalf("assign the manager: %v", err)
	}

	var aID string
	aID, f.aLFDI = f.register(t, f.a)
	f.aEdevPath = "/edev/" + aID
	f.register(t, f.b)
	return f
}

// register POSTs the caller's EndDevice and returns its id and the LFDI the
// server stored for it.
func (f *managerWritesFixture) register(t *testing.T, c *http.Client) (id, lfdi string) {
	t.Helper()
	status, body, hdr := send(t, c, f.srv, http.MethodPost, "/edev",
		`<EndDevice xmlns="urn:ieee:std:2030.5:ns"><changedTime>1785700000</changedTime><enabled>true</enabled></EndDevice>`)
	if status != http.StatusCreated {
		t.Fatalf("POST /edev: status %d, body %s", status, body)
	}
	id = strings.TrimPrefix(hdr.Get("Location"), "/edev/")
	dev, err := f.srv.Stores.EndDevices.Get(context.Background(), id)
	if err != nil {
		t.Fatalf("read back EndDevice %q: %v", id, err)
	}
	return id, dev.LFDI
}

const regPutBody = `<Registration xmlns="urn:ieee:std:2030.5:ns"><dateTimeRegistered>1</dateTimeRegistered><pIN>654321</pIN></Registration>`

const uptBody = `<UsagePoint xmlns="urn:ieee:std:2030.5:ns"><mRID>0F0F0F0F0F0F0F0F0F0F0F0F0F0F0F01</mRID>` +
	`<roleFlags>03</roleFlags><serviceCategoryKind>0</serviceCategoryKind><status>1</status></UsagePoint>`

func TestManagerOnlyWrites_PlainDeviceIsRefusedAndChangesNothing(t *testing.T) {
	f := newManagerWritesFixture(t)
	ctx := context.Background()
	aID := strings.TrimPrefix(f.aEdevPath, "/edev/")

	// A real registration, so a refused DELETE has something it could remove.
	if err := f.srv.Stores.Registrations.Create(ctx, aID, sep2.Registration{
		Resource: sep2.Resource{Href: f.aEdevPath + "/rg"}, DateTimeRegistered: 1785700000, PIN: 123455, PollRate: 900,
	}); err != nil {
		t.Fatalf("seed registration: %v", err)
	}

	writes := []struct{ method, path, body string }{
		{http.MethodPut, f.aEdevPath + "/rg", regPutBody},
		{http.MethodDelete, f.aEdevPath + "/rg", ""},
		{http.MethodPost, "/upt", uptBody},
	}
	for name, c := range map[string]*http.Client{"device A": f.a, "device B": f.b} {
		for _, w := range writes {
			status, _, _ := send(t, c, f.srv, w.method, w.path, w.body)
			if status != http.StatusForbidden {
				t.Errorf("%s %s %s: status %d, want 403", name, w.method, w.path, status)
			}
		}
	}

	reg, err := f.srv.Stores.Registrations.Get(ctx, aID)
	if err != nil || reg.PIN != 123455 {
		t.Errorf("registration after the refused writes = %+v, err %v; want the original pIN 123455", reg, err)
	}
	if n, _ := f.srv.Stores.UsagePoints.Count(ctx); n != 0 {
		t.Errorf("%d usage points exist after refused POSTs, want 0", n)
	}

	// None of these writes touches the management store, so a refused device
	// is still no manager, and the one real pair is unchanged.
	for name, lfdi := range map[string]string{"A": f.aLFDI, "B": f.bLFDI} {
		managed, err := f.srv.Stores.EndDeviceManagers.ManagedBy(ctx, lfdi)
		if err != nil || len(managed) != 0 {
			t.Errorf("device %s manages %v (err %v), want nothing", name, managed, err)
		}
	}
	if managed, _ := f.srv.Stores.EndDeviceManagers.ManagedBy(ctx, f.mgrLFDI); len(managed) != 1 || managed[0] != f.managedD {
		t.Errorf("manager manages %v, want exactly [%s]", managed, f.managedD)
	}
	if status, _, _ := send(t, f.a, f.srv, http.MethodPost, "/upt", uptBody); status != http.StatusForbidden {
		t.Errorf("device A POST /upt after the attempts: status %d, want 403 still", status)
	}
}

const managedDPath = "/edev/managed-d"

func TestManagerOnlyWrites_ManagerMakesThemOnItsOwnDevice(t *testing.T) {
	f := newManagerWritesFixture(t)
	ctx := context.Background()
	const dID = "managed-d"

	if status, body, hdr := send(t, f.mgr, f.srv, http.MethodPut, managedDPath+"/rg", regPutBody); status != http.StatusCreated || hdr.Get("Location") != managedDPath+"/rg" {
		t.Fatalf("manager PUT rg: status %d location %q body %s, want 201 at the registration", status, hdr.Get("Location"), body)
	}
	reg, err := f.srv.Stores.Registrations.Get(ctx, dID)
	if err != nil || reg.PIN != 654321 || reg.Href != managedDPath+"/rg" {
		t.Errorf("stored registration = %+v, err %v; want pIN 654321 at %s", reg, err, managedDPath+"/rg")
	}

	if status, body, _ := send(t, f.mgr, f.srv, http.MethodDelete, managedDPath+"/rg", ""); status != http.StatusOK || !strings.Contains(body, "654321") {
		t.Errorf("manager DELETE rg: status %d body %s, want 200 with the removed record", status, body)
	}
	if _, err := f.srv.Stores.Registrations.Get(ctx, dID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("registration after the manager's DELETE: err %v, want not found", err)
	}

	status, body, hdr := send(t, f.mgr, f.srv, http.MethodPost, "/upt", uptBody)
	if status != http.StatusCreated || !strings.HasPrefix(hdr.Get("Location"), "/upt/") {
		t.Fatalf("manager POST /upt: status %d location %q body %s, want 201", status, hdr.Get("Location"), body)
	}
	if n, _ := f.srv.Stores.UsagePoints.Count(ctx); n != 1 {
		t.Errorf("%d usage points after the manager's POST, want 1", n)
	}
}

// A manager acts only for devices it has a live pair with: the manager of
// device D cannot change the PIN of device A, nor of one that does not exist,
// and the refusals leave the stored registration untouched.
func TestManagerOnlyWrites_ManagerCannotActForAnotherDevice(t *testing.T) {
	f := newManagerWritesFixture(t)
	ctx := context.Background()
	aID := strings.TrimPrefix(f.aEdevPath, "/edev/")
	if err := f.srv.Stores.Registrations.Create(ctx, aID, sep2.Registration{
		Resource: sep2.Resource{Href: f.aEdevPath + "/rg"}, DateTimeRegistered: 1785700000, PIN: 123455, PollRate: 900,
	}); err != nil {
		t.Fatalf("seed registration: %v", err)
	}

	for _, w := range []struct{ method, path, body string }{
		{http.MethodPut, f.aEdevPath + "/rg", regPutBody},
		{http.MethodDelete, f.aEdevPath + "/rg", ""},
		{http.MethodPut, "/edev/no-such-device/rg", regPutBody},
	} {
		if status, _, _ := send(t, f.mgr, f.srv, w.method, w.path, w.body); status != http.StatusForbidden {
			t.Errorf("manager of D: %s %s: status %d, want 403", w.method, w.path, status)
		}
	}
	reg, err := f.srv.Stores.Registrations.Get(ctx, aID)
	if err != nil || reg.PIN != 123455 {
		t.Errorf("device A registration after the refused writes = %+v, err %v; want the original pIN 123455", reg, err)
	}
}

// A pair whose device record is gone grants nothing: not the write on that
// device, and not POST /upt.
func TestManagerOnlyWrites_StalePairGrantsNothing(t *testing.T) {
	f := newManagerWritesFixture(t)
	ctx := context.Background()

	if err := f.srv.Stores.EndDevices.Delete(ctx, "managed-d"); err != nil {
		t.Fatalf("remove the managed device's record: %v", err)
	}
	if managed, err := f.srv.Stores.EndDeviceManagers.ManagedBy(ctx, f.mgrLFDI); err != nil || len(managed) != 1 {
		t.Fatalf("control: the pair must still exist after the record is removed, got %v (err %v)", managed, err)
	}
	for _, w := range []struct{ method, path, body string }{
		{http.MethodPut, managedDPath + "/rg", regPutBody},
		{http.MethodPost, "/upt", uptBody},
	} {
		if status, _, _ := send(t, f.mgr, f.srv, w.method, w.path, w.body); status != http.StatusForbidden {
			t.Errorf("manager with only a stale pair: %s %s: status %d, want 403", w.method, w.path, status)
		}
	}
	if _, err := f.srv.Stores.Registrations.Get(ctx, "managed-d"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("a registration exists for the removed device: err %v, want not found", err)
	}
	if n, _ := f.srv.Stores.UsagePoints.Count(ctx); n != 0 {
		t.Errorf("%d usage points after the refused POST, want 0", n)
	}
}

func listMRIDs(t *testing.T, body string, root any) []string {
	t.Helper()
	switch list := root.(type) {
	case *sep2.MirrorUsagePointList:
		if err := xml.Unmarshal([]byte(body), list); err != nil {
			t.Fatalf("unmarshal %s: %v", body, err)
		}
		var out []string
		for _, m := range list.MirrorUsagePoint {
			out = append(out, m.MRID)
		}
		return out
	case *sep2.UsagePointList:
		if err := xml.Unmarshal([]byte(body), list); err != nil {
			t.Fatalf("unmarshal %s: %v", body, err)
		}
		var out []string
		for _, u := range list.UsagePoint {
			out = append(out, u.MRID)
		}
		return out
	}
	t.Fatalf("unsupported list type %T", root)
	return nil
}

func mirrorBody(mrid, deviceLFDI string) string {
	b := `<MirrorUsagePoint xmlns="urn:ieee:std:2030.5:ns"><mRID>` + mrid + `</mRID>`
	if deviceLFDI != "" {
		b += `<deviceLFDI>` + deviceLFDI + `</deviceLFDI>`
	}
	return b + `<roleFlags>03</roleFlags><serviceCategoryKind>0</serviceCategoryKind><status>1</status></MirrorUsagePoint>`
}

func TestScopedLists_MirrorsAndUsagePoints(t *testing.T) {
	f := newManagerWritesFixture(t)
	const (
		mridA = "0A0A0A0A0A0A0A0A0A0A0A0A0A0A0A0A"
		mridB = "0B0B0B0B0B0B0B0B0B0B0B0B0B0B0B0B"
		mridD = "0D0D0D0D0D0D0D0D0D0D0D0D0D0D0D0D"
	)
	for _, p := range []struct {
		c    *http.Client
		body string
	}{
		{f.a, mirrorBody(mridA, "")},
		{f.b, mirrorBody(mridB, "")},
		{f.mgr, mirrorBody(mridD, f.managedD)},
	} {
		if status, body, _ := send(t, p.c, f.srv, http.MethodPost, "/mup", p.body); status != http.StatusCreated {
			t.Fatalf("POST /mup: status %d body %s", status, body)
		}
	}

	for _, tc := range []struct {
		name string
		c    *http.Client
		want string
	}{
		{"device A", f.a, mridA},
		{"device B", f.b, mridB},
		{"the manager", f.mgr, mridD},
	} {
		status, body, _ := send(t, tc.c, f.srv, http.MethodGet, "/mup", "")
		if status != http.StatusOK {
			t.Fatalf("%s GET /mup: status %d", tc.name, status)
		}
		var list sep2.MirrorUsagePointList
		got := listMRIDs(t, body, &list)
		if len(got) != 1 || got[0] != tc.want || list.All != 1 {
			t.Errorf("%s sees %v (all=%d), want only [%s]", tc.name, got, list.All, tc.want)
		}
	}

	if status, _, _ := send(t, f.mgr, f.srv, http.MethodPost, "/upt", uptBody); status != http.StatusCreated {
		t.Fatalf("manager POST /upt: status %d", status)
	}
	for _, tc := range []struct {
		name string
		c    *http.Client
		want int
	}{{"the manager", f.mgr, 1}, {"device A", f.a, 0}, {"device B", f.b, 0}} {
		status, body, _ := send(t, tc.c, f.srv, http.MethodGet, "/upt", "")
		var list sep2.UsagePointList
		got := listMRIDs(t, body, &list)
		if status != http.StatusOK || len(got) != tc.want || int(list.All) != tc.want {
			t.Errorf("%s GET /upt: status %d sees %v (all=%d), want %d records", tc.name, status, got, list.All, tc.want)
		}
	}
}
