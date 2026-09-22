package sep2capture

import (
	"fmt"
	"os"
	"time"
)

// writeLoop is the store's one writer goroutine: it owns active,
// nextSegNum, and liveSegs for the store's whole lifetime, so none of them
// need a lock. It exits once writeCh is closed and drained (Close), and
// closes whatever segment is still open before returning.
func (s *Store) writeLoop() {
	defer close(s.writerDone)
	for ex := range s.writeCh {
		size := queuedSize(ex)
		s.writeOne(ex)
		s.inFlightBytes.Add(-size)
	}
	if s.active != nil {
		_ = s.active.f.Close()
		s.active = nil
	}
}

// writeOne appends one exchange to the active segment, rolling and
// evicting first as needed, then indexes it and publishes it to
// subscribers. A write error never retries the same record (Q4): it closes
// the active segment so the next call starts a fresh one, and counts this
// record as lost via DroppedWriteError. No connection ever sees any of
// this; writeOne only ever runs after Record has already handed the bytes
// off.
func (s *Store) writeOne(ex Exchange) {
	// A duplicate id is refused before any disk work, the write-error
	// counters, or testBeforeWrite: two Recorders on one Store each start
	// their own atomic id counter (recorder.go), so the same id can reach
	// here twice, and only DuplicateIndexIDs counts it (index.go).
	if s.idx.refuseIfDuplicate(ex.ID) {
		return
	}
	if s.testBeforeWrite != nil {
		s.testBeforeWrite()
	}
	if ex.Request.Truncated || ex.Response.Truncated {
		s.truncated.Add(1)
	}

	reqLen := int64(len(ex.Request.Bytes))
	respLen := int64(len(ex.Response.Bytes))
	recLen := int64(recordHeaderLen) + reqLen + respLen

	// Parsed and estimated before ensureRoomFor so a would-be index
	// memory overage can trigger the same early eviction the disk cap
	// does (operator decision 2026-09-22), rather than only being
	// checked after the entry already exists.
	method, path := parseRequestLine(ex.Request.Bytes)
	status := parseStatusLine(ex.Response.Bytes)
	entrySize := indexEntryBytes(method, path, ex.ClientLFDI, ex.ClientSFDI, ex.Error)

	if err := s.ensureRoomFor(recLen, entrySize); err != nil {
		s.droppedWriteError.Add(1)
		s.logWriteErr(err)
		return
	}

	// A record whose own size exceeds segBytes still gets written (Q4:
	// "gets a segment to itself"), but only into a segment that is
	// otherwise empty: the size>0 guard stops an oversized record from
	// rolling forever before it is ever written.
	if s.active == nil || (s.active.size > 0 && s.active.size+recLen > s.segBytes) {
		if err := s.rollSegment(); err != nil {
			s.droppedWriteError.Add(1)
			s.logWriteErr(err)
			return
		}
	}

	offset := s.active.size
	header := encodeRecordHeader(ex)
	if _, err := s.active.f.Write(header); err != nil {
		s.failActiveSegment(err)
		return
	}
	if reqLen > 0 {
		if _, err := s.active.f.Write(ex.Request.Bytes); err != nil {
			s.failActiveSegment(err)
			return
		}
	}
	if respLen > 0 {
		if _, err := s.active.f.Write(ex.Response.Bytes); err != nil {
			s.failActiveSegment(err)
			return
		}
	}

	s.active.size += recLen
	s.setLiveSegSize(s.active.number, s.active.size)
	s.totalOnDisk.Add(recLen)

	// Assigned here, not at Record: this is the one point every exchange
	// passes through exactly once, in the single writer goroutine, so Seq
	// is a true write-order sequence regardless of what order ids were
	// handed out at exchange open (index.go's Summary.Seq doc).
	s.nextPublishSeq++
	s.maxPublishSeq.Store(s.nextPublishSeq)

	entry := exchangeEntry{
		Summary: Summary{
			ID:            ex.ID,
			ConnID:        ex.ConnID,
			Seq:           s.nextPublishSeq,
			ClientKey:     ex.ClientLFDI,
			Started:       ex.Started,
			Ended:         ex.Ended,
			Mark:          ex.Mark,
			Error:         ex.Error,
			HandlerRuns:   ex.HandlerRuns,
			Method:        method,
			Path:          path,
			Status:        status,
			ReqTrueLen:    ex.Request.TrueLen,
			RespTrueLen:   ex.Response.TrueLen,
			ReqStored:     reqLen,
			RespStored:    respLen,
			ReqTruncated:  ex.Request.Truncated,
			RespTruncated: ex.Response.Truncated,
		},
		clientSFDI: ex.ClientSFDI,
		segment:    s.active.number,
		offset:     offset,
		memBytes:   entrySize,
	}
	s.idx.add(entry)
	s.publish(entry.Summary)
}

