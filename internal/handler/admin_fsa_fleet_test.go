package handler_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/handler"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

// #1154: the server-managed fleet FSA of one aggregator.

const (
	fleetAgg = "AAAA000000000000000000000000000000000001"
	fleetL1  = "BBBB000000000000000000000000000000000001"
	fleetL2  = "BBBB000000000000000000000000000000000002"
	fleetL3  = "BBBB000000000000000000000000000000000003"
	fleetL4  = "BBBB000000000000000000000000000000000004"
)

type fleetFSAFixture struct {
	*templateFixture
	devices  *memory.EndDeviceStore
	managers *memory.EndDeviceManagementStore
}

// newFleetFSAFixture builds the template fixture over an EndDeviceStore holding
// the aggregator (store id 9) and devices 1 to 3 under fleetL1 to fleetL3. The
// pair store starts empty, and fleetL4 has no EndDevice record.
func newFleetFSAFixture(t *testing.T) *fleetFSAFixture {
	t.Helper()
	devs := memory.NewEndDeviceStore()
	for id, lfdi := range map[string]string{"9": fleetAgg, "1": fleetL1, "2": fleetL2, "3": fleetL3} {
		if err := devs.Create(context.Background(), id, sep2.EndDevice{SubscribableResource: sep2.SubscribableResource{Resource: sep2.Resource{Href: "/edev/" + id}}, LFDI: lfdi}); err != nil {
			t.Fatal(err)
		}
	}
	f := newTemplateFixtureWith(t, memory.NewDERProgramStore(), devs)
	mgrs := memory.NewEndDeviceManagementStore()
	f.h.Managers = mgrs
	f.h.FleetDevices = devs
	f.mux.HandleFunc("DELETE /api/fsas/{id}", f.h.HandleDeleteAdminFSA())
	return &fleetFSAFixture{templateFixture: f, devices: devs, managers: mgrs}
}

func (f *fleetFSAFixture) pair(t *testing.T, managed ...string) {
	t.Helper()
	for _, m := range managed {
		if err := f.managers.Assign(context.Background(), fleetAgg, m); err != nil {
			t.Fatal(err)
		}
	}
}

func (f *fleetFSAFixture) fleet(t *testing.T) string {
	t.Helper()
	id, err := f.h.FleetFSA(httptest.NewRequest(http.MethodPost, "/fleet", nil), fleetAgg)
	if err != nil {
		t.Fatalf("FleetFSA: %v", err)
	}
	return id
}

func TestFleetFSA_CreatedOnceWithOneProgramTemplate(t *testing.T) {
	t.Parallel()
	f := newFleetFSAFixture(t)
	ctx := context.Background()

	id := f.fleet(t)
	if again := f.fleet(t); again != id {
		t.Fatalf("second call returned fsa %q, want the same %q", again, id)
	}
	if got := f.h.AdminFSAs.FleetAggregator(id); got != fleetAgg {
		t.Errorf("FleetAggregator(%q) = %q, want %q", id, got, fleetAgg)
	}
	if got, ok := f.h.AdminFSAs.FleetFSA(fleetAgg); !ok || got != id {
		t.Errorf("FleetFSA(aggregator) = %q, %v, want %q", got, ok, id)
	}
	// "fsa-A" from the fixture is operator-made and plus the fleet is all there is.
	if n := len(f.h.AdminFSAs.List(ctx)); n != 2 {
		t.Errorf("FSAs after two calls = %d, want 2 (the fixture's and one fleet)", n)
	}
	tmpls := f.h.AdminFSAs.Templates(ctx, id)
	if len(tmpls) != 1 {
		t.Fatalf("templates = %d, want exactly 1", len(tmpls))
	}
	if tmpls[0].Primacy != 65 || tmpls[0].Description != "Fleet control" {
		t.Errorf("template = primacy %d, description %q, want 65 and %q", tmpls[0].Primacy, tmpls[0].Description, "Fleet control")
	}
	if f.h.AdminFSAs.FleetAggregator("fsa-A") != "" {
		t.Error("the operator-made FSA reads as a fleet")
	}
}

