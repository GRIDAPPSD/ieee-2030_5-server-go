package memory

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
)

// ErrInvalidFSAControl is returned for an FSA control record the store will
// not hold: an empty template id, id or mRID, or an update that changes the
// mRID or creationTime.
var ErrInvalidFSAControl = errors.New("invalid FSA control")

// FSAControl is one DER control issued on a program template, the record
// every device copy of it is written from (#972). Every copy carries the
// record's id, mRID and creationTime, so a device assigned later, or a copy
// lost before a restart, gets the same event.
type FSAControl struct {
	TemplateID   string              `json:"templateId"`
	ID           string              `json:"id"`
	MRID         string              `json:"mRID"`
	CreationTime int64               `json:"creationTime"`
	Start        int64               `json:"start"`
	Duration     uint32              `json:"duration"`
	Description  string              `json:"description,omitempty"`
	Base         sep2.DERControlBase `json:"derControlBase"`
	CancelledAt  *int64              `json:"cancelledAt,omitempty"`
	CancelReason string              `json:"cancelReason,omitempty"`
}

// Copy returns an independent copy.
func (c FSAControl) Copy() FSAControl {
	out := c
	out.Base = c.Base.Copy()
	if c.CancelledAt != nil {
		v := *c.CancelledAt
		out.CancelledAt = &v
	}
	return out
}

// Ended reports whether the control's interval has passed at now.
func (c FSAControl) Ended(now int64) bool {
	return now >= c.Start+int64(c.Duration)
}

func compareFSAControlKey(c FSAControl, key [2]string) int {
	return cmp.Or(strings.Compare(c.TemplateID, key[0]), strings.Compare(c.ID, key[1]))
}

func fsaControlKey(templateID, id string) [2]string { return [2]string{templateID, id} }

// checkFSAControlAdd reports why c cannot join ctrls, the records of one FSA.
// Copies of controls of one FSA land on the same devices, so an mRID shared
// by two records would name two events on each device.
func checkFSAControlAdd(ctrls []FSAControl, c FSAControl) error {
	if c.TemplateID == "" || c.ID == "" || c.MRID == "" {
		return fmt.Errorf("%w: template id, id and mRID are required", ErrInvalidFSAControl)
	}
	for _, o := range ctrls {
		if (o.TemplateID == c.TemplateID && o.ID == c.ID) || strings.EqualFold(o.MRID, c.MRID) {
			return store.ErrAlreadyExists
		}
	}
	return nil
}

// CreateFSAControl records a control on a template of fsaID. ErrNotFound if
// the FSA or the template is absent; ErrAlreadyExists if its id is taken on
// the template or its mRID on the FSA.
func (s *AdminFSAStore) CreateFSAControl(_ context.Context, fsaID string, c FSAControl) error {
	s.persistMu.Lock()
	defer s.persistMu.Unlock()
	s.mu.Lock()
	if _, found := slices.BinarySearchFunc(s.templates[fsaID], c.TemplateID, compareTemplateID); !found {
		s.mu.Unlock()
		return store.ErrNotFound
	}
	ctrls := s.controls[fsaID]
	if err := checkFSAControlAdd(ctrls, c); err != nil {
		s.mu.Unlock()
		return err
	}
	key := fsaControlKey(c.TemplateID, c.ID)
	idx, _ := slices.BinarySearchFunc(ctrls, key, compareFSAControlKey)
	s.controls[fsaID] = slices.Insert(ctrls, idx, c.Copy())
	s.mu.Unlock()
	return s.persistOrRollback(func() {
		ctrls := s.controls[fsaID]
		if idx, found := slices.BinarySearchFunc(ctrls, key, compareFSAControlKey); found {
			s.setFSAControls(fsaID, slices.Delete(ctrls, idx, idx+1))
		}
	})
}

