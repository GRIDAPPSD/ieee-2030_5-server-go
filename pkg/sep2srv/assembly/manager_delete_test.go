package assembly_test

import (
	"context"
	"errors"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/assembly"
	coreedev "github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/handlers/enddevice"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
)

// DELETE /edev/{id} by a manager removes a managed device and its pair
// (#1087 slice 2), driven through the assembled router.

func managerDeleteServer(t *testing.T, cfg assembly.RouterConfig, fleet managementFleet) *httptest.Server {
	t.Helper()
	handler, _ := assembly.BuildProtocolRouter(cfg, fleet.stores, gateTestPolicy(), "serverSFDI", "serverLFDI", nil)
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return srv
}

func assertPairHeld(t *testing.T, fleet managementFleet, managed, wantManager string) {
	t.Helper()
	got, err := fleet.managers.ManagerOf(context.Background(), managed)
	if err != nil || got != wantManager {
		t.Errorf("ManagerOf(%s) = %q, %v; want %q", managed, got, err, wantManager)
	}
}

func assertRecordHeld(t *testing.T, fleet managementFleet, id, wantLFDI string) {
	t.Helper()
	dev, err := fleet.stores.EndDevices.Get(context.Background(), id)
	if err != nil || dev.LFDI != wantLFDI {
		t.Errorf("EndDevice %s = LFDI %q, %v; want the record kept with LFDI %q", id, dev.LFDI, err, wantLFDI)
	}
}

func TestManagerDelete_RemovesTheRecordItsCascadeAndOnlyItsOwnPair(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fleet := newManagementFleet(t)
	if err := fleet.stores.Registrations.Create(ctx, victimID, sep2.Registration{PIN: testFixturePIN, DateTimeRegistered: 1600000000}); err != nil {
		t.Fatalf("seed Registration: %v", err)
	}
	srv := managerDeleteServer(t, assembly.RouterConfig{}, fleet)

	status, raw := gateRequest(t, srv, http.MethodDelete, "/edev/"+victimID, managerLFDI, "")
	if status != http.StatusNoContent {
		t.Fatalf("manager DELETE: status %d, want 204; body=%q", status, raw)
	}

	if _, err := fleet.stores.EndDevices.Get(ctx, victimID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("record after the delete: err=%v, want ErrNotFound", err)
	}
	if _, err := fleet.stores.Registrations.Get(ctx, victimID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("Registration after the delete: err=%v, want ErrNotFound (the cascade ran)", err)
	}
	if status, _ := gateRequest(t, srv, http.MethodGet, "/edev/"+victimID, managerLFDI, ""); status != http.StatusNotFound {
		t.Errorf("manager GET after the delete: status %d, want 404", status)
	}
	if _, err := fleet.managers.ManagerOf(ctx, victimLFDI); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("ManagerOf(deleted device) = %v, want ErrNotFound: the pair must go with the record", err)
	}
	assertPairHeld(t, fleet, secondChildLFDI, managerLFDI)
	assertPairHeld(t, fleet, callerLFDI, otherManagerLFDI)
	assertRecordHeld(t, fleet, secondChildID, secondChildLFDI)
}

func TestManagerDelete_RefusesWhoDoesNotManageTheDeviceAndChangesNothing(t *testing.T) {
	t.Parallel()
	fleet := newManagementFleet(t)
	srv := managerDeleteServer(t, assembly.RouterConfig{}, fleet)

	for name, caller := range map[string]string{
		"another manager":              otherManagerLFDI,
		"an unrelated device":          unmanagedLFDI,
		"the managed device's sibling": secondChildLFDI,
	} {
		status, raw := gateRequest(t, srv, http.MethodDelete, "/edev/"+victimID, caller, "")
		if status != http.StatusForbidden {
			t.Errorf("%s DELETE: status %d, want 403; body=%q", name, status, raw)
		}
		assertNoFleetData(t, name, raw)
	}
	assertRecordHeld(t, fleet, victimID, victimLFDI)
	assertPairHeld(t, fleet, victimLFDI, managerLFDI)
}

func TestManagerDelete_OptionOffRefusesAndChangesNothing(t *testing.T) {
	t.Parallel()
	fleet := newManagementFleet(t)
	srv := managerDeleteServer(t, assembly.RouterConfig{InBandManagementOff: true}, fleet)

	status, raw := gateRequest(t, srv, http.MethodDelete, "/edev/"+victimID, managerLFDI, "")
	if status != http.StatusForbidden {
		t.Fatalf("manager DELETE with the option off: status %d, want 403; body=%q", status, raw)
	}
	assertRecordHeld(t, fleet, victimID, victimLFDI)
	assertPairHeld(t, fleet, victimLFDI, managerLFDI)

	if status, raw := gateRequest(t, srv, http.MethodDelete, "/edev/"+victimID, victimLFDI, ""); status != http.StatusNoContent {
		t.Errorf("the device deleting itself with the option off: status %d, want 204; body=%q", status, raw)
	}
}

func TestManagerDelete_ADeviceDeletingItselfLeavesItsPair(t *testing.T) {
	t.Parallel()
	fleet := newManagementFleet(t)
	srv := managerDeleteServer(t, assembly.RouterConfig{}, fleet)

	if status, raw := gateRequest(t, srv, http.MethodDelete, "/edev/"+victimID, victimLFDI, ""); status != http.StatusNoContent {
		t.Fatalf("self DELETE: status %d, want 204; body=%q", status, raw)
	}
	assertPairHeld(t, fleet, victimLFDI, managerLFDI)
}

