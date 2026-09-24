package memory

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
)

// EndDeviceManagementStore is the in-memory [store.EndDeviceManagementStore].
type EndDeviceManagementStore struct {
	mu        sync.RWMutex
	managerOf map[string]string              // managed LFDI -> manager LFDI
	managedBy map[string]map[string]struct{} // manager LFDI -> managed LFDIs

	// Durable persistence (GRIDAPPSD/ieee-2030_5-server-go#440). Empty
	// persistPath = pure in-memory, the historical behavior. When set, every
	// mutating method writes the candidate snapshot to disk BEFORE applying
	// it to managerOf/managedBy, both under mu (GRIDAPPSD/ieee-2030_5-server-go#677
	// fix round): a grant or a revocation this store's own maps had not
	// already committed. A write a caller is told failed is therefore never
	// live, and never answered by an idempotent early-return on retry,
	// because the maps never changed in the first place. The cost is that
	// mu, including reads through the protocol-side ownership gate, is held
	// for the duration of the disk write; accepted because this store is
	// mutated only from the admin plane, at operator rate.
	persistPath string
}

// NewEndDeviceManagementStore returns an empty management store.
func NewEndDeviceManagementStore() *EndDeviceManagementStore {
	return &EndDeviceManagementStore{
		managerOf: make(map[string]string),
		managedBy: make(map[string]map[string]struct{}),
	}
}

// ManagerOf implements [store.EndDeviceManagementStore].
func (s *EndDeviceManagementStore) ManagerOf(_ context.Context, managedLFDI string) (string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	manager, ok := s.managerOf[managedLFDI]
	if !ok {
		return "", store.ErrNotFound
	}
	return manager, nil
}

// ManagedBy implements [store.EndDeviceManagementStore].
func (s *EndDeviceManagementStore) ManagedBy(_ context.Context, managerLFDI string) ([]string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	managed := make([]string, 0, len(s.managedBy[managerLFDI]))
	for lfdi := range s.managedBy[managerLFDI] {
		managed = append(managed, lfdi)
	}
	slices.Sort(managed)
	return managed, nil
}

