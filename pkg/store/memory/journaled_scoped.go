package memory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"log"
	"strings"
	"sync"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/jsonl"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/logsafe"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
)

// JournaledScopedStore is a parent-scoped collection that appends one line
// per write to a JSON-lines journal and replays the journal at open
// (GRIDAPPSD/ieee-2030_5-server-go#1206). It is for a collection written far
// more often than it is rewritten: a [PersistentScopedStore] rewrites its
// whole snapshot on every write.
//
// A write that stores in memory but fails to reach the journal is undone
// before the error returns, as in [PersistentScopedStore]. Appends are not
// synced one by one: Sync flushes them, so a process crash loses nothing and
// a power loss loses what was written since the last Sync. Compact rewrites
// the journal with only the records its caller keeps; the others stay in
// memory and are gone after the next restart.
type JournaledScopedStore[T store.Copier[T]] struct {
	label string
	path  string
	// inner holds the records in memory, for its key index and its parent
	// undo; it has no snapshot path and never writes.
	inner *PersistentScopedStore[T]

	// writeMu serializes writers and Compact. A writer also holds readMu
	// across its mutate, append and undo, so a reader sees only decided
	// state; Compact changes nothing in memory and leaves readers running.
	writeMu sync.Mutex
	readMu  sync.RWMutex
	// dirty is set when a failed append may have left part of a line; the
	// next write rewrites the journal from memory first.
	dirty bool
}

// Journal operations.
const (
	journalCreate       = "c"
	journalUpdate       = "u"
	journalDelete       = "d"
	journalDeleteParent = "p"
)

// journalEntry is one journal line. Value is the record for a create or an
// update; Sum is a CRC-32 of the other fields, so a torn or altered line is
// dropped at replay, not applied.
type journalEntry struct {
	Op     string          `json:"op"`
	Parent string          `json:"p"`
	ID     string          `json:"id,omitempty"`
	Value  json.RawMessage `json:"v,omitempty"`
	Sum    uint32          `json:"k"`
}

func (e journalEntry) checksum() uint32 {
	h := crc32.NewIEEE()
	for _, part := range [][]byte{[]byte(e.Op), []byte(e.Parent), []byte(e.ID), e.Value} {
		_, _ = h.Write(part) // a hash.Hash never returns an error
		_, _ = h.Write([]byte{0})
	}
	return h.Sum32()
}

func newJournalEntry[T any](op, parent, id string, value *T) (journalEntry, error) {
	e := journalEntry{Op: op, Parent: parent, ID: id}
	if value != nil {
		raw, err := json.Marshal(value)
		if err != nil {
			return journalEntry{}, err
		}
		e.Value = raw
	}
	e.Sum = e.checksum()
	return e, nil
}

// NewJournaledScopedStore replays the journal at path, creating nothing until
// the first write. A line that does not decode, fails its checksum or cannot
// be applied (a create of an id already held, an update of one not held) is
// dropped and counted in the log, and the journal is rewritten without it. A
// delete of an id not held is not damage: Compact may have dropped its
// create. label names the collection in errors and logs.
func NewJournaledScopedStore[T store.Copier[T]](path, label string) (*JournaledScopedStore[T], error) {
	if path == "" {
		return nil, fmt.Errorf("%s journal: needs a path", label)
	}
	inner, err := NewPersistentScopedStore[T]("", label)
	if err != nil {
		return nil, err
	}
	s := &JournaledScopedStore[T]{label: label, path: path, inner: inner}
	ctx := context.Background()
	dropped, err := jsonl.ReadLines(path, func(line []byte) bool { return s.replay(ctx, line) })
	if err != nil {
		return nil, fmt.Errorf("%s journal: %w", label, err)
	}
	if dropped > 0 {
		log.Printf("%s journal: dropped %d line(s) that did not decode, failed their checksum or could not be applied; the journal is rewritten without them", logsafe.Str(label), dropped)
		if err := s.rewriteLocked(ctx); err != nil {
			return nil, err
		}
	}
	return s, nil
}

// replay applies one journal line and reports whether it was valid.
func (s *JournaledScopedStore[T]) replay(ctx context.Context, line []byte) bool {
	var e journalEntry
	if err := json.Unmarshal(line, &e); err != nil || e.Parent == "" || e.Sum != e.checksum() {
		return false
	}
	switch e.Op {
	case journalCreate, journalUpdate:
		var v T
		if e.ID == "" || json.Unmarshal(e.Value, &v) != nil {
			return false
		}
		if e.Op == journalCreate {
			return s.inner.Create(ctx, e.Parent, e.ID, v) == nil
		}
		return s.inner.Update(ctx, e.Parent, e.ID, v) == nil
	case journalDelete:
		err := s.inner.Delete(ctx, e.Parent, e.ID)
		return e.ID != "" && (err == nil || errors.Is(err, store.ErrNotFound))
	case journalDeleteParent:
		_, err := s.inner.DeleteParent(ctx, e.Parent)
		return err == nil
	}
	return false
}