// rekeyBeforeRelease moves the pair to another manager just before Release
// runs, which is a rekey landing between the gate's admission and the handler.
type rekeyBeforeRelease struct {
	managementFleet
	newManager string
}

func (r rekeyBeforeRelease) ManagerOf(ctx context.Context, managed string) (string, error) {
	return r.managers.ManagerOf(ctx, managed)
}

func (r rekeyBeforeRelease) ManagedBy(ctx context.Context, manager string) ([]string, error) {
	return r.managers.ManagedBy(ctx, manager)
}

func (r rekeyBeforeRelease) Assign(ctx context.Context, manager, managed string) error {
	return r.managers.Assign(ctx, manager, managed)
}

func (r rekeyBeforeRelease) Unassign(ctx context.Context, managed string) error {
	return r.managers.Unassign(ctx, managed)
}

func (r rekeyBeforeRelease) Release(ctx context.Context, manager, managed string) error {
	if err := r.managers.Unassign(ctx, managed); err != nil {
		return err
	}
	if err := r.managers.Assign(ctx, r.newManager, managed); err != nil {
		return err
	}
	return r.managers.Release(ctx, manager, managed)
}

func TestManagerDelete_APairRekeyedAfterTheGateIsNotRemoved(t *testing.T) {
	t.Parallel()
	fleet := newManagementFleet(t)
	fleet.stores.EndDeviceManagers = rekeyBeforeRelease{managementFleet: fleet, newManager: otherManagerLFDI}
	srv := managerDeleteServer(t, assembly.RouterConfig{}, fleet)

	status, raw := gateRequest(t, srv, http.MethodDelete, "/edev/"+victimID, managerLFDI, "")
	if status != http.StatusForbidden {
		t.Fatalf("manager DELETE after a rekey: status %d, want 403; body=%q", status, raw)
	}
	assertRecordHeld(t, fleet, victimID, victimLFDI)
	assertPairHeld(t, fleet, victimLFDI, otherManagerLFDI)
}

// deleteFailingDevices fails the record delete and nothing else.
type deleteFailingDevices struct{ store.EndDeviceStore }

func (deleteFailingDevices) Delete(context.Context, string) error {
	return errors.New("backend unavailable")
}

func TestManagerDelete_AFailedRecordDeleteAfterTheReleaseLeavesNoPairAndAnswers500(t *testing.T) {
	t.Parallel()
	fleet := newManagementFleet(t)
	fleet.stores.EndDevices = deleteFailingDevices{fleet.stores.EndDevices}
	srv := managerDeleteServer(t, assembly.RouterConfig{}, fleet)

	status, raw := gateRequest(t, srv, http.MethodDelete, "/edev/"+victimID, managerLFDI, "")
	if status != http.StatusInternalServerError {
		t.Fatalf("manager DELETE with a failing record delete: status %d, want 500; body=%q", status, raw)
	}
	if _, err := fleet.managers.ManagerOf(context.Background(), victimLFDI); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("ManagerOf after the failed delete = %v, want ErrNotFound: the release comes first and stays", err)
	}
	assertPairHeld(t, fleet, secondChildLFDI, managerLFDI)
}

// Not parallel: it captures the process log.
func TestManagerDelete_LogsTheReleasedPairAndALostGrantWhenTheDeleteFails(t *testing.T) {
	fleet := newManagementFleet(t)
	fleet.stores.EndDevices = deleteFailingDevices{fleet.stores.EndDevices}
	srv := managerDeleteServer(t, assembly.RouterConfig{}, fleet)
	var buf strings.Builder
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })

	if status, _ := gateRequest(t, srv, http.MethodDelete, "/edev/"+victimID, managerLFDI, ""); status != http.StatusInternalServerError {
		t.Fatalf("status %d, want 500", status)
	}
	var released, lost bool
	for _, l := range strings.Split(buf.String(), "\n") {
		named := strings.Contains(l, managerLFDI) && strings.Contains(l, victimID)
		released = released || (named && strings.Contains(l, "released the pair"))
		lost = lost || (named && strings.Contains(l, "lost its pair"))
	}
	if !released || !lost {
		t.Errorf("log names the manager and id for the release (%v) and the failed delete (%v); log=%q", released, lost, buf.String())
	}
}

// A value already on the request context must not turn a device's own delete
// into a manager delete: the gate is the only writer.
func TestManagerDelete_APreSetDelegatedValueDoesNotTurnASelfDeleteIntoAManagerDelete(t *testing.T) {
	t.Parallel()
	fleet := newManagementFleet(t)
	policy := gateTestPolicy()
	inner := policy.Wrap
	policy.Wrap = func(h http.Handler) http.Handler {
		return inner(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			h.ServeHTTP(w, r.WithContext(coreedev.WithDelegatedAccess(r.Context(), managerLFDI)))
		}))
	}
	handler, _ := assembly.BuildProtocolRouter(assembly.RouterConfig{}, fleet.stores, policy, "serverSFDI", "serverLFDI", nil)
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	if status, raw := gateRequest(t, srv, http.MethodDelete, "/edev/"+victimID, victimLFDI, ""); status != http.StatusNoContent {
		t.Fatalf("self DELETE: status %d, want 204; body=%q", status, raw)
	}
	assertPairHeld(t, fleet, victimLFDI, managerLFDI)
}