// ensureRoomFor evicts whole segments, oldest first, until the directory
// has room for need more bytes AND the index has room for one more entry of
// entryEstimate bytes, or only the active segment is left with nothing more
// to evict (Q4: the active segment is never deleted). The disk cap can
// therefore return with the directory still over capBytes, by up to one
// record on top of one segment: an unavoidable transient, since evicting
// the segment currently being written to is not possible.
//
// The index memory budget (operator decision 2026-09-22) cannot rely on
// that same transient allowance: with small exchanges, the index fills far
// faster than the active segment fills on disk, so waiting for a disk-size
// rollover would let the index grow past its budget by as much as one
// whole segment's entries before anything is ever evicted. So when only
// the active segment is left and the index is still over budget, this
// rolls it early (rolledForIndex), turning it into an ordinary evictable
// segment, and evicts it on the next pass. Rolling is attempted at most
// once per call, and only when the active segment already holds an entry
// to evict: otherwise a budget smaller than one entry's own estimate would
// roll and evict empty segments forever.
func (s *Store) ensureRoomFor(need, entryEstimate int64) error {
	rolledForIndex := false
	for len(s.liveSegs) > 0 {
		overDisk := s.totalOnDisk.Load()+need > s.capBytes
		overIndex := s.idx.approxMemBytes()+entryEstimate > s.indexMemBudget
		if !overDisk && !overIndex {
			break
		}
		oldest := s.liveSegs[0]
		if s.active != nil && oldest.number == s.active.number {
			if !overIndex || rolledForIndex || s.active.size == 0 {
				break
			}
			if err := s.rollSegment(); err != nil {
				return err
			}
			rolledForIndex = true
			continue
		}
		if err := s.evictOldest(overIndex); err != nil {
			return err
		}
	}
	return nil
}

// evictOldest deletes the oldest live segment (which ensureRoomFor has
// already confirmed is not the active one) and removes its entries from
// the index. forIndexMemory is true when this eviction was needed for the
// index memory budget, per the operator's 2026-09-22 decision. Exactly one
// of EvictedSegments and IndexMemoryEvictions is incremented, so the two
// counters are disjoint and sum to every eviction, matching their own doc
// comments (Stats, in store.go): a cause is charged to the index budget
// whenever it was over, even if the disk cap was over at the same time.
func (s *Store) evictOldest(forIndexMemory bool) error {
	num := s.liveSegs[0].number
	size := s.liveSegs[0].size
	if err := os.Remove(s.segmentPath(num)); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("sep2capture: evict segment %d: %w", num, err)
	}
	s.liveSegs = s.liveSegs[1:]
	s.totalOnDisk.Add(-size)
	if forIndexMemory {
		s.indexMemoryEvictions.Add(1)
	} else {
		s.evictedSegments.Add(1)
	}
	s.idx.evictSegment(num)
	return nil
}