// FSAControls returns independent copies of the records on one template,
// oldest creationTime first, the order a device assigned later receives them.
func (s *AdminFSAStore) FSAControls(_ context.Context, fsaID, templateID string) []FSAControl {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := []FSAControl{}
	for _, c := range s.controls[fsaID] {
		if c.TemplateID == templateID {
			out = append(out, c.Copy())
		}
	}
	slices.SortStableFunc(out, func(a, b FSAControl) int {
		return cmp.Or(cmp.Compare(a.CreationTime, b.CreationTime), strings.Compare(a.ID, b.ID))
	})
	return out
}

// FSAControl returns one record. ErrNotFound if it is absent.
func (s *AdminFSAStore) FSAControl(_ context.Context, fsaID, templateID, id string) (FSAControl, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	ctrls := s.controls[fsaID]
	idx, found := slices.BinarySearchFunc(ctrls, fsaControlKey(templateID, id), compareFSAControlKey)
	if !found {
		return FSAControl{}, store.ErrNotFound
	}
	return ctrls[idx].Copy(), nil
}

// FSAControlByMRID returns the FSA holding the record whose mRID is mrid, in
// either case, and the record.
func (s *AdminFSAStore) FSAControlByMRID(mrid string) (string, FSAControl, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for fsaID, ctrls := range s.controls {
		for _, c := range ctrls {
			if strings.EqualFold(c.MRID, mrid) {
				return fsaID, c.Copy(), true
			}
		}
	}
	return "", FSAControl{}, false
}

// UpdateFSAControl replaces the record with c's template id and id.
// ErrNotFound if it is absent. An event keeps its mRID and creationTime, so
// changing either is ErrInvalidFSAControl.
func (s *AdminFSAStore) UpdateFSAControl(_ context.Context, fsaID string, c FSAControl) error {
	s.persistMu.Lock()
	defer s.persistMu.Unlock()
	s.mu.Lock()
	ctrls := s.controls[fsaID]
	key := fsaControlKey(c.TemplateID, c.ID)
	idx, found := slices.BinarySearchFunc(ctrls, key, compareFSAControlKey)
	if !found {
		s.mu.Unlock()
		return store.ErrNotFound
	}
	before := ctrls[idx]
	if c.MRID != before.MRID || c.CreationTime != before.CreationTime {
		s.mu.Unlock()
		return fmt.Errorf("%w: mRID and creationTime cannot change", ErrInvalidFSAControl)
	}
	ctrls[idx] = c.Copy()
	s.mu.Unlock()
	return s.persistOrRollback(func() {
		ctrls := s.controls[fsaID]
		if idx, found := slices.BinarySearchFunc(ctrls, key, compareFSAControlKey); found {
			ctrls[idx] = before
		}
	})
}

// DeleteFSAControl removes one record. ErrNotFound if it is absent.
func (s *AdminFSAStore) DeleteFSAControl(_ context.Context, fsaID, templateID, id string) error {
	s.persistMu.Lock()
	defer s.persistMu.Unlock()
	s.mu.Lock()
	ctrls := s.controls[fsaID]
	key := fsaControlKey(templateID, id)
	idx, found := slices.BinarySearchFunc(ctrls, key, compareFSAControlKey)
	if !found {
		s.mu.Unlock()
		return store.ErrNotFound
	}
	before := ctrls[idx]
	s.setFSAControls(fsaID, slices.Delete(ctrls, idx, idx+1))
	s.mu.Unlock()
	return s.persistOrRollback(func() {
		ctrls := s.controls[fsaID]
		if idx, found := slices.BinarySearchFunc(ctrls, key, compareFSAControlKey); !found {
			s.controls[fsaID] = slices.Insert(ctrls, idx, before)
		}
	})
}

// setFSAControls stores ctrls under fsaID, dropping the key when it empties.
// Caller holds s.mu.
func (s *AdminFSAStore) setFSAControls(fsaID string, ctrls []FSAControl) {
	if len(ctrls) == 0 {
		delete(s.controls, fsaID)
		return
	}
	s.controls[fsaID] = ctrls
}
