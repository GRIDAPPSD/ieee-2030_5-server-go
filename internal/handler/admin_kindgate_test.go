package handler_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/handler"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

const (
	kgAggA = "A100000000000000000000000000000000000001"
	kgAggB = "A200000000000000000000000000000000000002"
	kgDevA = "D100000000000000000000000000000000000001"
	kgDevB = "D200000000000000000000000000000000000002"
	kgNew  = "E100000000000000000000000000000000000001"
	kgDevC = "D300000000000000000000000000000000000003"
)

type kindFixture struct {
	mgmt     *handler.AdminManagementHandler
	profiles *handler.AdminDeviceProfileHandler
	pairs    *memory.EndDeviceManagementStore
	store    *memory.DeviceProfileStore
}

func newKindFixture(t *testing.T) kindFixture {
	t.Helper()
	pairs := memory.NewEndDeviceManagementStore()
	ps := memory.NewDeviceProfileStore()
	return kindFixture{
		mgmt:     &handler.AdminManagementHandler{Managers: pairs, Profiles: ps},
		profiles: &handler.AdminDeviceProfileHandler{Profiles: ps, Managers: pairs},
		pairs:    pairs,
		store:    ps,
	}
}

func (f kindFixture) setKind(t *testing.T, lfdi, kind string) {
	t.Helper()
	if err := f.store.Put(t.Context(), store.DeviceProfile{LFDI: lfdi, Kind: kind}); err != nil {
		t.Fatalf("seed profile %s: %v", lfdi, err)
	}
}

func (f kindFixture) create(t *testing.T, manager, managed string) (int, string) {
	t.Helper()
	w := doJSON(t, f.mgmt.HandleCreateManagementPair(), http.MethodPost, "/api/management-pairs",
		`{"managerLFDI":"`+manager+`","managedLFDI":"`+managed+`"}`)
	return w.Code, w.Body.String()
}

func (f kindFixture) rekey(t *testing.T, role, from, to string) (int, string) {
	t.Helper()
	w := doJSON(t, f.mgmt.HandleRekeyManagementPair(), http.MethodPost, "/api/management-pairs/rekey",
		`{"role":"`+role+`","from":"`+from+`","to":"`+to+`"}`)
	return w.Code, w.Body.String()
}

func (f kindFixture) assertManagerOf(t *testing.T, managed, want string) {
	t.Helper()
	got, err := f.pairs.ManagerOf(t.Context(), managed)
	if want == "" {
		if err == nil {
			t.Errorf("ManagerOf(%s) = %q, want no pair", managed, got)
		}
		return
	}
	if err != nil || got != want {
		t.Errorf("ManagerOf(%s) = %q, %v; want %q", managed, got, err, want)
	}
}

func TestCreatePairRefusesDeviceManager(t *testing.T) {
	f := newKindFixture(t)
	f.setKind(t, kgDevA, store.DeviceKindDevice)
	code, body := f.create(t, kgDevA, kgDevB)
	if code != http.StatusConflict || !strings.Contains(body, kgDevA) {
		t.Fatalf("create = %d %s, want 409 naming %s", code, body, kgDevA)
	}
	f.assertManagerOf(t, kgDevB, "")
}

func TestCreatePairRefusesAggregatorManaged(t *testing.T) {
	f := newKindFixture(t)
	f.setKind(t, kgAggA, store.DeviceKindAggregator)
	f.setKind(t, kgAggB, store.DeviceKindAggregator)
	code, body := f.create(t, kgAggA, kgAggB)
	if code != http.StatusConflict || !strings.Contains(body, kgAggB) {
		t.Fatalf("create = %d %s, want 409 naming %s", code, body, kgAggB)
	}
	f.assertManagerOf(t, kgAggB, "")
}

func TestCreatePairRefusesManagedThatManagesDevices(t *testing.T) {
	// No profile for kgAggB: it reads as an aggregator only because it manages.
	f := newKindFixture(t)
	if err := f.pairs.Assign(t.Context(), kgAggB, kgDevB); err != nil {
		t.Fatal(err)
	}
	code, body := f.create(t, kgAggA, kgAggB)
	if code != http.StatusConflict || !strings.Contains(body, kgAggB) {
		t.Fatalf("create = %d %s, want 409 naming %s", code, body, kgAggB)
	}
	f.assertManagerOf(t, kgAggB, "")
}

func TestCreatePairKeepsLegacyAndUnclassifiedWorking(t *testing.T) {
	f := newKindFixture(t)
	// Pair on disk with no profile: the manager reads as an aggregator.
	if err := f.pairs.Assign(t.Context(), kgAggA, kgDevA); err != nil {
		t.Fatal(err)
	}
	if code, body := f.create(t, kgAggA, kgDevB); code != http.StatusCreated {
		t.Fatalf("second pair for legacy manager = %d %s, want 201", code, body)
	}
	f.assertManagerOf(t, kgDevB, kgAggA)
	// A manager nobody has classified is not refused either.
	if code, body := f.create(t, kgNew, kgAggB); code != http.StatusCreated {
		t.Fatalf("pair for unclassified manager = %d %s, want 201", code, body)
	}
	// A profile with no kind set is not a device.
	f.setKind(t, kgDevB, "")
	if code, body := f.create(t, kgDevB, kgDevC); code != http.StatusCreated {
		t.Fatalf("pair for manager with empty kind = %d %s, want 201", code, body)
	}
}