// Assign implements [store.EndDeviceManagementStore].
func (s *EndDeviceManagementStore) Assign(_ context.Context, managerLFDI, managedLFDI string) error {
	if err := checkCanonicalLFDI("manager", managerLFDI); err != nil {
		return err
	}
	if err := checkCanonicalLFDI("managed", managedLFDI); err != nil {
		return err
	}
	if managerLFDI == managedLFDI {
		return fmt.Errorf("enddevice management: %s cannot manage itself: %w", managedLFDI, store.ErrInvalidManagementPair)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if current, ok := s.managerOf[managedLFDI]; ok {
		if current == managerLFDI {
			return nil
		}
		return fmt.Errorf("enddevice management: %s already has a manager: %w", managedLFDI, store.ErrAlreadyExists)
	}

	records := append(s.recordsLocked(), managementPairRecord{ManagerLFDI: managerLFDI, ManagedLFDI: managedLFDI})
	if err := s.persistRecordsLocked(records); err != nil {
		return err
	}
	s.managerOf[managedLFDI] = managerLFDI
	if s.managedBy[managerLFDI] == nil {
		s.managedBy[managerLFDI] = make(map[string]struct{})
	}
	s.managedBy[managerLFDI][managedLFDI] = struct{}{}
	return nil
}

// Unassign implements [store.EndDeviceManagementStore].
func (s *EndDeviceManagementStore) Unassign(_ context.Context, managedLFDI string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	manager, ok := s.managerOf[managedLFDI]
	if !ok {
		return store.ErrNotFound
	}

	records := slices.DeleteFunc(s.recordsLocked(), func(r managementPairRecord) bool {
		return r.ManagedLFDI == managedLFDI
	})
	if err := s.persistRecordsLocked(records); err != nil {
		return err
	}
	delete(s.managerOf, managedLFDI)
	delete(s.managedBy[manager], managedLFDI)
	if len(s.managedBy[manager]) == 0 {
		delete(s.managedBy, manager)
	}
	return nil
}

// RekeyManager replaces oldManagerLFDI with newManagerLFDI as the manager of
// every pair oldManagerLFDI currently holds, leaving the managed LFDIs
// themselves unchanged. A rotated certificate carries a new LFDI, so
// without this a rotation leaves every pair naming the retired LFDI: the
// retired certificate keeps management and the rotated one has none.
//
// Returns ErrNotFound when oldManagerLFDI manages nothing (checked before
// the no-op case below, so a from/to pair naming an LFDI that manages
// nothing is refused rather than reported as a successful rotation that
// moved zero pairs), and ErrInvalidManagementPair when either LFDI is not
// canonical or the rekey would make newManagerLFDI manage itself.
// ErrAlreadyExists when newManagerLFDI already manages other devices:
// merging two fleets is irreversible (the old key is deleted), so a rekey
// never does it implicitly, the same refusal RekeyManaged already gives a
// managed-side collision.
func (s *EndDeviceManagementStore) RekeyManager(_ context.Context, oldManagerLFDI, newManagerLFDI string) error {
	if err := checkCanonicalLFDI("old manager", oldManagerLFDI); err != nil {
		return err
	}
	if err := checkCanonicalLFDI("new manager", newManagerLFDI); err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	managed := s.managedBy[oldManagerLFDI]
	if len(managed) == 0 {
		return store.ErrNotFound
	}
	if oldManagerLFDI == newManagerLFDI {
		return nil
	}
	for managedLFDI := range managed {
		if managedLFDI == newManagerLFDI {
			return fmt.Errorf("enddevice management: rekey would make %s manage itself: %w", newManagerLFDI, store.ErrInvalidManagementPair)
		}
	}
	if len(s.managedBy[newManagerLFDI]) > 0 {
		return fmt.Errorf("enddevice management: %s already manages other devices: %w", newManagerLFDI, store.ErrAlreadyExists)
	}

	records := s.recordsLocked()
	for i := range records {
		if records[i].ManagerLFDI == oldManagerLFDI {
			records[i].ManagerLFDI = newManagerLFDI
		}
	}
	if err := s.persistRecordsLocked(records); err != nil {
		return err
	}
	dest := make(map[string]struct{}, len(managed))
	for managedLFDI := range managed {
		dest[managedLFDI] = struct{}{}
		s.managerOf[managedLFDI] = newManagerLFDI
	}
	s.managedBy[newManagerLFDI] = dest
	delete(s.managedBy, oldManagerLFDI)
	return nil
}

// RekeyManaged replaces oldManagedLFDI with newManagedLFDI, keeping the same
// manager.
//
// Returns ErrNotFound when oldManagedLFDI is unmanaged (checked before the
// no-op case below, for the same reason RekeyManager checks existence
// first), ErrInvalidManagementPair when either LFDI is not canonical or
// newManagedLFDI equals its own manager, and ErrAlreadyExists when
// newManagedLFDI already has a manager: a rekey must never silently
// overwrite an existing pair or leave a device with two managers.
func (s *EndDeviceManagementStore) RekeyManaged(_ context.Context, oldManagedLFDI, newManagedLFDI string) error {
	if err := checkCanonicalLFDI("old managed", oldManagedLFDI); err != nil {
		return err
	}
	if err := checkCanonicalLFDI("new managed", newManagedLFDI); err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	manager, ok := s.managerOf[oldManagedLFDI]
	if !ok {
		return store.ErrNotFound
	}
	if oldManagedLFDI == newManagedLFDI {
		return nil
	}
	if newManagedLFDI == manager {
		return fmt.Errorf("enddevice management: %s cannot manage itself: %w", newManagedLFDI, store.ErrInvalidManagementPair)
	}
	if _, exists := s.managerOf[newManagedLFDI]; exists {
		return fmt.Errorf("enddevice management: %s already has a manager: %w", newManagedLFDI, store.ErrAlreadyExists)
	}

	records := s.recordsLocked()
	for i := range records {
		if records[i].ManagedLFDI == oldManagedLFDI {
			records[i].ManagedLFDI = newManagedLFDI
		}
	}
	if err := s.persistRecordsLocked(records); err != nil {
		return err
	}
	delete(s.managerOf, oldManagedLFDI)
	s.managerOf[newManagedLFDI] = manager
	delete(s.managedBy[manager], oldManagedLFDI)
	s.managedBy[manager][newManagedLFDI] = struct{}{}
	return nil
}

// checkCanonicalLFDI refuses input a certificate-derived LFDI never takes, so
// no comparison ever needs to fold case or trim.
func checkCanonicalLFDI(role, lfdi string) error {
	if lfdi == "" || lfdi != strings.TrimSpace(lfdi) || lfdi != strings.ToUpper(lfdi) {
		return fmt.Errorf("enddevice management: %s LFDI %q is not in canonical form: %w", role, lfdi, store.ErrInvalidManagementPair)
	}
	return nil
}

var (
	_ store.EndDeviceManagementReader = (*EndDeviceManagementStore)(nil)
	_ store.EndDeviceManagementStore  = (*EndDeviceManagementStore)(nil)
)