// rollSegment closes the previous active segment, if any (leaving it open
// until a GC finalizer gets around to it let disk use run past the hard
// cap, since a deleted-but-open file's space is never freed), then opens
// the next one.
func (s *Store) rollSegment() error {
	prev := s.active
	num := s.nextSegNum
	s.nextSegNum++
	f, err := os.OpenFile(s.segmentPath(num), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("sep2capture: create segment %d: %w", num, err)
	}
	if prev != nil {
		if cerr := prev.f.Close(); cerr != nil {
			s.logWriteErr(fmt.Errorf("sep2capture: close segment %d after roll: %w", prev.number, cerr))
		}
	}
	s.active = &segmentFile{number: num, f: f}
	s.liveSegs = append(s.liveSegs, liveSegment{number: num})
	return nil
}

func (s *Store) setLiveSegSize(num, size int64) {
	if n := len(s.liveSegs); n > 0 && s.liveSegs[n-1].number == num {
		s.liveSegs[n-1].size = size
	}
}

// failActiveSegment abandons the active segment after a write error: the
// record that failed is never indexed, so Exchange can never be asked for
// it, but a header or a partial payload may already be on disk ahead of
// the failure. It reconciles the tracked size against the file's real
// size before closing, so ensureRoomFor's next cap check is never fooled
// by bytes that are on disk but were never counted.
func (s *Store) failActiveSegment(err error) {
	s.droppedWriteError.Add(1)
	s.logWriteErr(err)
	if s.active == nil {
		return
	}
	if fi, statErr := s.active.f.Stat(); statErr == nil && fi.Size() > s.active.size {
		diff := fi.Size() - s.active.size
		s.active.size = fi.Size()
		s.totalOnDisk.Add(diff)
		s.setLiveSegSize(s.active.number, s.active.size)
	}
	_ = s.active.f.Close()
	s.active = nil
}

// logWriteErr logs at most once a minute (Q4), so a persistently failing
// disk cannot flood the error log at exchange rate.
func (s *Store) logWriteErr(err error) {
	s.writeErrLogMu.Lock()
	defer s.writeErrLogMu.Unlock()
	if time.Since(s.writeErrLogAt) < time.Minute {
		return
	}
	s.writeErrLogAt = time.Now()
	s.errorLog.Printf("sep2capture: store write error: %v", err)
}

// publish pushes sum to every live subscriber (Subscribe, in
// store_reader.go) without blocking on a slow one: a full subscriber
// channel ends that subscription right here rather than leaving it open to
// silently miss whatever else publishes while it stays behind (PR 620
// review, HIGH: a slow reader's stream never closed, so its browser never
// reconnected to resume the gap). handleStream's `open` check on its next
// select then returns, ending the response so EventSource reconnects and
// resumes from the last Seq it saw. SlowSubscribers therefore counts
// readers dropped, matching its own doc (Stats, store.go) and the route
// doc (handler.go): one increment per ended subscription, not one per lost
// update.
//
// Every send attempt below runs while subMu is held, exactly as it did
// before this change: publish is the only sender (called only from this
// store's one writer goroutine, never concurrently with itself), and
// closeSubscription's own delete also runs under subMu, so holding the
// lock across the whole loop is what stops a concurrent ctx.Done() close
// from ever closing sub.ch while a send to it is in flight. The actual
// close (in closeSubscription, called once the lock is released) is safe
// precisely because nothing sends to a dropped sub again: the next
// publish call re-reads s.subs, which by then no longer holds it.
func (s *Store) publish(sum Summary) {
	s.subMu.Lock()
	var dropped []*subscription
	for sub := range s.subs {
		select {
		case sub.ch <- sum:
		default:
			s.slowSubscribers.Add(1)
			dropped = append(dropped, sub)
		}
	}
	s.subMu.Unlock()

	for _, sub := range dropped {
		// force=true: a write may genuinely be blocked on this stalled
		// reader's own connection (closeSubscription's doc,
		// store_reader.go).
		s.closeSubscription(sub, true)
	}
}