func TestFleetFSA_AssignmentFollowsThePairs(t *testing.T) {
	t.Parallel()
	f := newFleetFSAFixture(t)
	ctx := context.Background()
	f.pair(t, fleetL1, fleetL2)

	id := f.fleet(t)
	if got := f.h.AdminFSAs.Devices(ctx, id); !slices.Equal(got, []string{"1", "2"}) {
		t.Fatalf("assigned after pairing L1, L2 = %v, want [1 2]", got)
	}
	tmplID := f.h.AdminFSAs.Templates(ctx, id)[0].ID
	for _, dev := range []string{"1", "2"} {
		if _, err := f.h.DeviceFSAs.Get(ctx, dev, id); err != nil {
			t.Errorf("device %s holds no scoped FSA: %v", dev, err)
		}
		if _, err := f.copies.Programs.Get(ctx, dev, tmplID); err != nil {
			t.Errorf("device %s holds no copy of the fleet program: %v", dev, err)
		}
	}

	f.pair(t, fleetL3)
	f.fleet(t)
	if got := f.h.AdminFSAs.Devices(ctx, id); !slices.Equal(got, []string{"1", "2", "3"}) {
		t.Fatalf("assigned after adding L3 = %v, want [1 2 3]", got)
	}

	if err := f.managers.Unassign(ctx, fleetL1); err != nil {
		t.Fatal(err)
	}
	f.fleet(t)
	if got := f.h.AdminFSAs.Devices(ctx, id); !slices.Equal(got, []string{"2", "3"}) {
		t.Fatalf("assigned after removing L1 = %v, want [2 3]", got)
	}
	if _, err := f.copies.Programs.Get(ctx, "1", tmplID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("removed device 1 still holds the fleet program: %v", err)
	}
	if _, err := f.h.DeviceFSAs.Get(ctx, "1", id); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("removed device 1 still holds the scoped FSA: %v", err)
	}
}

func TestFleetFSA_ManagedDeviceWithoutRecordIsSkipped(t *testing.T) {
	t.Parallel()
	f := newFleetFSAFixture(t)
	f.pair(t, fleetL1, fleetL4)

	id := f.fleet(t)
	if got := f.h.AdminFSAs.Devices(context.Background(), id); !slices.Equal(got, []string{"1"}) {
		t.Errorf("assigned = %v, want [1]: L4 has no EndDevice record", got)
	}
}

// selfListing reports the aggregator among its own managed devices, which the
// pair store refuses to record, so the test can reach the guard behind it.
type selfListing struct {
	store.EndDeviceManagementReader
}

func (s selfListing) ManagedBy(ctx context.Context, m string) ([]string, error) {
	out, err := s.EndDeviceManagementReader.ManagedBy(ctx, m)
	return append([]string{m}, out...), err
}

func TestFleetFSA_AggregatorsOwnEndDeviceIsNeverAssigned(t *testing.T) {
	t.Parallel()
	f := newFleetFSAFixture(t)
	ctx := context.Background()
	f.pair(t, fleetL1)
	f.h.Managers = selfListing{f.managers}

	id := f.fleet(t)
	if got := f.h.AdminFSAs.Devices(ctx, id); !slices.Equal(got, []string{"1"}) {
		t.Fatalf("assigned = %v, want [1] without the aggregator's own device 9", got)
	}

	// A stale assignment of the aggregator is removed by the next reconcile.
	if err := f.h.AdminFSAs.AssignDevice(ctx, id, "9"); err != nil {
		t.Fatal(err)
	}
	f.fleet(t)
	if got := f.h.AdminFSAs.Devices(ctx, id); !slices.Equal(got, []string{"1"}) {
		t.Errorf("assigned after a stale assignment of 9 = %v, want [1]", got)
	}
}

func TestFleetFSA_AdminRoutesRefuseDeleteAndAssignmentChanges(t *testing.T) {
	t.Parallel()
	f := newFleetFSAFixture(t)
	ctx := context.Background()
	f.pair(t, fleetL1)
	id := f.fleet(t)

	cases := []struct {
		name, method, target, body string
	}{
		{"delete", http.MethodDelete, "/api/fsas/" + id, ""},
		{"assign", http.MethodPost, "/api/devices/2/fsa-assignment", `{"fsaHref":"/api/fsas/` + id + `"}`},
		{"unassign", http.MethodDelete, "/api/devices/1/fsa-assignment?fsaHref=/api/fsas/" + id, ""},
	}
	for _, tc := range cases {
		w := f.do(tc.method, tc.target, tc.body)
		if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), `"code":"fsa_server_managed"`) {
			t.Errorf("%s: %d %s, want 409 with code fsa_server_managed", tc.name, w.Code, w.Body.String())
		}
	}
	if _, err := f.h.AdminFSAs.Get(ctx, id); err != nil {
		t.Errorf("the fleet FSA is gone after a refused delete: %v", err)
	}
	if got := f.h.AdminFSAs.Devices(ctx, id); !slices.Equal(got, []string{"1"}) {
		t.Errorf("assigned after refused changes = %v, want [1]", got)
	}

	// Control: the same routes still work on an operator-made FSA.
	f.assign(t, "2", "fsa-A")
	if w := f.do(http.MethodDelete, "/api/devices/2/fsa-assignment?fsaHref=/api/fsas/fsa-A", ""); w.Code != http.StatusNoContent {
		t.Errorf("unassign on the operator-made FSA = %d %s, want 204", w.Code, w.Body.String())
	}
	if w := f.do(http.MethodDelete, "/api/fsas/fsa-A", ""); w.Code != http.StatusNoContent {
		t.Errorf("delete of the operator-made FSA = %d %s, want 204", w.Code, w.Body.String())
	}
}

