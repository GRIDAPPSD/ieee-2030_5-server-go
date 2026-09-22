package sep2capture

import (
	"sort"
	"sync"
	"time"
)

// Summary is one exchange's index entry: everything Exchanges and the
// exchange list need to render without reading its bytes off disk (Q4).
type Summary struct {
	ID            uint64
	ConnID        uint64
	ClientKey     string // ClientLFDI, or "" when the connection had none
	Started       time.Time
	Ended         time.Time
	Mark          Mark
	Error         string
	HandlerRuns   int
	Method        string // from the request's first line, "" if unparseable
	Path          string // capped at maxIndexedPath bytes
	Status        int    // from the response's first line, 0 if none
	ReqTrueLen    int64
	RespTrueLen   int64
	ReqStored     int64 // bytes actually on disk for this direction
	RespStored    int64
	ReqTruncated  bool
	RespTruncated bool
}

// ClientSummary is one client's row for Clients(): counts survive
// eviction even after every exchange id behind them is gone (Q4).
type ClientSummary struct {
	Key           string
	FirstSeen     time.Time
	LastSeen      time.Time
	ExchangeCount uint64
}

// exchangeEntry is Summary plus what Exchange needs to read the payload
// back: which segment, and the byte offset of that record's header within
// it. clientSFDI is kept here rather than on Summary because the reader
// list methods (Q4) only ever describe an exchange by ClientKey (LFDI, or
// remote host when there is none); SFDI is only needed to reconstruct a
// full Exchange value.
type exchangeEntry struct {
	Summary
	clientSFDI string
	segment    int64
	offset     int64
}

// clientEntry is one client's row plus the exchange ids recorded for it,
// kept sorted ascending by id (not append order) so Exchanges' binary
// search on afterID is valid even when a client's connections finish out
// of id order: a later-opened connection can complete, and so reach
// Record, before an earlier one on the same client does.
type clientEntry struct {
	ClientSummary
	ids []uint64
}

// index is the in-memory index Q4 describes: per-exchange, per-client, and
// (via segIDs) per-segment, so evictSegment can find exactly what one
// deleted file's entries were without scanning the whole map. Only the
// writer goroutine ever calls add or evictSegment; the reader methods in
// store_reader.go take mu for every access, including their own.
type index struct {
	mu       sync.Mutex
	byID     map[uint64]*exchangeEntry
	byClient map[string]*clientEntry
	segIDs   map[int64][]uint64
	maxSeen  uint64
}

func newIndex() *index {
	return &index{
		byID:     make(map[uint64]*exchangeEntry),
		byClient: make(map[string]*clientEntry),
		segIDs:   make(map[int64][]uint64),
	}
}

// add records one exchange written to segment/offset. Called only from the
// writer goroutine, once per exchange, in write order.
func (x *index) add(e exchangeEntry) {
	x.mu.Lock()
	defer x.mu.Unlock()

	x.byID[e.ID] = &e
	if e.ID > x.maxSeen {
		x.maxSeen = e.ID
	}
	x.segIDs[e.segment] = append(x.segIDs[e.segment], e.ID)

	c := x.byClient[e.ClientKey]
	if c == nil {
		c = &clientEntry{ClientSummary: ClientSummary{Key: e.ClientKey, FirstSeen: e.Started}}
		x.byClient[e.ClientKey] = c
	}
	c.LastSeen = e.Ended
	c.ExchangeCount++
	c.ids = insertSorted(c.ids, e.ID)
}

// insertSorted inserts id into a slice already sorted ascending, keeping
// it sorted. ids are assigned globally, not per client, so a client with
// more than one live connection can hand Record two exchanges out of id
// order; this keeps the per-client slice usable for a binary search
// regardless.
func insertSorted(ids []uint64, id uint64) []uint64 {
	i := sort.Search(len(ids), func(i int) bool { return ids[i] >= id })
	ids = append(ids, 0)
	copy(ids[i+1:], ids[i:])
	ids[i] = id
	return ids
}

// removeSorted removes id from a slice sorted ascending, if present.
func removeSorted(ids []uint64, id uint64) []uint64 {
	i := sort.Search(len(ids), func(i int) bool { return ids[i] >= id })
	if i < len(ids) && ids[i] == id {
		ids = append(ids[:i], ids[i+1:]...)
	}
	return ids
}

// evictSegment removes every index entry that segment num held: called
// only by the writer goroutine, only for the oldest live segment
// (ensureRoomFor in segment_writer.go), right before that segment's file
// is deleted. Exchange counts on the affected clients are left untouched:
// per Q4, "counts survive eviction; ids do not."
func (x *index) evictSegment(num int64) {
	x.mu.Lock()
	defer x.mu.Unlock()

	ids := x.segIDs[num]
	delete(x.segIDs, num)
	for _, id := range ids {
		e, ok := x.byID[id]
		if !ok {
			continue
		}
		delete(x.byID, id)
		if c := x.byClient[e.ClientKey]; c != nil {
			c.ids = removeSorted(c.ids, id)
		}
	}
}
