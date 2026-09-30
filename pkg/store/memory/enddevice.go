package memory

import (
	"context"
	"log"
	"sync"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
)

// EndDeviceStore wraps the generic Store with SFDI/LFDI secondary indexes.
type EndDeviceStore struct {
	*Store[sep2.EndDevice]
	idxMu     sync.RWMutex
	sfdiIndex map[string]string // sfdi -> store key
	lfdiIndex map[string]string // lfdi -> store key
}

// NewEndDeviceStore creates an in-memory EndDeviceStore.
func NewEndDeviceStore() *EndDeviceStore {
	return &EndDeviceStore{
		Store:     NewStore[sep2.EndDevice](),
		sfdiIndex: make(map[string]string),
		lfdiIndex: make(map[string]string),
	}
}

func (s *EndDeviceStore) Create(ctx context.Context, id string, device sep2.EndDevice) error {
	if err := s.Store.Create(ctx, id, device); err != nil {
		return err
	}
	// Captured before indexDevice overwrites the SFDI/LFDI keys, so a
	// rollback can restore whichever id owned them before this call rather
	// than merely erasing what this call wrote: Store.Create enforces
	// uniqueness on id alone, so this device can legally reuse an identity
	// another, untouched device already held, and indexDevice's overwrite
	// would otherwise leave that device unresolvable by SFDI or LFDI even
	// though its own record was never touched
	// (GRIDAPPSD/ieee-2030_5-server-go#721).
	snap := s.snapshotIndex(device)
	s.indexDevice(id, device)
	// Snapshot to disk if persistence is configured
	// (GRIDAPPSD/ieee-2030_5-server-go#165). No-op for in-memory stores
	// so back-compat is automatic.
	if err := s.persistEndDeviceSnapshot(); err != nil {
		// A failed flush must not leave a half-committed device readable: a
		// decorator such as RegisteredEndDeviceStore trusts a nil error from
		// this call before touching the Registration store, and an error
		// here that still left the device inserted would let it skip that
		// step while the device stayed servable
		// (GRIDAPPSD/ieee-2030_5-server-go#721).
		s.restoreIndex(id, device, snap)
		if delErr := s.Store.Delete(ctx, id); delErr != nil {
			log.Printf("memory: EndDevice %q left stored after its snapshot failed to flush; rollback also failed: %v", id, delErr)
		}
		return err
	}
	return nil
}

func (s *EndDeviceStore) Update(ctx context.Context, id string, device sep2.EndDevice) error {
	// Remove old indexes before update
	old, err := s.Store.Get(ctx, id)
	if err != nil {
		return err
	}
	s.removeIndex(id, old)

	if err := s.Store.Update(ctx, id, device); err != nil {
		return err
	}
	s.indexDevice(id, device)
	return s.persistEndDeviceSnapshot()
}

func (s *EndDeviceStore) Delete(ctx context.Context, id string) error {
	old, err := s.Store.Get(ctx, id)
	if err != nil {
		return err
	}
	s.removeIndex(id, old)
	if err := s.Store.Delete(ctx, id); err != nil {
		return err
	}
	return s.persistEndDeviceSnapshot()
}

func (s *EndDeviceStore) GetBySFDI(_ context.Context, sfdi string) (sep2.EndDevice, error) {
	s.idxMu.RLock()
	id, ok := s.sfdiIndex[sfdi]
	s.idxMu.RUnlock()

	if !ok {
		return sep2.EndDevice{}, store.ErrNotFound
	}
	return s.Store.Get(context.Background(), id)
}

func (s *EndDeviceStore) GetByLFDI(_ context.Context, lfdi string) (sep2.EndDevice, error) {
	s.idxMu.RLock()
	id, ok := s.lfdiIndex[lfdi]
	s.idxMu.RUnlock()

	if !ok {
		return sep2.EndDevice{}, store.ErrNotFound
	}
	return s.Store.Get(context.Background(), id)
}

func (s *EndDeviceStore) indexDevice(id string, device sep2.EndDevice) {
	s.idxMu.Lock()
	defer s.idxMu.Unlock()
	if device.SFDI != "" {
		s.sfdiIndex[device.SFDI] = id
	}
	if device.LFDI != "" {
		s.lfdiIndex[device.LFDI] = id
	}
}

// removeIndex clears device's SFDI and LFDI index entries, but only when
// they still point at id. Store.Create enforces uniqueness on id alone, not
// on either identity field, so a duplicate SFDI or LFDI across two ids is
// possible; deleting by field value alone would then erase the OTHER id's
// live entry, whether that id is a genuinely different device or this same
// device's own successor after a rolled-back Create reused its key
// (GRIDAPPSD/ieee-2030_5-server-go#721).
func (s *EndDeviceStore) removeIndex(id string, device sep2.EndDevice) {
	s.idxMu.Lock()
	defer s.idxMu.Unlock()
	if s.sfdiIndex[device.SFDI] == id {
		delete(s.sfdiIndex, device.SFDI)
	}
	if s.lfdiIndex[device.LFDI] == id {
		delete(s.lfdiIndex, device.LFDI)
	}
}

// indexSnapshot records who held device's SFDI and LFDI keys before a
// Create's indexDevice call overwrites them, so a rollback can put the
// prior owner back rather than merely clearing what this call wrote. The
// "had" fields distinguish no prior owner from an empty id, which a bare
// zero-value string cannot.
type indexSnapshot struct {
	sfdiOwner    string
	sfdiHadOwner bool
	lfdiOwner    string
	lfdiHadOwner bool
}

// snapshotIndex captures the current owners of device's SFDI and LFDI keys.
// Call it before indexDevice, while the keys still name whoever held them
// beforehand.
func (s *EndDeviceStore) snapshotIndex(device sep2.EndDevice) indexSnapshot {
	s.idxMu.RLock()
	defer s.idxMu.RUnlock()
	var snap indexSnapshot
	snap.sfdiOwner, snap.sfdiHadOwner = s.sfdiIndex[device.SFDI]
	snap.lfdiOwner, snap.lfdiHadOwner = s.lfdiIndex[device.LFDI]
	return snap
}

// restoreIndex undoes indexDevice(id, device) for a Create that did not
// durably complete. Each key is reset to the owner snap recorded before the
// write when it had one, or cleared when it did not, but ONLY when the key
// still names id, the write being rolled back: a key some other, later
// write has since claimed is left alone, the same guard removeIndex applies
// (GRIDAPPSD/ieee-2030_5-server-go#721). Restoring the prior owner, rather
// than always clearing, is what removeIndex alone could not do: indexDevice
// had already overwritten that owner's entry before the rollback runs, so
// removeIndex's own id-check sees only the failed write's id and erases the
// key instead of returning it, leaving the prior owner unresolvable by an
// identity it still legitimately holds.
func (s *EndDeviceStore) restoreIndex(id string, device sep2.EndDevice, snap indexSnapshot) {
	s.idxMu.Lock()
	defer s.idxMu.Unlock()
	if s.sfdiIndex[device.SFDI] == id {
		if snap.sfdiHadOwner {
			s.sfdiIndex[device.SFDI] = snap.sfdiOwner
		} else {
			delete(s.sfdiIndex, device.SFDI)
		}
	}
	if s.lfdiIndex[device.LFDI] == id {
		if snap.lfdiHadOwner {
			s.lfdiIndex[device.LFDI] = snap.lfdiOwner
		} else {
			delete(s.lfdiIndex, device.LFDI)
		}
	}
}
