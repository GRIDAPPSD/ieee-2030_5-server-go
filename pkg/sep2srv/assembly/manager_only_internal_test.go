package assembly

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/identity"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

// TestManagerOnlyWritesNameOnlyMountedPatterns fails when a row outlives its
// route, which would read as live policy while granting nothing.
func TestManagerOnlyWritesNameOnlyMountedPatterns(t *testing.T) {
	t.Parallel()
	stores := fullStores()
	policy := AuthPolicy{Identity: ownerIdentity("OWNER")}
	_, patterns := BuildProtocolRouter(RouterConfig{}, stores, policy, "serverSFDI", "serverLFDI", nil)

	mounted := map[string]bool{}
	for _, p := range patterns {
		mounted[p] = true
	}
	if len(managerOnlyWrites) == 0 {
		t.Fatal("managerOnlyWrites is empty; the stale-row check would pass vacuously")
	}
	if !mounted["GET /mup"] {
		t.Fatal("control: the router did not report GET /mup, so the pattern list cannot be trusted")
	}
	for p := range managerOnlyWrites {
		if !mounted[p] {
			t.Errorf("managerOnlyWrites names %q, which is not a currently mounted pattern; remove the stale row", p)
		}
	}
}

// failDevices fails Get and GetByLFDI, the two reads the manager-only check
// makes on the EndDevice store.
type failDevices struct {
	store.EndDeviceStore
	err error
}

func (f failDevices) Get(context.Context, string) (sep2.EndDevice, error) {
	return sep2.EndDevice{}, f.err
}

func (f failDevices) GetByLFDI(context.Context, string) (sep2.EndDevice, error) {
	return sep2.EndDevice{}, f.err
}

// failManagers fails the two reads the manager-only check makes on the
// management store.
type failManagers struct {
	store.EndDeviceManagementStore
	err error
}

func (f failManagers) ManagerOf(context.Context, string) (string, error) { return "", f.err }

func (f failManagers) ManagedBy(context.Context, string) ([]string, error) { return nil, f.err }

const (
	devLFDI   = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA1"
	mgrLFDI   = "BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB2"
	otherLFDI = "CCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCC3"
)

// managerOnlyFixture holds one live device "1" (LFDI devLFDI) managed by mgrLFDI.
func managerOnlyFixture(t *testing.T) (store.EndDeviceStore, store.EndDeviceManagementStore) {
	t.Helper()
	devs := memory.NewEndDeviceStore()
	dev := sep2.EndDevice{LFDI: devLFDI}
	dev.Href = "/edev/1"
	if err := devs.Create(context.Background(), "1", dev); err != nil {
		t.Fatalf("seed device: %v", err)
	}
	mgrs := memory.NewEndDeviceManagementStore()
	if err := mgrs.Assign(context.Background(), mgrLFDI, devLFDI); err != nil {
		t.Fatalf("seed pair: %v", err)
	}
	return devs, mgrs
}

// TestDecideManagerOnly drives every branch of decideManagerOnly directly,
// including the failure branches that no route test reaches: an outage must be
// ownershipStoreFailed (a 500) and an unwired store must refuse, never admit.
func TestDecideManagerOnly(t *testing.T) {
	t.Parallel()
	boom := errors.New("store down")
	devs, mgrs := managerOnlyFixture(t)

	cases := []struct {
		name     string
		devices  store.EndDeviceStore
		managers store.EndDeviceManagementStore
		identity func(context.Context) (string, string, bool)
		id       string
		names    bool
		want     ownershipDecision
		reason   string
		errIs    error
	}{
		{name: "manager of the device", devices: devs, managers: mgrs, identity: ownerIdentity(mgrLFDI), id: "1", names: true, want: ownershipAllowed},
		{name: "manager with a live pair, no device named", devices: devs, managers: mgrs, identity: ownerIdentity(mgrLFDI), want: ownershipAllowed},
		{name: "device reads itself as no manager", devices: devs, managers: mgrs, identity: ownerIdentity(devLFDI), id: "1", names: true, want: ownershipDenied, reason: reasonNotManagerCert},
		{name: "another manager", devices: devs, managers: mgrs, identity: ownerIdentity(otherLFDI), id: "1", names: true, want: ownershipDenied, reason: reasonNotManagerCert},
		{name: "absent device is refused like an unmanaged one", devices: devs, managers: mgrs, identity: ownerIdentity(mgrLFDI), id: "9", names: true, want: ownershipDenied, reason: reasonAbsent},
		{name: "no live pair, no device named", devices: devs, managers: mgrs, identity: ownerIdentity(otherLFDI), want: ownershipDenied, reason: reasonNotManagerCert},
		{name: "no identity", devices: devs, managers: mgrs, identity: nil, id: "1", names: true, want: ownershipDenied, reason: reasonNoIdentity},
		{name: "management store not wired", devices: devs, managers: nil, identity: ownerIdentity(mgrLFDI), id: "1", names: true, want: ownershipDenied, reason: reasonNotManagerCert},
		{name: "management store not wired, no device named", devices: devs, managers: nil, identity: ownerIdentity(mgrLFDI), want: ownershipDenied, reason: reasonNotManagerCert},
		{name: "device store not wired", devices: nil, managers: mgrs, identity: ownerIdentity(mgrLFDI), id: "1", names: true, want: ownershipStoreFailed},
		{name: "device store not wired, no device named", devices: nil, managers: mgrs, identity: ownerIdentity(mgrLFDI), want: ownershipStoreFailed},
		{name: "device read fails", devices: failDevices{devs, boom}, managers: mgrs, identity: ownerIdentity(mgrLFDI), id: "1", names: true, want: ownershipStoreFailed, errIs: boom},
		{name: "manager lookup fails", devices: devs, managers: failManagers{mgrs, boom}, identity: ownerIdentity(mgrLFDI), id: "1", names: true, want: ownershipStoreFailed, errIs: boom},
		{name: "pair lookup fails, no device named", devices: devs, managers: failManagers{mgrs, boom}, identity: ownerIdentity(mgrLFDI), want: ownershipStoreFailed, errIs: boom},
		{name: "record read fails behind the pair, no device named", devices: failDevices{devs, boom}, managers: mgrs, identity: ownerIdentity(mgrLFDI), want: ownershipStoreFailed, errIs: boom},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var id identity.Func
			if tc.identity != nil {
				id = tc.identity
			}
			g := newOwnershipGate(newRecordingMux(), tc.devices, tc.managers, id)
			req := httptest.NewRequest(http.MethodPut, "/edev/x/rg", nil)
			req.SetPathValue("id", tc.id)

			v := g.decideManagerOnly(req, tc.names)
			if v.decision != tc.want {
				t.Fatalf("decision = %v, want %v (verdict %+v)", v.decision, tc.want, v)
			}
			if tc.reason != "" && v.reason != tc.reason {
				t.Errorf("reason = %q, want %q", v.reason, tc.reason)
			}
			if tc.errIs != nil && !errors.Is(v.err, tc.errIs) {
				t.Errorf("err = %v, want it to wrap %v", v.err, tc.errIs)
			}
			if tc.want == ownershipAllowed && v.manager != mgrLFDI {
				t.Errorf("admitted manager = %q, want the manager", v.manager)
			}
		})
	}
}
