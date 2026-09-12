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
// It is not persisted.
type EndDeviceManagementStore struct {
	mu        sync.RWMutex
	managerOf map[string]string              // managed LFDI -> manager LFDI
	managedBy map[string]map[string]struct{} // manager LFDI -> managed LFDIs
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
		return fmt.Errorf("enddevice management: %s cannot manage itself", managedLFDI)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if current, ok := s.managerOf[managedLFDI]; ok {
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
	delete(s.managerOf, managedLFDI)
	delete(s.managedBy[manager], managedLFDI)
	if len(s.managedBy[manager]) == 0 {
		delete(s.managedBy, manager)
	}
	return nil
}

// checkCanonicalLFDI refuses input a certificate-derived LFDI never takes, so
// no comparison ever needs to fold case or trim.
func checkCanonicalLFDI(role, lfdi string) error {
	if lfdi == "" || lfdi != strings.TrimSpace(lfdi) || lfdi != strings.ToUpper(lfdi) {
		return fmt.Errorf("enddevice management: %s LFDI %q is not in canonical form", role, lfdi)
	}
	return nil
}

var _ store.EndDeviceManagementStore = (*EndDeviceManagementStore)(nil)
