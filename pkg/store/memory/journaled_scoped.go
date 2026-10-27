package memory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"log"
	"os"
	"strconv"
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
//
// The journal opens with a floor: the time from which it holds every record
// its caller asked Compact to keep. CompleteFrom reports the floor the replay
// started from, so a reader can tell a record that never existed from one a
// restart dropped.
type JournaledScopedStore[T store.Copier[T]] struct {
	label string
	path  string
	// inner holds the records in memory, for its key index and its parent
	// undo; it has no snapshot path and never writes.
	inner *PersistentScopedStore[T]

	// memFloor is the floor the replay started from; fileFloor is the floor
	// the journal on disk carries.
	memFloor, fileFloor int64

	// writeMu serializes writers. A writer also holds readMu across its
	// mutate, append and undo, so a reader sees only decided state.
	writeMu sync.Mutex
	readMu  sync.RWMutex
	// dirty is set when a failed append or sync may have left the journal
	// short or torn; the next write rewrites the journal from memory first.
	dirty bool
	// since, while a compaction is staged, collects every entry appended
	// after its snapshot, to be added to the staged journal before it
	// replaces the old one. Nil when no compaction is running.
	since []journalEntry

	// compactMu runs one compaction at a time.
	compactMu sync.Mutex

	// fault, when set by a test, fails every journal write it returns an
	// error for.
	fault func() error
}

// Journal operations.
const (
	journalFloor        = "f"
	journalCreate       = "c"
	journalCreateAll    = "b"
	journalUpdate       = "u"
	journalDelete       = "d"
	journalDeleteParent = "p"
)

// journalEntry is one journal line. Value is the record for a create or an
// update, and the list of records for a batch; At is the floor. Sum is a
// CRC-32 of the other fields, so a torn or altered line is dropped at replay,
// not applied.
type journalEntry struct {
	Op     string          `json:"op"`
	Parent string          `json:"p,omitempty"`
	ID     string          `json:"id,omitempty"`
	Value  json.RawMessage `json:"v,omitempty"`
	At     int64           `json:"at,omitempty"`
	Sum    uint32          `json:"k"`
}

func (e journalEntry) checksum() uint32 {
	h := crc32.NewIEEE()
	for _, part := range [][]byte{[]byte(e.Op), []byte(e.Parent), []byte(e.ID), e.Value, []byte(strconv.FormatInt(e.At, 10))} {
		_, _ = h.Write(part) // a hash.Hash never returns an error
		_, _ = h.Write([]byte{0})
	}
	return h.Sum32()
}

func sealed(e journalEntry) journalEntry {
	e.Sum = e.checksum()
	return e
}

func newJournalEntry(op, parent, id string, value any) (journalEntry, error) {
	e := journalEntry{Op: op, Parent: parent, ID: id}
	if value != nil {
		raw, err := json.Marshal(value)
		if err != nil {
			return journalEntry{}, err
		}
		e.Value = raw
	}
	return sealed(e), nil
}

// batchRecord is one record of a batch line.
type batchRecord[T any] struct {
	ID    string `json:"id"`
	Value T      `json:"v"`
}

