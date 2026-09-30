package memory

import (
	"context"
	"fmt"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
)

// DeviceKeyedCascadeEndDeviceStore decorates any [store.EndDeviceStore] so
// deleting an EndDevice also removes the DeviceStatus, PowerStatus,
// Configuration and FunctionSetAssignments records kept under it, closing
// the same orphan shape #701 closed for flow reservation and LogEvent
// records (GRIDAPPSD/ieee-2030_5-server-go#721): without this, a device
// later created at the same key inherits whatever its predecessor left
// behind.
//
// Unlike [FlowReservationLinkedEndDeviceStore] and
// [LogEventLinkedEndDeviceStore], this type advertises no link and
// overrides no read path: core models no DeviceStatusLink, PowerStatusLink
// or ConfigurationLink at all, and FunctionSetAssignmentsListLink is
// stamped unconditionally by the POST /edev handler rather than derived
// here. There is nothing for a decorator to get right or wrong on the read
// side, so this type only ever touches Delete.
//
// Each of the four collections is optional and cascaded independently: a
// deployment that does not wire one has that family's routes unmounted
// (assembly.go documents each as its own anchor, "its own routes only"), so
// there is nothing stored under it to remove, and probing or deleting an
// absent collection would only manufacture an error for a family this
// server does not serve.
type DeviceKeyedCascadeEndDeviceStore struct {
	devs           store.EndDeviceStore
	configurations store.ScopedStore[sep2.Configuration]
	deviceStatuses store.ScopedStore[sep2.DeviceStatus]
	powerStatuses  store.ScopedStore[sep2.PowerStatus]
	fsas           store.ScopedStore[sep2.FunctionSetAssignments]
}

// compile-time proof the decorator is substitutable for what it decorates,
// and that it can be probed as part of a Delete chain (deleteProber).
var (
	_ store.EndDeviceStore = (*DeviceKeyedCascadeEndDeviceStore)(nil)
	_ deleteProber         = (*DeviceKeyedCascadeEndDeviceStore)(nil)
)

// NewDeviceKeyedCascadeEndDeviceStore decorates devs so its Delete cascades
// whichever of configurations, deviceStatuses, powerStatuses and fsas are
// wired ([store.IsAbsent] false); an absent argument means that family is
// not served, matching the route-mount gate in assembly.go.
//
// devs is required, for the same reason every EndDeviceStore decorator
// requires it: a nil decorated store fails loudly at construction instead of
// on the first request.
func NewDeviceKeyedCascadeEndDeviceStore(
	devs store.EndDeviceStore,
	configurations store.ScopedStore[sep2.Configuration],
	deviceStatuses store.ScopedStore[sep2.DeviceStatus],
	powerStatuses store.ScopedStore[sep2.PowerStatus],
	fsas store.ScopedStore[sep2.FunctionSetAssignments],
) *DeviceKeyedCascadeEndDeviceStore {
	if store.IsAbsent(devs) {
		panic("memory: NewDeviceKeyedCascadeEndDeviceStore: devs (EndDeviceStore) must not be nil")
	}
	return &DeviceKeyedCascadeEndDeviceStore{
		devs:           devs,
		configurations: configurations,
		deviceStatuses: deviceStatuses,
		powerStatuses:  powerStatuses,
		fsas:           fsas,
	}
}

