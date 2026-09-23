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
		Seq:        entry.Seq,
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
// PR 620 review: resuming on ID missed or duplicated exchanges whenever
// two connections on one client finished out of id order). The scan
// itself is still a full pass over the index rather than a maintained
// global order, deliberately: it only runs once per (re)connect, not on
// the hot path Q5 is about. The result slice is grown by append rather
// than pre-sized to the whole index, and sorted after idx.mu is released
// (PR 620 review, MEDIUM: a reconnect missing a handful of events used to
// allocate and sort a slice sized for the whole index while holding the
// lock the writer goroutine also needs), so a reconnect near the head of
// a large index costs proportional to what it replays.
func (s *Store) summariesAfter(afterSeq uint64) []Summary {
	// Scanned in its own function so the deferred Unlock runs even if a
	// panic interrupts the scan (PR 620 review, LOW: a bare Lock/Unlock
	// pair left idx.mu locked for the life of the process on a panic
	// between them), while still releasing the lock before the sort
	// below runs, exactly as the round 2 fix intended (this doc's next
	// paragraph).
	out := func() []Summary {
		s.idx.mu.Lock()
		defer s.idx.mu.Unlock()
		var out []Summary
		for _, e := range s.idx.byID {
			if e.Seq > afterSeq {
				out = append(out, e.Summary)
			}
		}
		return out
	}()

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
	// forceExpire, when set, is called once as part of ending this
	// subscription: handleStream sets it to force its own connection's
	// write deadline into the past (PR 620 review, MEDIUM: a write
	// already blocked on a stalled client waited out its own deadline,
	// up to a minute in production, regardless of why the subscription
	// ended). net.Conn's SetWriteDeadline "sets the deadline for future
	// Write calls and any currently-blocked Write call" and is safe to
	// call from another goroutine (net.Conn: "multiple goroutines may
	// invoke methods on a Conn simultaneously").
	forceExpire func()
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
// funnels through, and forceExpire (nil is fine) runs there too, once,
// however the subscription ends.
//
// A Subscribe that arrives after Store.Close has already run is refused: it
// gets an already-closed channel back rather than being registered, since
// closeAllSubscribers has already taken its one snapshot of subs and would
// never see a later addition (#628 fix round 1, silent-failure MEDIUM: a
// stream subscribed after Close previously registered and then never
// ended, holding an admin shutdown open for as long as the client stayed
// connected). The check and the registration both run under intakeMu's
// read lock, the same lock Close takes exclusively to set closed=true, so
// either this call's RLock is strictly before Close's Lock (sub is
// registered before Close's own snapshot, and closeAllSubscribers will
// close it) or strictly after Close's Unlock (closed is already true, and
// sub is refused): there is no window where a subscription is registered
// but closeAllSubscribers has already run.
func (s *Store) Subscribe(ctx context.Context, forceExpire func()) <-chan Summary {
	sub := &subscription{ch: make(chan Summary, subscriberBufferSize), forceExpire: forceExpire}

	s.intakeMu.RLock()
	if s.closed {
		s.intakeMu.RUnlock()
		close(sub.ch)
		return sub.ch
	}
	s.subMu.Lock()
	s.subs[sub] = struct{}{}
	s.subMu.Unlock()
	s.intakeMu.RUnlock()

	if subscribeHook != nil {
		subscribeHook(sub.ch)
	}

	go func() {
		<-ctx.Done()
		// force=false: ctx ending already means the connection is going
		// (closeSubscription's doc below), so nothing here needs
		// forceExpire's own write-deadline override.
		s.closeSubscription(sub, false)
	}()

	return sub.ch
}

// closeSubscription removes sub from the live set and closes its channel,
// exactly once regardless of how many of Subscribe's three end paths call
// it concurrently for the same sub. force is true only on the two paths
// where a write may genuinely be blocked on a connection that is not
// already closing on its own (publish's slow-reader drop, and
// Store.Close): only those call forceExpire. The subscription's own
// ctx.Done() path always passes force=false (PR 620 review, MEDIUM:
// forceExpire's SetWriteDeadline(now) otherwise logged "use of closed
// network connection" on every ordinary disconnect, once net/http had
// already closed the connection that ended ctx, and could hold the
// shared once-a-minute log throttle against the genuine case
// forceExpire exists for).
func (s *Store) closeSubscription(sub *subscription, force bool) {
	sub.once.Do(func() {
		if force && sub.forceExpire != nil {
			sub.forceExpire()
		}
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
		// force=true: a shutdown must not wait out a write already
		// blocked on a stalled client (closeSubscription's doc above).
		s.closeSubscription(sub, true)
	}
}
