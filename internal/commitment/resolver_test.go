package commitment

import (
	"context"
	"errors"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
)

type fakeDevices map[string]sep2.EndDevice

func (f fakeDevices) Get(_ context.Context, id string) (sep2.EndDevice, error) {
	dev, ok := f[id]
	if !ok {
		return sep2.EndDevice{}, store.ErrNotFound
	}
	return dev, nil
}

// fakeManagers implements store.EndDeviceManagementReader over a plain map
// of managed LFDI to manager LFDI.
type fakeManagers map[string]string

func (f fakeManagers) ManagerOf(_ context.Context, managedLFDI string) (string, error) {
	m, ok := f[managedLFDI]
	if !ok {
		return "", store.ErrNotFound
	}
	return m, nil
}

func (f fakeManagers) ManagedBy(_ context.Context, managerLFDI string) ([]string, error) {
	var out []string
	for managed, manager := range f {
		if manager == managerLFDI {
			out = append(out, managed)
		}
	}
	return out, nil
}

var errManagerLookupFailed = errors.New("fake: manager lookup failed")

// failingManagers always returns a non-ErrNotFound error, to test the fail
// closed path.
type failingManagers struct{}

func (failingManagers) ManagerOf(context.Context, string) (string, error) {
	return "", errManagerLookupFailed
}

func (failingManagers) ManagedBy(context.Context, string) ([]string, error) {
	return nil, errManagerLookupFailed
}

func TestResolver_FleetOf(t *testing.T) {
	t.Parallel()
	const (
		aggregatorID   = "aggregator-edev"
		aggregatorLFDI = "AGG1"
		managedID      = "managed-edev"
		managedLFDI    = "MNG1"
		standaloneID   = "standalone-edev"
		standaloneLFDI = "STD1"
	)

	devices := fakeDevices{
		aggregatorID: sep2.EndDevice{LFDI: aggregatorLFDI},
		managedID:    sep2.EndDevice{LFDI: managedLFDI},
		standaloneID: sep2.EndDevice{LFDI: standaloneLFDI},
	}
	managers := fakeManagers{
		managedLFDI: aggregatorLFDI, // managedID's manager is the aggregator
	}

	r := Resolver{Devices: devices, Managers: managers}
	ctx := context.Background()

	aggFleet, err := r.FleetOf(ctx, aggregatorID)
	if err != nil {
		t.Fatalf("FleetOf(aggregator) error = %v", err)
	}
	if aggFleet != aggregatorLFDI {
		t.Errorf("FleetOf(aggregator) = %q, want its own LFDI %q (unmanaged)", aggFleet, aggregatorLFDI)
	}

	managedFleet, err := r.FleetOf(ctx, managedID)
	if err != nil {
		t.Fatalf("FleetOf(managed) error = %v", err)
	}
	if managedFleet != aggregatorLFDI {
		t.Errorf("FleetOf(managed) = %q, want the manager's LFDI %q", managedFleet, aggregatorLFDI)
	}
	if managedFleet != aggFleet {
		t.Errorf("FleetOf(managed) = %q and FleetOf(aggregator) = %q; a reservation and a control on its managed device must resolve to the same fleet key", managedFleet, aggFleet)
	}

	standaloneFleet, err := r.FleetOf(ctx, standaloneID)
	if err != nil {
		t.Fatalf("FleetOf(standalone) error = %v", err)
	}
	if standaloneFleet != standaloneLFDI {
		t.Errorf("FleetOf(standalone) = %q, want its own LFDI %q (unmanaged)", standaloneFleet, standaloneLFDI)
	}
}

func TestResolver_FleetOf_EmptyLFDIIsError(t *testing.T) {
	t.Parallel()
	devices := fakeDevices{"no-lfdi": sep2.EndDevice{LFDI: ""}}
	r := Resolver{Devices: devices, Managers: fakeManagers{}}

	_, err := r.FleetOf(context.Background(), "no-lfdi")
	if err == nil {
		t.Fatal("FleetOf(EndDevice with empty LFDI) error = nil, want an error (fail closed, never a fleet of \"\")")
	}
}

func TestResolver_FleetOf_ManagerLookupFailurePropagates(t *testing.T) {
	t.Parallel()
	devices := fakeDevices{"dev": sep2.EndDevice{LFDI: "DEV1"}}
	r := Resolver{Devices: devices, Managers: failingManagers{}}

	_, err := r.FleetOf(context.Background(), "dev")
	if !errors.Is(err, errManagerLookupFailed) {
		t.Errorf("FleetOf error = %v, want errManagerLookupFailed to propagate (caller fails closed)", err)
	}
	if got := err.Error(); got == errManagerLookupFailed.Error() {
		t.Errorf("FleetOf error = %q, want it wrapped with the device/LFDI context, not the bare underlying error", got)
	}
}

func TestResolver_FleetOf_DeviceLookupFailurePropagates(t *testing.T) {
	t.Parallel()
	r := Resolver{Devices: fakeDevices{}, Managers: fakeManagers{}}

	_, err := r.FleetOf(context.Background(), "absent")
	if !errors.Is(err, store.ErrNotFound) {
		t.Errorf("FleetOf(absent device) error = %v, want store.ErrNotFound to propagate", err)
	}
	if got := err.Error(); got == store.ErrNotFound.Error() {
		t.Errorf("FleetOf error = %q, want it wrapped with the EndDevice id context, not the bare underlying error", got)
	}
}

// A stored LFDI in lower case resolves to the same fleet as its upper-case
// form: the management store holds the canonical upper-case LFDI, and hex
// allows either case.
func TestResolver_FleetOf_LFDICaseInsensitive(t *testing.T) {
	t.Parallel()
	devices := fakeDevices{
		"managed-lower": sep2.EndDevice{LFDI: "ab12cd"},
		"standalone":    sep2.EndDevice{LFDI: "ef34"},
	}
	r := Resolver{Devices: devices, Managers: fakeManagers{"AB12CD": "AGG9"}}
	ctx := context.Background()

	if got, err := r.FleetOf(ctx, "managed-lower"); err != nil || got != "AGG9" {
		t.Errorf("FleetOf(lower-case managed) = %q, %v; want the manager AGG9", got, err)
	}
	if got, err := r.FleetOf(ctx, "standalone"); err != nil || got != "EF34" {
		t.Errorf("FleetOf(lower-case standalone) = %q, %v; want the canonical EF34", got, err)
	}
}

func TestResolver_FleetOf_EmptyLFDIIsErrNoLFDI(t *testing.T) {
	t.Parallel()
	r := Resolver{Devices: fakeDevices{"no-lfdi": sep2.EndDevice{}}, Managers: fakeManagers{}}
	if _, err := r.FleetOf(context.Background(), "no-lfdi"); !errors.Is(err, ErrNoLFDI) {
		t.Errorf("FleetOf(empty LFDI) error = %v, want ErrNoLFDI", err)
	}
}