// NewJournaledScopedStore replays the journal at path. now is the floor of a
// journal that has none: a missing one, or one with no floor line, holds
// nothing from before it was opened.
//
// A line that does not decode, fails its checksum or cannot be applied (a
// create of an id already held, an update of one not held) is dropped and
// counted in the log, and the journal is rewritten without it. A delete of
// an id not held is not damage: Compact may have dropped its create. A
// journal in which no record line is valid is moved aside, not rewritten,
// and logged with its count. label names the collection in errors and logs.
func NewJournaledScopedStore[T store.Copier[T]](path, label string, now int64) (*JournaledScopedStore[T], error) {
	if path == "" {
		return nil, fmt.Errorf("%s journal: needs a path", label)
	}
	inner, err := NewPersistentScopedStore[T]("", label)
	if err != nil {
		return nil, err
	}
	s := &JournaledScopedStore[T]{label: label, path: path, inner: inner}
	ctx := context.Background()
	floor, valid := int64(-1), 0
	dropped, err := jsonl.ReadLines(path, func(line []byte) bool {
		ok, at := s.replay(ctx, line)
		switch {
		case ok && at > 0:
			floor = at
		case ok:
			valid++
		}
		return ok
	})
	if err != nil {
		return nil, fmt.Errorf("%s journal: %w", label, err)
	}
	noFloor := floor < 0
	if noFloor {
		floor = now
	}
	s.memFloor, s.fileFloor = floor, floor
	if dropped > 0 && valid == 0 {
		aside := path + ".damaged-" + strconv.FormatInt(now, 10)
		if err := os.Rename(path, aside); err != nil {
			return nil, fmt.Errorf("%s journal: move the damaged journal aside: %w", label, err)
		}
		s.memFloor, s.fileFloor = now, now
		log.Printf("%s journal: none of its %d line(s) could be replayed, so it holds no record; it is kept as %s and a new journal starts empty", logsafe.Str(label), dropped, logsafe.Str(aside))
	} else if dropped > 0 {
		log.Printf("%s journal: dropped %d line(s) that did not decode, failed their checksum or could not be applied, and replayed %d; the journal is rewritten without them", logsafe.Str(label), dropped, valid)
	}
	if dropped > 0 || noFloor {
		if err := s.rewriteLocked(ctx); err != nil {
			return nil, err
		}
	}
	return s, nil
}

// replay applies one journal line and reports whether it was valid, and the
// floor when it is a floor line.
func (s *JournaledScopedStore[T]) replay(ctx context.Context, line []byte) (bool, int64) {
	var e journalEntry
	if err := json.Unmarshal(line, &e); err != nil || e.Sum != e.checksum() {
		return false, 0
	}
	if e.Op == journalFloor {
		return e.At > 0, e.At
	}
	if e.Parent == "" {
		return false, 0
	}
	switch e.Op {
	case journalCreate, journalUpdate:
		var v T
		if e.ID == "" || json.Unmarshal(e.Value, &v) != nil {
			return false, 0
		}
		if e.Op == journalCreate {
			return s.inner.Create(ctx, e.Parent, e.ID, v) == nil, 0
		}
		return s.inner.Update(ctx, e.Parent, e.ID, v) == nil, 0
	case journalCreateAll:
		var recs []batchRecord[T]
		if json.Unmarshal(e.Value, &recs) != nil || len(recs) == 0 {
			return false, 0
		}
		ids, values := make([]string, len(recs)), make([]T, len(recs))
		for i, r := range recs {
			ids[i], values[i] = r.ID, r.Value
		}
		return s.createAllInner(ctx, e.Parent, ids, values) == nil, 0
	case journalDelete:
		err := s.inner.Delete(ctx, e.Parent, e.ID)
		return e.ID != "" && (err == nil || errors.Is(err, store.ErrNotFound)), 0
	case journalDeleteParent:
		_, err := s.inner.DeleteParent(ctx, e.Parent)
		return err == nil, 0
	}
	return false, 0
}

// Persists reports true: every write reaches the journal.
func (s *JournaledScopedStore[T]) Persists() bool { return true }

// RollsBackOnFailure reports true: a failed write restores the pre-call state.
func (s *JournaledScopedStore[T]) RollsBackOnFailure() bool { return true }

// CompleteFrom is the floor the replay started from, in the caller's time
// unit: the store holds every record Compact was asked to keep from then on,
// and may lack older ones.
func (s *JournaledScopedStore[T]) CompleteFrom() int64 { return s.memFloor }

func (s *JournaledScopedStore[T]) checkFault() error {
	if s.fault != nil {
		return s.fault()
	}
	return nil
}

// parentRecords is one parent's records in ascending id order.
type parentRecords[T any] struct {
	parent string
	ids    []string
	items  []T
}