// Persists reports true: every write reaches the journal.
func (s *JournaledScopedStore[T]) Persists() bool { return true }

// RollsBackOnFailure reports true: a failed write restores the pre-call state.
func (s *JournaledScopedStore[T]) RollsBackOnFailure() bool { return true }

// records is every held record, as create entries, parents and ids ascending.
// keep, when not nil, picks which of a parent's records go in. The caller
// holds writeMu, so no write is undecided.
func (s *JournaledScopedStore[T]) records(ctx context.Context, keep func(parent string, items []T) ([]bool, error)) ([]journalEntry, error) {
	parents, err := s.inner.Parents(ctx)
	if err != nil {
		return nil, err
	}
	var out []journalEntry
	for _, parent := range parents {
		ids := s.inner.IDs(parent)
		items := make([]T, 0, len(ids))
		for _, id := range ids {
			v, err := s.inner.Get(ctx, parent, id)
			if err != nil {
				return nil, fmt.Errorf("read %s/%s: %v", parent, id, err)
			}
			items = append(items, v)
		}
		kept := make([]bool, len(items))
		if keep == nil {
			for i := range kept {
				kept[i] = true
			}
		} else if kept, err = keep(parent, items); err != nil {
			return nil, err
		} else if len(kept) != len(items) {
			return nil, fmt.Errorf("keep for %s answered %d of %d records", parent, len(kept), len(items))
		}
		for i, id := range ids {
			if !kept[i] {
				continue
			}
			e, err := newJournalEntry(journalCreate, parent, id, &items[i])
			if err != nil {
				return nil, fmt.Errorf("encode %s/%s: %w", parent, id, err)
			}
			out = append(out, e)
		}
	}
	return out, nil
}

// rewriteLocked replaces the journal with every held record. The caller
// holds writeMu, or is the constructor.
func (s *JournaledScopedStore[T]) rewriteLocked(ctx context.Context) error {
	return s.replaceLocked(ctx, nil)
}

func (s *JournaledScopedStore[T]) replaceLocked(ctx context.Context, keep func(parent string, items []T) ([]bool, error)) error {
	entries, err := s.records(ctx, keep)
	if err != nil {
		return fmt.Errorf("%s journal: %w", s.label, err)
	}
	payload, err := jsonl.Encode(entries)
	if err != nil {
		return fmt.Errorf("%s journal: %w", s.label, err)
	}
	if err := jsonl.Rewrite(s.path, payload); err != nil {
		return fmt.Errorf("%s journal: %w", s.label, err)
	}
	s.dirty = false
	return nil
}

// Compact rewrites the journal with the records keep picks. keep sees each
// parent's records in ascending id order and answers one flag per record. A
// record it drops stays in memory until the next restart. On an error the
// journal is left as it was.
func (s *JournaledScopedStore[T]) Compact(ctx context.Context, keep func(parent string, items []T) ([]bool, error)) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return s.replaceLocked(ctx, keep)
}

// Sync flushes the appends written since the last Sync.
func (s *JournaledScopedStore[T]) Sync() error {
	if err := jsonl.Sync(s.path); err != nil {
		return fmt.Errorf("%s journal: %w", s.label, err)
	}
	return nil
}

// write runs mutate, then appends entries; on a failed append it runs undo
// and marks the journal for a rewrite. A journal left dirty by an earlier
// failure is rewritten before anything is mutated.
func (s *JournaledScopedStore[T]) write(ctx context.Context, mutate func() ([]journalEntry, error), undo func() error) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if s.dirty {
		if err := s.rewriteLocked(ctx); err != nil {
			return err
		}
	}
	s.readMu.Lock()
	defer s.readMu.Unlock()
	entries, err := mutate()
	if err != nil || len(entries) == 0 {
		return err
	}
	payload, err := jsonl.Encode(entries)
	if err == nil {
		err = jsonl.Append(s.path, payload, false)
	}
	if err == nil {
		return nil
	}
	err = fmt.Errorf("%s journal: %w", s.label, err)
	s.dirty = true
	if uerr := undo(); uerr != nil {
		return errors.Join(err, fmt.Errorf("%s journal: undo: %w", s.label, uerr))
	}
	return err
}

// Get returns the record under (parentID, id), or store.ErrNotFound.
func (s *JournaledScopedStore[T]) Get(ctx context.Context, parentID, id string) (T, error) {
	s.readMu.RLock()
	defer s.readMu.RUnlock()
	return s.inner.Get(ctx, parentID, id)
}

// List returns one page of the records under parentID.
func (s *JournaledScopedStore[T]) List(ctx context.Context, parentID string, opts store.ListOptions) (store.ListResult[T], error) {
	s.readMu.RLock()
	defer s.readMu.RUnlock()
	return s.inner.List(ctx, parentID, opts)
}

// Count returns the number of records under parentID.
func (s *JournaledScopedStore[T]) Count(ctx context.Context, parentID string) (uint32, error) {
	s.readMu.RLock()
	defer s.readMu.RUnlock()
	return s.inner.Count(ctx, parentID)
}