// probeDelete checks whether Delete(ctx, id) looks likely to succeed,
// without mutating anything: whatever s.devs owns beneath it, then each
// family that is wired. See [deleteProber] and [probeScopedParent] for what
// this does and does not guarantee.
func (s *DeviceKeyedCascadeEndDeviceStore) probeDelete(ctx context.Context, id string) error {
	if err := probeInner(ctx, s.devs, id); err != nil {
		return err
	}
	if !store.IsAbsent(s.configurations) {
		if err := probeScopedParent(ctx, s.configurations, id); err != nil {
			return fmt.Errorf("checking configuration for %q: %w", id, err)
		}
	}
	if !store.IsAbsent(s.deviceStatuses) {
		if err := probeScopedParent(ctx, s.deviceStatuses, id); err != nil {
			return fmt.Errorf("checking device status for %q: %w", id, err)
		}
	}
	if !store.IsAbsent(s.powerStatuses) {
		if err := probeScopedParent(ctx, s.powerStatuses, id); err != nil {
			return fmt.Errorf("checking power status for %q: %w", id, err)
		}
	}
	if !store.IsAbsent(s.fsas) {
		if err := probeScopedParent(ctx, s.fsas, id); err != nil {
			return fmt.Errorf("checking function set assignments for %q: %w", id, err)
		}
	}
	return nil
}

// Delete cascades every wired family before removing the device, so none of
// them survives under the dead key for a later device created at the same
// key to inherit (GRIDAPPSD/ieee-2030_5-server-go#721).
//
// probeDelete runs first over the WHOLE chain, this layer and everything
// s.devs owns, and nothing is mutated unless every layer reports it can
// succeed; see [LogEventLinkedEndDeviceStore.Delete] for why that ordering
// matters.
func (s *DeviceKeyedCascadeEndDeviceStore) Delete(ctx context.Context, id string) error {
	if err := s.probeDelete(ctx, id); err != nil {
		return err
	}
	if !store.IsAbsent(s.configurations) {
		if err := deleteScopedParent(ctx, s.configurations, id); err != nil {
			return fmt.Errorf("cascading configuration for %q: %w", id, err)
		}
	}
	if !store.IsAbsent(s.deviceStatuses) {
		if err := deleteScopedParent(ctx, s.deviceStatuses, id); err != nil {
			return fmt.Errorf("cascading device status for %q: %w", id, err)
		}
	}
	if !store.IsAbsent(s.powerStatuses) {
		if err := deleteScopedParent(ctx, s.powerStatuses, id); err != nil {
			return fmt.Errorf("cascading power status for %q: %w", id, err)
		}
	}
	if !store.IsAbsent(s.fsas) {
		if err := deleteScopedParent(ctx, s.fsas, id); err != nil {
			return fmt.Errorf("cascading function set assignments for %q: %w", id, err)
		}
	}
	return s.devs.Delete(ctx, id)
}

// Create, Update, Get, GetBySFDI, GetByLFDI, List and Count all delegate
// unchanged: this decorator touches no link and reshapes no stored value, so
// every path but Delete is exactly what s.devs already does.

func (s *DeviceKeyedCascadeEndDeviceStore) Create(ctx context.Context, id string, device sep2.EndDevice) error {
	return s.devs.Create(ctx, id, device)
}

func (s *DeviceKeyedCascadeEndDeviceStore) Update(ctx context.Context, id string, device sep2.EndDevice) error {
	return s.devs.Update(ctx, id, device)
}

func (s *DeviceKeyedCascadeEndDeviceStore) Get(ctx context.Context, id string) (sep2.EndDevice, error) {
	return s.devs.Get(ctx, id)
}

func (s *DeviceKeyedCascadeEndDeviceStore) GetBySFDI(ctx context.Context, sfdi string) (sep2.EndDevice, error) {
	return s.devs.GetBySFDI(ctx, sfdi)
}

func (s *DeviceKeyedCascadeEndDeviceStore) GetByLFDI(ctx context.Context, lfdi string) (sep2.EndDevice, error) {
	return s.devs.GetByLFDI(ctx, lfdi)
}

func (s *DeviceKeyedCascadeEndDeviceStore) List(ctx context.Context, opts store.ListOptions) (store.ListResult[sep2.EndDevice], error) {
	return s.devs.List(ctx, opts)
}

func (s *DeviceKeyedCascadeEndDeviceStore) Count(ctx context.Context) (uint32, error) {
	return s.devs.Count(ctx)
}
