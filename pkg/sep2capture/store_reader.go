package sep2capture

import (
	"context"
	"os"
	"sort"
	"sync"
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

// summariesAfter returns every indexed Summary with Seq > afterSeq, sorted
// by Seq, across every client. It backs the SSE resume path (Handler,
// handler_sse.go): a reconnecting client's after= or Last-Event-ID names a
// Seq, not an exchange id or a client, since the stream itself is not
// scoped to one and ids arrive out of order (index.go's Summary.Seq doc;
// PR 620 review, HIGH: resuming on ID missed or duplicated exchanges
// whenever two connections on one client finished out of id order). This
// is a full scan of the index rather than a maintained global order,
// deliberately: it only runs once per (re)connect, not on the hot path Q5
// is about.
func (s *Store) summariesAfter(afterSeq uint64) []Summary {
	s.idx.mu.Lock()
	defer s.idx.mu.Unlock()

	out := make([]Summary, 0, len(s.idx.byID))
	for _, e := range s.idx.byID {
		if e.Seq > afterSeq {
			out = append(out, e.Summary)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Seq < out[j].Seq })
	return out
}

// subscription is one GET /stream reader's channel plus the guard that
// makes ending it idempotent: a subscription can be ended three different
// ways (its own ctx.Done(), publish's slow-reader drop, or Store.Close
// ending every stream at once), and closeSubscription is the one place
// that actually calls close(ch), so a race between any two of those paths
// closes it exactly once rather than panicking on a double close.
type subscription struct {
	ch   chan Summary
	once sync.Once
}

// subscribeHook, when set by a test in this package, runs synchronously
// right after Subscribe registers sub, with the raw bidirectional channel
// (Subscribe's own return value is receive-only). It lets a test inject a
// value directly, simulating publish's own race with a history replay
// that reads sub before handleStream does, deterministically rather than
// by racing the scheduler for it. Production never sets it.
var subscribeHook func(ch chan Summary)

// Subscribe returns a channel of every Summary recorded after the call,
// closed when ctx ends, when this subscriber falls too far behind
// (publish, segment_writer.go), or when Store.Close ends every open
// stream. The store, not the caller, closes it (channels are closed by the
// sender): closeSubscription is the single path every one of those closes
// funnels through.
func (s *Store) Subscribe(ctx context.Context) <-chan Summary {
	sub := &subscription{ch: make(chan Summary, subscriberBufferSize)}

	s.subMu.Lock()
	s.subs[sub] = struct{}{}
	s.subMu.Unlock()

	if subscribeHook != nil {
		subscribeHook(sub.ch)
	}

	go func() {
		<-ctx.Done()
		s.closeSubscription(sub)
	}()

	return sub.ch
}

// closeSubscription removes sub from the live set and closes its channel,
// exactly once regardless of how many of Subscribe's three end paths call
// it concurrently for the same sub.
func (s *Store) closeSubscription(sub *subscription) {
	sub.once.Do(func() {
		s.subMu.Lock()
		delete(s.subs, sub)
		s.subMu.Unlock()
		close(sub.ch)
	})
}

// closeAllSubscribers ends every open GET /stream subscription (Store.Close):
// snapshotting the live set before closing avoids mutating subs while
// ranging over it, since closeSubscription itself deletes from subs under
// subMu.
func (s *Store) closeAllSubscribers() {
	s.subMu.Lock()
	subs := make([]*subscription, 0, len(s.subs))
	for sub := range s.subs {
		subs = append(subs, sub)
	}
	s.subMu.Unlock()

	for _, sub := range subs {
		s.closeSubscription(sub)
	}
}
