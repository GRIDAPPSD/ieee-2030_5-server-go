package memory

import (
	"context"
	"encoding/hex"
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
	// it to managerOf/managedBy: a grant or a revocation this store's own
	// maps had not already committed. A write a caller is told failed is
	// therefore never live, and never answered by an idempotent early-return
	// on retry, because the maps never changed in the first place.
	//
	// persistMu, not mu, guards the disk write (GRIDAPPSD/ieee-2030_5-server-go#677
	// fix round item 1): every mutating method holds persistMu for its
	// entire duration, which serializes writers against each other the same
	// way holding mu for the write used to, but readers taking mu.RLock
	// (ManagerOf, ManagedBy, and so every GET /edev that lists a caller's
	// managed devices, protocol-side ownership gate included) are never
	// blocked by a disk write in progress. The persist-then-apply order
	// itself lives in one place, [EndDeviceManagementStore.mutate] (#677
	// fix round item 2): a mutating method takes mu.RLock only to read the
	// current state and build the candidate snapshot, releases it before
	// the write, then re-takes mu.Lock only to apply the already-durable
	// change. persistMu already excludes every other mutator for the whole
	// operation, so nothing can have changed state between the RUnlock and
	// the Lock: the precondition checked under RLock is still true when the
	// mutation is applied under Lock.
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

// mutate is the one place the persist-then-apply order lives (#677 fix
// round item 2): every mutating method supplies only a build step, run
// under mu.RLock, that returns either an error (the operation is refused,
// nothing changes), a nil apply (a no-op: nothing to persist or apply), or a
// candidate snapshot plus the apply step that makes it live. mutate
// persists the candidate before it ever calls apply, and apply is the only
// thing build hands back that can touch managerOf/managedBy, so a method has
// no way to make a change live without the write already being durable.
// This replaces four copies of the RLock/build/RUnlock/persist/Lock/apply/
// Unlock sequence, and the eleven hand-called RUnlock/Unlock releases that
// went with them, with one. See enddevicemanagement_lockorder_test.go for
// the source-level proof that every write to the maps outside loadFromFile
// (#677 fix round item 6) happens inside an apply closure passed here.
func (s *EndDeviceManagementStore) mutate(build func() (records []managementPairRecord, apply func(), err error)) error {
	s.persistMu.Lock()
	defer s.persistMu.Unlock()

	s.mu.RLock()
	records, apply, err := build()
	s.mu.RUnlock()
	if err != nil {
		return err
	}
	if apply == nil {
		return nil
	}

	if err := s.persistRecords(records); err != nil {
		return err
	}

	s.mu.Lock()
	apply()
	s.mu.Unlock()
	return nil
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

	return s.mutate(func() ([]managementPairRecord, func(), error) {
		if current, ok := s.managerOf[managedLFDI]; ok {
			if current == managerLFDI {
				return nil, nil, nil
			}
			return nil, nil, fmt.Errorf("enddevice management: %s already has a manager: %w", managedLFDI, store.ErrAlreadyExists)
		}
		records := append(s.recordsLocked(), managementPairRecord{ManagerLFDI: managerLFDI, ManagedLFDI: managedLFDI})
		return records, func() {
			s.managerOf[managedLFDI] = managerLFDI
			if s.managedBy[managerLFDI] == nil {
				s.managedBy[managerLFDI] = make(map[string]struct{})
			}
			s.managedBy[managerLFDI][managedLFDI] = struct{}{}
		}, nil
	})
}

// Unassign implements [store.EndDeviceManagementStore].
func (s *EndDeviceManagementStore) Unassign(_ context.Context, managedLFDI string) error {
	return s.mutate(func() ([]managementPairRecord, func(), error) {
		manager, ok := s.managerOf[managedLFDI]
		if !ok {
			return nil, nil, store.ErrNotFound
		}
		records := slices.DeleteFunc(s.recordsLocked(), func(r managementPairRecord) bool {
			return r.ManagedLFDI == managedLFDI
		})
		return records, func() {
			delete(s.managerOf, managedLFDI)
			delete(s.managedBy[manager], managedLFDI)
			if len(s.managedBy[manager]) == 0 {
				delete(s.managedBy, manager)
			}
		}, nil
	})
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

	return s.mutate(func() ([]managementPairRecord, func(), error) {
		managedSrc := s.managedBy[oldManagerLFDI]
		if len(managedSrc) == 0 {
			return nil, nil, store.ErrNotFound
		}
		if oldManagerLFDI == newManagerLFDI {
			return nil, nil, nil
		}
		for managedLFDI := range managedSrc {
			if managedLFDI == newManagerLFDI {
				return nil, nil, fmt.Errorf("enddevice management: rekey would make %s manage itself: %w", newManagerLFDI, store.ErrInvalidManagementPair)
			}
		}
		if len(s.managedBy[newManagerLFDI]) > 0 {
			return nil, nil, fmt.Errorf("enddevice management: %s already manages other devices: %w", newManagerLFDI, store.ErrAlreadyExists)
		}
		managed := make(map[string]struct{}, len(managedSrc))
		for managedLFDI := range managedSrc {
			managed[managedLFDI] = struct{}{}
		}
		records := s.recordsLocked()
		for i := range records {
			if records[i].ManagerLFDI == oldManagerLFDI {
				records[i].ManagerLFDI = newManagerLFDI
			}
		}
		return records, func() {
			dest := make(map[string]struct{}, len(managed))
			for managedLFDI := range managed {
				dest[managedLFDI] = struct{}{}
				s.managerOf[managedLFDI] = newManagerLFDI
			}
			s.managedBy[newManagerLFDI] = dest
			delete(s.managedBy, oldManagerLFDI)
		}, nil
	})
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

	return s.mutate(func() ([]managementPairRecord, func(), error) {
		manager, ok := s.managerOf[oldManagedLFDI]
		if !ok {
			return nil, nil, store.ErrNotFound
		}
		if oldManagedLFDI == newManagedLFDI {
			return nil, nil, nil
		}
		if newManagedLFDI == manager {
			return nil, nil, fmt.Errorf("enddevice management: %s cannot manage itself: %w", newManagedLFDI, store.ErrInvalidManagementPair)
		}
		if _, exists := s.managerOf[newManagedLFDI]; exists {
			return nil, nil, fmt.Errorf("enddevice management: %s already has a manager: %w", newManagedLFDI, store.ErrAlreadyExists)
		}
		records := s.recordsLocked()
		for i := range records {
			if records[i].ManagedLFDI == oldManagedLFDI {
				records[i].ManagedLFDI = newManagedLFDI
			}
		}
		return records, func() {
			delete(s.managerOf, oldManagedLFDI)
			s.managerOf[newManagedLFDI] = manager
			delete(s.managedBy[manager], oldManagedLFDI)
			s.managedBy[manager][newManagedLFDI] = struct{}{}
		}, nil
	})
}

// checkCanonicalLFDI refuses input a certificate-derived LFDI never takes, so
// no comparison ever needs to fold case or trim. The length/hex check (#677
// fix round item 5) matches internal/handler's normalizeLFDI/validLFDI: the
// two packages cannot share the check directly (store must not import
// handler), but a value the API refuses must never arrive from disk and
// become an entry no API call can address again.
func checkCanonicalLFDI(role, lfdi string) error {
	if lfdi == "" || lfdi != strings.TrimSpace(lfdi) || lfdi != strings.ToUpper(lfdi) || !isHex40(lfdi) {
		return fmt.Errorf("enddevice management: %s LFDI %q is not in canonical form: %w", role, lfdi, store.ErrInvalidManagementPair)
	}
	return nil
}

// isHex40 reports whether s is exactly 40 hex characters: the canonical
// LFDI length per IEEE 2030.5 section 6.3.4 (the leading 20 bytes of a
// SHA-256 fingerprint).
func isHex40(s string) bool {
	if len(s) != 40 {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}

var (
	_ store.EndDeviceManagementReader = (*EndDeviceManagementStore)(nil)
	_ store.EndDeviceManagementStore  = (*EndDeviceManagementStore)(nil)
)
