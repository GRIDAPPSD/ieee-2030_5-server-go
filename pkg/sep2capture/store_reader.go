package sep2capture

import (
	"context"
	"os"
	"sort"
)

// Clients returns every client this Store has ever seen, sorted by key.
// A client's row survives every exchange it once held being evicted: only
// ExchangeCount, FirstSeen and LastSeen remain (Q4).
func (s *Store) Clients() []ClientSummary {
	s.idx.mu.Lock()
	defer s.idx.mu.Unlock()

	out := make([]ClientSummary, 0, len(s.idx.byClient))
	for _, c := range s.idx.byClient {
		out = append(out, c.ClientSummary)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

// Exchanges returns client's exchanges with id > afterID, oldest first, up
// to limit (0 or negative means no limit). Unknown clients and clients
// with nothing after afterID both return nil, not an error: neither is
// distinguishable from "nothing new yet" to a caller polling for updates.
func (s *Store) Exchanges(client string, afterID uint64, limit int) []Summary {
	s.idx.mu.Lock()
	defer s.idx.mu.Unlock()

	c, ok := s.idx.byClient[client]
	if !ok {
		return nil
	}
	start := sort.Search(len(c.ids), func(i int) bool { return c.ids[i] > afterID })
	rest := c.ids[start:]
	if limit > 0 && len(rest) > limit {
		rest = rest[:limit]
	}
	out := make([]Summary, 0, len(rest))
	for _, id := range rest {
		if e, ok := s.idx.byID[id]; ok {
			out = append(out, e.Summary)
		}
	}
	return out
}

// Exchange returns id's exact bytes and metadata, reading its segment
// file directly rather than from anything cached: the payload is never
// held in memory outside the writer's own hand-off. ErrEvicted covers both
// an id whose segment has since been deleted and one that raced eviction
// between the index lookup and the file read (Q4); ErrNotFound covers an
// id above the highest this Store has ever indexed.
func (s *Store) Exchange(id uint64) (Exchange, error) {
	s.idx.mu.Lock()
	e, ok := s.idx.byID[id]
	var entry exchangeEntry
	if ok {
		entry = *e
	}
	maxSeen := s.idx.maxSeen
	s.idx.mu.Unlock()

	if !ok {
		if id <= maxSeen {
			return Exchange{}, ErrEvicted
		}
		return Exchange{}, ErrNotFound
	}

	f, err := os.Open(s.segmentPath(entry.segment))
	if err != nil {
		return Exchange{}, ErrEvicted
	}
	defer func() { _ = f.Close() }()

	payload := make([]byte, entry.ReqStored+entry.RespStored)
	if _, err := f.ReadAt(payload, entry.offset+int64(recordHeaderLen)); err != nil {
		return Exchange{}, ErrEvicted
	}

	return Exchange{
		ID:         entry.ID,
		ConnID:     entry.ConnID,
		ClientLFDI: entry.ClientKey,
		ClientSFDI: entry.clientSFDI,
		Started:    entry.Started,
		Ended:      entry.Ended,
		Request: Direction{
			Bytes:     payload[:entry.ReqStored],
			TrueLen:   entry.ReqTrueLen,
			Truncated: entry.ReqTruncated,
		},
		Response: Direction{
			Bytes:     payload[entry.ReqStored:],
			TrueLen:   entry.RespTrueLen,
			Truncated: entry.RespTruncated,
		},
		Mark:        entry.Mark,
		Error:       entry.Error,
		HandlerRuns: entry.HandlerRuns,
	}, nil
}

// summariesAfter returns every indexed Summary with ID > afterID, sorted
// by ID, across every client. It backs the SSE resume path (Handler,
// handler_sse.go): a reconnecting client's after= or Last-Event-ID names
// an id, not a client, since the stream itself is not scoped to one. This
// is a full scan of the index rather than a maintained global order,
// deliberately: it only runs once per (re)connect, not on the hot path Q5
// is about, and out-of-order arrival (index.go) means a per-client sorted
// slice like Exchanges' cannot be reused across clients without one.
func (s *Store) summariesAfter(afterID uint64) []Summary {
	s.idx.mu.Lock()
	defer s.idx.mu.Unlock()

	out := make([]Summary, 0, len(s.idx.byID))
	for id, e := range s.idx.byID {
		if id > afterID {
			out = append(out, e.Summary)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// Subscribe returns a channel of every Summary recorded after the call,
// closed when ctx ends. The store, not the caller, closes it (channels are
// closed by the sender): a goroutine tied to ctx removes and closes it,
// and publish (segment_writer.go) never blocks a slow reader, dropping
// instead and counting SlowSubscribers.
func (s *Store) Subscribe(ctx context.Context) <-chan Summary {
	ch := make(chan Summary, subscriberBufferSize)

	s.subMu.Lock()
	s.subs[ch] = struct{}{}
	s.subMu.Unlock()

	go func() {
		<-ctx.Done()
		s.subMu.Lock()
		delete(s.subs, ch)
		s.subMu.Unlock()
		close(ch)
	}()

	return ch
}