// snapshotLocked is every held record. The caller holds writeMu, so no write
// is undecided.
func (s *JournaledScopedStore[T]) snapshotLocked(ctx context.Context) ([]parentRecords[T], error) {
	parents, err := s.inner.Parents(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]parentRecords[T], 0, len(parents))
	for _, parent := range parents {
		pr := parentRecords[T]{parent: parent, ids: s.inner.IDs(parent)}
		for _, id := range pr.ids {
			v, err := s.inner.Get(ctx, parent, id)
			if err != nil {
				return nil, fmt.Errorf("read %s/%s: %v", parent, id, err)
			}
			pr.items = append(pr.items, v)
		}
		out = append(out, pr)
	}
	return out, nil
}

// journalOf is the journal of snap: the floor line, then a create per record
// keep picks. A nil keep keeps every record.
func journalOf[T any](snap []parentRecords[T], floor int64, keep func(parent string, items []T) ([]bool, error)) ([]byte, error) {
	entries := []journalEntry{sealed(journalEntry{Op: journalFloor, At: floor})}
	for _, pr := range snap {
		kept := make([]bool, len(pr.items))
		if keep == nil {
			for i := range kept {
				kept[i] = true
			}
		} else {
			var err error
			if kept, err = keep(pr.parent, pr.items); err != nil {
				return nil, err
			}
			if len(kept) != len(pr.items) {
				return nil, fmt.Errorf("keep for %s answered %d of %d records", pr.parent, len(kept), len(pr.items))
			}
		}
		for i, id := range pr.ids {
			if !kept[i] {
				continue
			}
			e, err := newJournalEntry(journalCreate, pr.parent, id, &pr.items[i])
			if err != nil {
				return nil, fmt.Errorf("encode %s/%s: %w", pr.parent, id, err)
			}
			entries = append(entries, e)
		}
	}
	return jsonl.Encode(entries)
}

// rewriteLocked replaces the journal with every held record. The caller
// holds writeMu, or is the constructor.
func (s *JournaledScopedStore[T]) rewriteLocked(ctx context.Context) error {
	snap, err := s.snapshotLocked(ctx)
	if err == nil {
		var payload []byte
		if payload, err = journalOf(snap, s.fileFloor, nil); err == nil {
			if err = s.checkFault(); err == nil {
				err = jsonl.Rewrite(s.path, payload)
			}
		}
	}
	if err != nil {
		return fmt.Errorf("%s journal: %w", s.label, err)
	}
	s.dirty = false
	return nil
}

// Compact rewrites the journal with the records keep picks, under the floor
// keepFrom (never below the replay's floor). keep sees each parent's records
// in ascending id order and answers one flag per record. A record it drops
// stays in memory until the next restart. On an error the journal is left as
// it was.
//
// Writers wait only for the snapshot and for the final rename: keep and the
// write of the new journal run outside writeMu, and every entry appended in
// between is added to the new journal before it replaces the old one.
func (s *JournaledScopedStore[T]) Compact(ctx context.Context, keepFrom int64, keep func(parent string, items []T) ([]bool, error)) error {
	s.compactMu.Lock()
	defer s.compactMu.Unlock()

	s.writeMu.Lock()
	snap, err := s.snapshotLocked(ctx)
	if err != nil {
		s.writeMu.Unlock()
		return fmt.Errorf("%s journal: %w", s.label, err)
	}
	floor := max(keepFrom, s.memFloor)
	s.since = []journalEntry{}
	s.writeMu.Unlock()

	staged, err := s.stage(snap, floor, keep)

	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	since := s.since
	s.since = nil
	if err != nil {
		return fmt.Errorf("%s journal: %w", s.label, err)
	}
	tail, err := jsonl.Encode(since)
	if err == nil {
		if err = s.checkFault(); err == nil {
			err = staged.Commit(tail)
		} else {
			staged.Abort()
		}
	} else {
		staged.Abort()
	}
	if err != nil {
		return fmt.Errorf("%s journal: %w", s.label, err)
	}
	s.fileFloor = floor
	s.dirty = false
	return nil
}

