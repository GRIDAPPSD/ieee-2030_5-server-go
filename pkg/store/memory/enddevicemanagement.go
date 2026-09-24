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
	// persistPath = pure in-memory, the historical behavior. When set,
	// every mutation flushes a snapshot under persistMu, held separately
	// from mu so the disk syscall does not block readers on the in-memory
	// state.
	persistMu   sync.Mutex
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
	if current, ok := s.managerOf[managedLFDI]; ok {
		s.mu.Unlock()
		if current == managerLFDI {
			return nil
		}
		return fmt.Errorf("enddevice management: %s already has a manager: %w", managedLFDI, store.ErrAlreadyExists)
	}
	s.managerOf[managedLFDI] = managerLFDI
	if s.managedBy[managerLFDI] == nil {
		s.managedBy[managerLFDI] = make(map[string]struct{})
	}
	s.managedBy[managerLFDI][managedLFDI] = struct{}{}
	s.mu.Unlock()
	// Persist outside the lock so disk I/O does not block concurrent
	// readers on s.mu.
	return s.persist()
}

// Unassign implements [store.EndDeviceManagementStore].
func (s *EndDeviceManagementStore) Unassign(_ context.Context, managedLFDI string) error {
	s.mu.Lock()
	manager, ok := s.managerOf[managedLFDI]
	if !ok {
		s.mu.Unlock()
		return store.ErrNotFound
	}
	delete(s.managerOf, managedLFDI)
	delete(s.managedBy[manager], managedLFDI)
	if len(s.managedBy[manager]) == 0 {
		delete(s.managedBy, manager)
	}
	s.mu.Unlock()
	return s.persist()
}

// RekeyManager replaces oldManagerLFDI with newManagerLFDI as the manager of
// every pair oldManagerLFDI currently holds, leaving the managed LFDIs
// themselves unchanged. A rotated certificate carries a new LFDI, so
// without this a rotation leaves every pair naming the retired LFDI: the
// retired certificate keeps management and the rotated one has none.
// Returns ErrNotFound when oldManagerLFDI manages nothing, and
// ErrInvalidManagementPair when either LFDI is not canonical or the rekey
// would make newManagerLFDI manage itself.
func (s *EndDeviceManagementStore) RekeyManager(_ context.Context, oldManagerLFDI, newManagerLFDI string) error {
	if err := checkCanonicalLFDI("old manager", oldManagerLFDI); err != nil {
		return err
	}
	if err := checkCanonicalLFDI("new manager", newManagerLFDI); err != nil {
		return err
	}
	if oldManagerLFDI == newManagerLFDI {
		return nil
	}

	s.mu.Lock()
	managed := s.managedBy[oldManagerLFDI]
	if len(managed) == 0 {
		s.mu.Unlock()
		return store.ErrNotFound
	}
	for managedLFDI := range managed {
		if managedLFDI == newManagerLFDI {
			s.mu.Unlock()
			return fmt.Errorf("enddevice management: rekey would make %s manage itself: %w", newManagerLFDI, store.ErrInvalidManagementPair)
		}
	}
	dest := s.managedBy[newManagerLFDI]
	if dest == nil {
		dest = make(map[string]struct{})
		s.managedBy[newManagerLFDI] = dest
	}
	for managedLFDI := range managed {
		dest[managedLFDI] = struct{}{}
		s.managerOf[managedLFDI] = newManagerLFDI
	}
	delete(s.managedBy, oldManagerLFDI)
	s.mu.Unlock()
	return s.persist()
}

// RekeyManaged replaces oldManagedLFDI with newManagedLFDI, keeping the same
// manager. Returns ErrNotFound when oldManagedLFDI is unmanaged,
// ErrInvalidManagementPair when either LFDI is not canonical or
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
	if oldManagedLFDI == newManagedLFDI {
		return nil
	}

	s.mu.Lock()
	manager, ok := s.managerOf[oldManagedLFDI]
	if !ok {
		s.mu.Unlock()
		return store.ErrNotFound
	}
	if newManagedLFDI == manager {
		s.mu.Unlock()
		return fmt.Errorf("enddevice management: %s cannot manage itself: %w", newManagedLFDI, store.ErrInvalidManagementPair)
	}
	if _, exists := s.managerOf[newManagedLFDI]; exists {
		s.mu.Unlock()
		return fmt.Errorf("enddevice management: %s already has a manager: %w", newManagedLFDI, store.ErrAlreadyExists)
	}
	delete(s.managerOf, oldManagedLFDI)
	s.managerOf[newManagedLFDI] = manager
	delete(s.managedBy[manager], oldManagedLFDI)
	s.managedBy[manager][newManagedLFDI] = struct{}{}
	s.mu.Unlock()
	return s.persist()
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