func TestRekeyRefusesByKind(t *testing.T) {
	f := newKindFixture(t)
	if err := f.pairs.Assign(t.Context(), kgAggA, kgDevA); err != nil {
		t.Fatal(err)
	}
	f.setKind(t, kgDevB, store.DeviceKindDevice)
	f.setKind(t, kgAggB, store.DeviceKindAggregator)

	code, body := f.rekey(t, "manager", kgAggA, kgDevB)
	if code != http.StatusConflict || !strings.Contains(body, kgDevB) {
		t.Errorf("rekey manager to device = %d %s, want 409 naming %s", code, body, kgDevB)
	}
	f.assertManagerOf(t, kgDevA, kgAggA)

	code, body = f.rekey(t, "managed", kgDevA, kgAggB)
	if code != http.StatusConflict || !strings.Contains(body, kgAggB) {
		t.Errorf("rekey managed to aggregator = %d %s, want 409 naming %s", code, body, kgAggB)
	}
	f.assertManagerOf(t, kgDevA, kgAggA)
	f.assertManagerOf(t, kgAggB, "")

	// The allowed moves still land.
	if code, body = f.rekey(t, "manager", kgAggA, kgNew); code != http.StatusOK {
		t.Errorf("rekey manager to unclassified = %d %s, want 200", code, body)
	}
	f.assertManagerOf(t, kgDevA, kgNew)
	if code, body = f.rekey(t, "managed", kgDevA, kgDevB); code != http.StatusOK {
		t.Errorf("rekey managed to device = %d %s, want 200", code, body)
	}
	f.assertManagerOf(t, kgDevB, kgNew)
}

func TestPutProfileKindChangeRefusals(t *testing.T) {
	f := newKindFixture(t)
	if err := f.pairs.Assign(t.Context(), kgAggA, kgDevA); err != nil {
		t.Fatal(err)
	}
	put := f.profiles.HandlePutDeviceProfile()

	w := putProfile(put, kgDevA, `{"name":"Meter","kind":"aggregator"}`)
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), kgAggA) {
		t.Errorf("managed device -> aggregator = %d %s, want 409 naming %s", w.Code, w.Body.String(), kgAggA)
	}
	if _, err := f.store.Get(t.Context(), kgDevA); err == nil {
		t.Error("refused PUT stored a profile for the managed device")
	}

	w = putProfile(put, kgAggA, `{"name":"Gateway","kind":"device"}`)
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), kgDevA) {
		t.Errorf("managing aggregator -> device = %d %s, want 409 naming %s", w.Code, w.Body.String(), kgDevA)
	}
	if _, err := f.store.Get(t.Context(), kgAggA); err == nil {
		t.Error("refused PUT stored a profile for the managing aggregator")
	}
}

func TestPutProfileKindChangeAllowed(t *testing.T) {
	f := newKindFixture(t)
	if err := f.pairs.Assign(t.Context(), kgAggA, kgDevA); err != nil {
		t.Fatal(err)
	}
	put := f.profiles.HandlePutDeviceProfile()
	cases := []struct{ name, lfdi, body, kind string }{
		{"unmanaged device to aggregator", kgNew, `{"name":"New","kind":"aggregator"}`, "aggregator"},
		{"managing aggregator confirmed", kgAggA, `{"name":"Gateway","kind":"aggregator"}`, "aggregator"},
		{"managed device confirmed", kgDevA, `{"name":"Meter","kind":"device"}`, "device"},
		{"managing aggregator clears kind", kgAggA, `{"name":"Gateway"}`, ""},
	}
	for _, c := range cases {
		if w := putProfile(put, c.lfdi, c.body); w.Code != http.StatusOK {
			t.Errorf("%s = %d %s, want 200", c.name, w.Code, w.Body.String())
			continue
		}
		got, err := f.store.Get(t.Context(), c.lfdi)
		if err != nil || got.Kind != c.kind {
			t.Errorf("%s: stored %+v, %v; want kind %q", c.name, got, err, c.kind)
		}
	}
}

func TestKindGateOffWithoutItsStore(t *testing.T) {
	pairs := memory.NewEndDeviceManagementStore()
	ps := memory.NewDeviceProfileStore()
	if err := ps.Put(t.Context(), store.DeviceProfile{LFDI: kgDevA, Kind: store.DeviceKindDevice}); err != nil {
		t.Fatal(err)
	}
	h := &handler.AdminManagementHandler{Managers: pairs}
	w := doJSON(t, h.HandleCreateManagementPair(), http.MethodPost, "/api/management-pairs",
		`{"managerLFDI":"`+kgDevA+`","managedLFDI":"`+kgDevB+`"}`)
	if w.Code != http.StatusCreated {
		t.Errorf("create with no profile store = %d %s, want 201", w.Code, w.Body.String())
	}
	if err := pairs.Assign(t.Context(), kgAggA, kgDevA); err != nil {
		t.Fatal(err)
	}
	ph := &handler.AdminDeviceProfileHandler{Profiles: ps}
	if w := putProfile(ph.HandlePutDeviceProfile(), kgDevA, `{"kind":"aggregator"}`); w.Code != http.StatusOK {
		t.Errorf("PUT with no pair store = %d %s, want 200", w.Code, w.Body.String())
	}
}