func TestFleetFSA_ProgramPrimacyStaysEditable(t *testing.T) {
	t.Parallel()
	f := newFleetFSAFixture(t)
	ctx := context.Background()
	f.pair(t, fleetL1)
	id := f.fleet(t)
	tmplID := f.h.AdminFSAs.Templates(ctx, id)[0].ID

	w := f.do(http.MethodPut, "/api/fsas/"+id+"/der-programs/"+tmplID, `{"description":"Fleet control","primacy":70}`)
	if w.Code != http.StatusOK {
		t.Fatalf("primacy edit = %d %s, want 200", w.Code, w.Body.String())
	}
	if got := f.h.AdminFSAs.Templates(ctx, id)[0].Primacy; got != 70 {
		t.Errorf("template primacy = %d, want 70", got)
	}
	p, err := f.copies.Programs.Get(ctx, "1", tmplID)
	if err != nil || p.Primacy != 70 {
		t.Errorf("device 1 copy primacy = %d, %v, want 70", p.Primacy, err)
	}
}

func TestFleetFSA_UnwiredAndFailingStoresFailClosed(t *testing.T) {
	t.Parallel()
	f := newFleetFSAFixture(t)
	f.pair(t, fleetL1)
	id := f.fleet(t)

	f.h.Managers = failingManagers{}
	if _, err := f.h.FleetFSA(httptest.NewRequest(http.MethodPost, "/fleet", nil), fleetAgg); err == nil {
		t.Error("a failing ManagedBy returned no error")
	}
	if got := f.h.AdminFSAs.Devices(context.Background(), id); !slices.Equal(got, []string{"1"}) {
		t.Errorf("assigned after a failing ManagedBy = %v, want the earlier [1] untouched", got)
	}

	f.h.Managers = nil
	if _, err := f.h.FleetFSA(httptest.NewRequest(http.MethodPost, "/fleet", nil), fleetAgg); !errors.Is(err, handler.ErrFleetUnwired) {
		t.Errorf("unwired Managers: %v, want ErrFleetUnwired", err)
	}
}

type failingManagers struct {
	store.EndDeviceManagementReader
}

func (failingManagers) ManagedBy(context.Context, string) ([]string, error) {
	return nil, errors.New("pairs unreadable")
}

// Reconciles and operator assignments all take programMu, so a pair change
// racing reconciles ends with exactly the managed devices assigned, one fleet
// FSA, and every operator assignment to the fleet refused.
func TestFleetFSA_ConcurrentReconcilesAndAssignsLeaveTheManagedSet(t *testing.T) {
	t.Parallel()
	f := newFleetFSAFixture(t)
	ctx := context.Background()
	f.pair(t, fleetL1)
	id := f.fleet(t)

	var wg sync.WaitGroup
	for i := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := f.h.FleetFSA(httptest.NewRequest(http.MethodPost, "/fleet", nil), fleetAgg); err != nil {
				t.Errorf("FleetFSA: %v", err)
			}
		}()
		wg.Add(1)
		go func() {
			defer wg.Done()
			if w := f.do(http.MethodPost, "/api/devices/3/fsa-assignment", `{"fsaHref":"/api/fsas/`+id+`"}`); w.Code != http.StatusConflict {
				t.Errorf("operator assign to the fleet = %d, want 409", w.Code)
			}
		}()
		if i == 3 {
			f.pair(t, fleetL2)
		}
	}
	wg.Wait()
	f.fleet(t)

	if got := f.h.AdminFSAs.Devices(ctx, id); !slices.Equal(got, []string{"1", "2"}) {
		t.Errorf("assigned = %v, want exactly the managed [1 2], never the refused 3", got)
	}
	fleets := 0
	for _, fsaID := range f.h.AdminFSAs.IDs(ctx) {
		if f.h.AdminFSAs.FleetAggregator(fsaID) == fleetAgg {
			fleets++
		}
	}
	if fleets != 1 {
		t.Errorf("fleet FSAs for the aggregator = %d, want 1", fleets)
	}
}
