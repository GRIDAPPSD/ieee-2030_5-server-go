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

	entry := exchangeEntry{
		Summary: Summary{
			ID:            ex.ID,
			ConnID:        ex.ConnID,
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
// entryEstimate bytes, or only the active segment is left (Q4: the active
// segment is never deleted). The index memory budget (operator decision
// 2026-09-22) evicts early, on the same segments, exactly like the disk
// cap: with small exchanges the index fills first, so retained history can
// end up under the 600 MB disk cap. It can therefore return with the
// directory still over capBytes, or the index still over its budget, by up
// to one record/entry on top of one segment: an unavoidable transient, not
// a bug, since evicting the segment currently being written to is not
// possible.
func (s *Store) ensureRoomFor(need, entryEstimate int64) error {
	for len(s.liveSegs) > 0 {
		overDisk := s.totalOnDisk.Load()+need > s.capBytes
		overIndex := s.idx.approxMemBytes()+entryEstimate > s.indexMemBudget
		if !overDisk && !overIndex {
			break
		}
		oldest := s.liveSegs[0]
		if s.active != nil && oldest.number == s.active.number {
			break
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
// index memory budget (counted separately from EvictedSegments, per the
// operator's 2026-09-22 decision), whether or not the disk cap was also
// over at the time.
func (s *Store) evictOldest(forIndexMemory bool) error {
	num := s.liveSegs[0].number
	size := s.liveSegs[0].size
	if err := os.Remove(s.segmentPath(num)); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("sep2capture: evict segment %d: %w", num, err)
	}
	s.liveSegs = s.liveSegs[1:]
	s.totalOnDisk.Add(-size)
	s.evictedSegments.Add(1)
	if forIndexMemory {
		s.indexMemoryEvictions.Add(1)
	}
	s.idx.evictSegment(num)
	return nil
}

// rollSegment closes the previous active segment, if any (security lane
// M3: leaving it open until a GC finalizer gets around to it let disk use
// run past the hard cap, since a deleted-but-open file's space is never
// freed), then opens the next one.
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
// channel counts as SlowSubscribers and drops this update for that reader
// rather than stalling the writer goroutine.
func (s *Store) publish(sum Summary) {
	s.subMu.Lock()
	defer s.subMu.Unlock()
	for ch := range s.subs {
		select {
		case ch <- sum:
		default:
			s.slowSubscribers.Add(1)
		}
	}
}