// HasParent reports whether the collection knows parentID.
func (s *JournaledScopedStore[T]) HasParent(ctx context.Context, parentID string) (bool, error) {
	s.readMu.RLock()
	defer s.readMu.RUnlock()
	return s.inner.HasParent(ctx, parentID)
}

// Parents returns the parent ids the collection knows, ascending.
func (s *JournaledScopedStore[T]) Parents(ctx context.Context) ([]string, error) {
	s.readMu.RLock()
	defer s.readMu.RUnlock()
	return s.inner.Parents(ctx)
}

// Create stores a record and journals it.
func (s *JournaledScopedStore[T]) Create(ctx context.Context, parentID, id string, value T) error {
	var hadParent bool
	return s.write(ctx, func() ([]journalEntry, error) {
		var err error
		if hadParent, err = s.inner.HasParent(ctx, parentID); err != nil {
			return nil, err
		}
		if err := s.inner.Create(ctx, parentID, id, value); err != nil {
			return nil, err
		}
		e, err := newJournalEntry(journalCreate, parentID, id, &value)
		if err != nil {
			return nil, errors.Join(fmt.Errorf("encode %s/%s: %w", parentID, id, err), s.undoCreate(ctx, parentID, id, hadParent))
		}
		return []journalEntry{e}, nil
	}, func() error { return s.undoCreate(ctx, parentID, id, hadParent) })
}

// undoCreate removes a record Create stored, and the parent with it when the
// create established it.
func (s *JournaledScopedStore[T]) undoCreate(ctx context.Context, parentID, id string, hadParent bool) error {
	if hadParent {
		return s.inner.Delete(ctx, parentID, id)
	}
	_, err := s.inner.DeleteParent(ctx, parentID)
	return err
}

// Update replaces a record and journals it.
func (s *JournaledScopedStore[T]) Update(ctx context.Context, parentID, id string, value T) error {
	var before T
	return s.write(ctx, func() ([]journalEntry, error) {
		var err error
		if before, err = s.inner.Get(ctx, parentID, id); err != nil {
			return nil, err
		}
		e, err := newJournalEntry(journalUpdate, parentID, id, &value)
		if err != nil {
			return nil, fmt.Errorf("encode %s/%s: %w", parentID, id, err)
		}
		if err := s.inner.Update(ctx, parentID, id, value); err != nil {
			return nil, err
		}
		return []journalEntry{e}, nil
	}, func() error { return s.inner.Update(ctx, parentID, id, before) })
}

// Delete removes a record and journals the removal.
func (s *JournaledScopedStore[T]) Delete(ctx context.Context, parentID, id string) error {
	var before T
	return s.write(ctx, func() ([]journalEntry, error) {
		var err error
		if before, err = s.inner.Get(ctx, parentID, id); err != nil {
			return nil, err
		}
		if err := s.inner.Delete(ctx, parentID, id); err != nil {
			return nil, err
		}
		e, _ := newJournalEntry[T](journalDelete, parentID, id, nil) // no value to encode
		return []journalEntry{e}, nil
	}, func() error { return s.inner.Create(ctx, parentID, id, before) })
}

// DeleteParent removes a parent's whole collection, reports how many records
// went with it, and journals the removal. On a failed append the records are
// restored and the count is zero.
func (s *JournaledScopedStore[T]) DeleteParent(ctx context.Context, parentID string) (uint32, error) {
	return s.deleteParents(ctx, func(p string) bool { return p == parentID })
}

// DeleteParentsWithPrefix removes every parent whose id starts with prefix,
// in one journal append. An empty prefix is refused.
func (s *JournaledScopedStore[T]) DeleteParentsWithPrefix(ctx context.Context, prefix string) (uint32, error) {
	if prefix == "" {
		return 0, errors.New("memory: DeleteParentsWithPrefix: prefix is required")
	}
	return s.deleteParents(ctx, func(p string) bool { return strings.HasPrefix(p, prefix) })
}

func (s *JournaledScopedStore[T]) deleteParents(ctx context.Context, match func(string) bool) (uint32, error) {
	var total uint32
	var undos []func(context.Context) error
	err := s.write(ctx, func() ([]journalEntry, error) {
		parents, err := s.inner.Parents(ctx)
		if err != nil {
			return nil, err
		}
		var entries []journalEntry
		for _, p := range parents {
			if !match(p) {
				continue
			}
			undo, n, err := s.inner.TakeParent(ctx, p)
			if err != nil {
				return nil, errors.Join(err, s.runUndos(ctx, undos))
			}
			undos = append(undos, undo)
			total += n
			e, _ := newJournalEntry[T](journalDeleteParent, p, "", nil) // no value to encode
			entries = append(entries, e)
		}
		return entries, nil
	}, func() error { return s.runUndos(ctx, undos) })
	if err != nil {
		return 0, err
	}
	return total, nil
}

func (s *JournaledScopedStore[T]) runUndos(ctx context.Context, undos []func(context.Context) error) error {
	var errs []error
	for _, undo := range undos {
		errs = append(errs, undo(ctx))
	}
	return errors.Join(errs...)
}