func (s *JournaledScopedStore[T]) stage(snap []parentRecords[T], floor int64, keep func(parent string, items []T) ([]bool, error)) (*jsonl.Staged, error) {
	payload, err := journalOf(snap, floor, keep)
	if err != nil {
		return nil, err
	}
	if err := s.checkFault(); err != nil {
		return nil, err
	}
	return jsonl.Stage(s.path, payload)
}

// Sync flushes the appends written since the last Sync. A failed sync marks
// the journal for a rewrite, as the appends it covered may be lost.
func (s *JournaledScopedStore[T]) Sync() error {
	err := s.checkFault()
	if err == nil {
		err = jsonl.Sync(s.path)
	}
	if err != nil {
		s.writeMu.Lock()
		s.dirty = true
		s.writeMu.Unlock()
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
		err = s.checkFault()
	}
	if err == nil {
		err = jsonl.Append(s.path, payload, false)
	}
	if err == nil {
		if s.since != nil {
			s.since = append(s.since, entries...)
		}
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
	return s.CreateAll(ctx, parentID, []string{id}, []T{value})
}

// CreateAll stores records under one parent as one journal line, so after a
// restart either all of them are held or none is, whatever fault the journal
// write hit. On any error none of them is stored.
func (s *JournaledScopedStore[T]) CreateAll(ctx context.Context, parentID string, ids []string, values []T) error {
	if len(ids) != len(values) || len(ids) == 0 {
		return fmt.Errorf("%s journal: CreateAll needs one value per id, got %d ids and %d values", s.label, len(ids), len(values))
	}
	var hadParent bool
	undo := func() error { return s.undoCreates(ctx, parentID, ids, hadParent) }
	return s.write(ctx, func() ([]journalEntry, error) {
		var err error
		if hadParent, err = s.inner.HasParent(ctx, parentID); err != nil {
			return nil, err
		}
		if err := s.createAllInner(ctx, parentID, ids, values); err != nil {
			return nil, err
		}
		var e journalEntry
		if len(ids) == 1 {
			e, err = newJournalEntry(journalCreate, parentID, ids[0], &values[0])
		} else {
			recs := make([]batchRecord[T], len(ids))
			for i := range ids {
				recs[i] = batchRecord[T]{ID: ids[i], Value: values[i]}
			}
			e, err = newJournalEntry(journalCreateAll, parentID, "", recs)
		}
		if err != nil {
			return nil, errors.Join(fmt.Errorf("encode %s: %w", parentID, err), undo())
		}
		return []journalEntry{e}, nil
	}, undo)
}

// createAllInner creates every record in memory, or none of them.
func (s *JournaledScopedStore[T]) createAllInner(ctx context.Context, parentID string, ids []string, values []T) error {
	hadParent, err := s.inner.HasParent(ctx, parentID)
	if err != nil {
		return err
	}
	for i := range ids {
		if err := s.inner.Create(ctx, parentID, ids[i], values[i]); err != nil {
			return errors.Join(err, s.undoCreates(ctx, parentID, ids[:i], hadParent))
		}
	}
	return nil
}

// undoCreates removes records a create stored, and the parent with them when
// the create established it.
func (s *JournaledScopedStore[T]) undoCreates(ctx context.Context, parentID string, ids []string, hadParent bool) error {
	if !hadParent {
		_, err := s.inner.DeleteParent(ctx, parentID)
		return err
	}
	var errs []error
	for _, id := range ids {
		if err := s.inner.Delete(ctx, parentID, id); err != nil && !errors.Is(err, store.ErrNotFound) {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
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
		return []journalEntry{sealed(journalEntry{Op: journalDelete, Parent: parentID, ID: id})}, nil
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
			entries = append(entries, sealed(journalEntry{Op: journalDeleteParent, Parent: p}))
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
