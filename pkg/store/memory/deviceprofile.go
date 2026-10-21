package memory

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
)

// DeviceProfileStore is the in-memory [store.DeviceProfileStore], optionally
// backed by the shared snapshot envelope in persistence.go.
//
// A profile grants nothing, so unlike the management store the disk write may
// share the one mutex: the candidate snapshot is written before it is applied,
// and a write a caller is told failed is never live.
type DeviceProfileStore struct {
	mu       sync.RWMutex
	profiles map[string]store.DeviceProfile // LFDI -> profile

	// persistPath is empty for a pure in-memory store.
	persistPath string
}

// NewDeviceProfileStore returns an empty in-memory store.
func NewDeviceProfileStore() *DeviceProfileStore {
	return &DeviceProfileStore{profiles: make(map[string]store.DeviceProfile)}
}

// NewDeviceProfileStoreWithPersistence builds a store wired to a JSON snapshot
// file; an empty path means in-memory only. A missing file is cold boot. A
// corrupt file, an unsupported version, or a record that fails the checks Put
// enforces (canonical LFDI, known kind, unique name) is an error and the file
// is refused wholesale, so a caller carrying on does so on purpose.
func NewDeviceProfileStoreWithPersistence(path string) (*DeviceProfileStore, error) {
	s := NewDeviceProfileStore()
	if path == "" {
		return s, nil
	}
	if err := s.loadFromFile(path); err != nil {
		return nil, fmt.Errorf("device profile persistence: load %q: %w", path, err)
	}
	s.persistPath = path
	return s, nil
}

func (s *DeviceProfileStore) loadFromFile(path string) error {
	env, err := readSnapshotEnvelope(path)
	if err != nil {
		return err
	}
	if env == nil || len(env.Records) == 0 {
		return nil
	}
	records, err := decodeSnapshotRecords[store.DeviceProfile](env.Records)
	if err != nil {
		return err
	}
	profiles := make(map[string]store.DeviceProfile, len(records))
	for _, r := range records {
		if err := checkProfile(r); err != nil {
			return fmt.Errorf("record %q: %w", r.LFDI, err)
		}
		if _, dup := profiles[r.LFDI]; dup {
			return fmt.Errorf("record: %s has more than one profile on disk", r.LFDI)
		}
		if holder, taken := nameHolder(profiles, r.Name, r.LFDI); taken {
			return fmt.Errorf("record: %s and %s share a name on disk", r.LFDI, holder)
		}
		profiles[r.LFDI] = r
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.profiles = profiles
	return nil
}

// checkProfile is the check every path into the store applies.
func checkProfile(p store.DeviceProfile) error {
	if !isCanonicalLFDI(p.LFDI) {
		return fmt.Errorf("device profile: LFDI %q is not in canonical form: %w", p.LFDI, store.ErrInvalidDeviceProfile)
	}
	switch p.Kind {
	case "", store.DeviceKindAggregator, store.DeviceKindDevice:
		return nil
	}
	return fmt.Errorf("device profile: unknown kind: %w", store.ErrInvalidDeviceProfile)
}

// nameHolder returns the LFDI other than self holding name, ignoring case. An
// empty name is "no name" and is never held.
func nameHolder(profiles map[string]store.DeviceProfile, name, self string) (string, bool) {
	if name == "" {
		return "", false
	}
	for lfdi, p := range profiles {
		if lfdi != self && strings.EqualFold(p.Name, name) {
			return lfdi, true
		}
	}
	return "", false
}

// Get implements [store.DeviceProfileReader].
func (s *DeviceProfileStore) Get(_ context.Context, lfdi string) (store.DeviceProfile, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	p, ok := s.profiles[lfdi]
	if !ok {
		return store.DeviceProfile{}, store.ErrNotFound
	}
	return p, nil
}

// List implements [store.DeviceProfileReader].
func (s *DeviceProfileStore) List(_ context.Context) ([]store.DeviceProfile, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return sortedProfiles(s.profiles), nil
}

// Put implements [store.DeviceProfileStore].
func (s *DeviceProfileStore) Put(_ context.Context, p store.DeviceProfile) error {
	if err := checkProfile(p); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, taken := nameHolder(s.profiles, p.Name, p.LFDI); taken {
		return fmt.Errorf("device profile: name already in use: %w", store.ErrAlreadyExists)
	}
	next := make(map[string]store.DeviceProfile, len(s.profiles)+1)
	for k, v := range s.profiles {
		next[k] = v
	}
	next[p.LFDI] = p
	return s.commitLocked(next)
}

// Delete implements [store.DeviceProfileStore].
func (s *DeviceProfileStore) Delete(_ context.Context, lfdi string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.profiles[lfdi]; !ok {
		return store.ErrNotFound
	}
	next := make(map[string]store.DeviceProfile, len(s.profiles))
	for k, v := range s.profiles {
		if k != lfdi {
			next[k] = v
		}
	}
	return s.commitLocked(next)
}

// commitLocked writes next to disk and only then makes it live. The caller
// holds s.mu for writing.
func (s *DeviceProfileStore) commitLocked(next map[string]store.DeviceProfile) error {
	if s.persistPath != "" {
		if err := writeSnapshotEnvelope(s.persistPath, sortedProfiles(next)); err != nil {
			return fmt.Errorf("device profile persistence: %w", err)
		}
	}
	s.profiles = next
	return nil
}

func sortedProfiles(m map[string]store.DeviceProfile) []store.DeviceProfile {
	out := make([]store.DeviceProfile, 0, len(m))
	for _, p := range m {
		out = append(out, p)
	}
	slices.SortFunc(out, func(a, b store.DeviceProfile) int { return strings.Compare(a.LFDI, b.LFDI) })
	return out
}

var (
	_ store.DeviceProfileReader = (*DeviceProfileStore)(nil)
	_ store.DeviceProfileStore  = (*DeviceProfileStore)(nil)
)
