package memory

import (
	"context"
	"sync"

	"github.com/craig8/ieee-2030_5-go/pkg/sep2"
	"github.com/craig8/ieee-2030_5-go/pkg/store"
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
	s.indexDevice(id, device)
	return nil
}

func (s *EndDeviceStore) Update(ctx context.Context, id string, device sep2.EndDevice) error {
	// Remove old indexes before update
	old, err := s.Store.Get(ctx, id)
	if err != nil {
		return err
	}
	s.removeIndex(old)

	if err := s.Store.Update(ctx, id, device); err != nil {
		return err
	}
	s.indexDevice(id, device)
	return nil
}

func (s *EndDeviceStore) Delete(ctx context.Context, id string) error {
	old, err := s.Store.Get(ctx, id)
	if err != nil {
		return err
	}
	s.removeIndex(old)
	return s.Store.Delete(ctx, id)
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

func (s *EndDeviceStore) removeIndex(device sep2.EndDevice) {
	s.idxMu.Lock()
	defer s.idxMu.Unlock()
	delete(s.sfdiIndex, device.SFDI)
	delete(s.lfdiIndex, device.LFDI)
}
