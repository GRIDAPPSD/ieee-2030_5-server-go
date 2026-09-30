package memory

import (
	"context"
	"errors"
	"fmt"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
)

// DeviceKeyedCascadeEndDeviceStore decorates any [store.EndDeviceStore] so
// deleting an EndDevice also removes the DeviceStatus, PowerStatus,
// Configuration and FunctionSetAssignments records kept under it, and the
// admin-plane FSA assignment links that name it, closing the same orphan
// shape #701 closed for flow reservation and LogEvent records
// (GRIDAPPSD/ieee-2030_5-server-go#721): without this, a device later
// created at the same key inherits whatever its predecessor left behind, and
// the admin topology keeps naming a device that is gone.
//
// Unlike [FlowReservationLinkedEndDeviceStore] and
// [LogEventLinkedEndDeviceStore], this type advertises no link and
// overrides no read path: core models no DeviceStatusLink, PowerStatusLink
// or ConfigurationLink at all, and FunctionSetAssignmentsListLink is
// stamped unconditionally by the POST /edev handler rather than derived
// here. There is nothing for a decorator to get right or wrong on the read
// side, so this type only ever touches Delete.
//
// Each collection is optional and cascaded independently: a deployment that
// does not wire one has that family's routes unmounted (assembly.go
// documents each of the four as its own anchor, "its own routes only"), so
// there is nothing stored under it to remove, and probing or deleting an
// absent collection would only manufacture an error for a family this
// server does not serve.
type DeviceKeyedCascadeEndDeviceStore struct {
	devs           store.EndDeviceStore
	configurations store.ScopedStore[sep2.Configuration]
	deviceStatuses store.ScopedStore[sep2.DeviceStatus]
	powerStatuses  store.ScopedStore[sep2.PowerStatus]
	fsas           store.ScopedStore[sep2.FunctionSetAssignments]
	adminFSAs      *AdminFSAStore
}

// compile-time proof the decorator is substitutable for what it decorates,
// and that it can be probed as part of a Delete chain (deleteProber).
var (
	_ store.EndDeviceStore = (*DeviceKeyedCascadeEndDeviceStore)(nil)
	_ deleteProber         = (*DeviceKeyedCascadeEndDeviceStore)(nil)
)

// NewDeviceKeyedCascadeEndDeviceStore decorates devs so its Delete cascades
// whichever of configurations, deviceStatuses, powerStatuses, fsas and
// adminFSAs are wired ([store.IsAbsent] false); an absent argument means
// that family is not served, matching the route-mount gate in assembly.go.
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
	adminFSAs *AdminFSAStore,
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
		adminFSAs:      adminFSAs,
	}
}

// deviceKeyedFamily names one cascaded ScopedStore collection alongside the
// probe and delete calls closed over its own store, so probeDelete and
// Delete can walk one slice instead of repeating a per-family branch four
// times each.
type deviceKeyedFamily struct {
	name   string
	probe  func(ctx context.Context, id string) error
	delete func(ctx context.Context, id string) error
}

// scopedFamilies returns the wired ScopedStore families in cascade order.
// A family whose store is absent (that route family is not mounted) is left
// out entirely, matching the per-family route-mount gate in assembly.go.
func (s *DeviceKeyedCascadeEndDeviceStore) scopedFamilies() []deviceKeyedFamily {
	var out []deviceKeyedFamily
	if !store.IsAbsent(s.configurations) {
		out = append(out, deviceKeyedFamily{
			name:   "configuration",
			probe:  func(ctx context.Context, id string) error { return probeScopedParent(ctx, s.configurations, id) },
			delete: func(ctx context.Context, id string) error { return deleteScopedParent(ctx, s.configurations, id) },
		})
	}
	if !store.IsAbsent(s.deviceStatuses) {
		out = append(out, deviceKeyedFamily{
			name:   "device status",
			probe:  func(ctx context.Context, id string) error { return probeScopedParent(ctx, s.deviceStatuses, id) },
			delete: func(ctx context.Context, id string) error { return deleteScopedParent(ctx, s.deviceStatuses, id) },
		})
	}
	if !store.IsAbsent(s.powerStatuses) {
		out = append(out, deviceKeyedFamily{
			name:   "power status",
			probe:  func(ctx context.Context, id string) error { return probeScopedParent(ctx, s.powerStatuses, id) },
			delete: func(ctx context.Context, id string) error { return deleteScopedParent(ctx, s.powerStatuses, id) },
		})
	}
	if !store.IsAbsent(s.fsas) {
		out = append(out, deviceKeyedFamily{
			name:   "function set assignments",
			probe:  func(ctx context.Context, id string) error { return probeScopedParent(ctx, s.fsas, id) },
			delete: func(ctx context.Context, id string) error { return deleteScopedParent(ctx, s.fsas, id) },
		})
	}
	return out
}

// probeDelete checks whether Delete(ctx, id) looks likely to succeed,
// without mutating anything: whatever s.devs owns beneath it, then each
// ScopedStore family that is wired. See [deleteProber] and
// [probeScopedParent] for what this does and does not guarantee.
//
// The admin FSA links have no probe here: [AdminFSAStore]'s reads cannot
// fail (they touch no disk), so there is nothing to check ahead of the
// write; see [DeviceKeyedCascadeEndDeviceStore.unassignAdminFSALinks].
func (s *DeviceKeyedCascadeEndDeviceStore) probeDelete(ctx context.Context, id string) error {
	if err := probeInner(ctx, s.devs, id); err != nil {
		return err
	}
	for _, f := range s.scopedFamilies() {
		if err := f.probe(ctx, id); err != nil {
			return fmt.Errorf("checking %s for %q: %w", f.name, id, err)
		}
	}
	return nil
}

// unassignAdminFSALinks clears the admin-plane device -> FSA assignment
// links before any other family is cascaded, so its one failure mode (a
// persistence flush, when AdminFSAStore is durable) leaves every other
// family untouched. HandleAssignDeviceFSA writes this link in the same act
// as the scoped FunctionSetAssignments record s.fsas cascades below;
// leaving the link behind would keep the admin topology naming a device
// that is gone and refuse a later re-assign as a duplicate
// (GRIDAPPSD/ieee-2030_5-server-go#721).
func (s *DeviceKeyedCascadeEndDeviceStore) unassignAdminFSALinks(ctx context.Context, id string) error {
	if store.IsAbsent(s.adminFSAs) {
		return nil
	}
	for _, fsaID := range s.adminFSAs.FSAsForDevice(ctx, id) {
		if err := s.adminFSAs.UnassignDevice(ctx, fsaID, id); err != nil && !errors.Is(err, store.ErrNotFound) {
			return fmt.Errorf("clearing the admin FSA assignment %q for %q: %w", fsaID, id, err)
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
// matters. The admin FSA links are unassigned first among this layer's own
// mutations, ahead of the probed ScopedStore families, since that is the
// one step here with no probe of its own.
func (s *DeviceKeyedCascadeEndDeviceStore) Delete(ctx context.Context, id string) error {
	if err := s.probeDelete(ctx, id); err != nil {
		return err
	}
	if err := s.unassignAdminFSALinks(ctx, id); err != nil {
		return err
	}
	for _, f := range s.scopedFamilies() {
		if err := f.delete(ctx, id); err != nil {
			return fmt.Errorf("cascading %s for %q: %w", f.name, id, err)
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
