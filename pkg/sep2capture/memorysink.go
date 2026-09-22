package sep2capture

import "sync"

// MemorySink stores every exchange it receives in a growing slice. It has
// no cap and no eviction: PR 3's segmented log replaces it for production
// use. It exists so this PR can prove recording end to end without disk
// storage, and it is safe for concurrent Record calls.
type MemorySink struct {
	mu        sync.Mutex
	exchanges []Exchange
}

// NewMemorySink returns an empty MemorySink.
func NewMemorySink() *MemorySink {
	return &MemorySink{}
}

// Record appends ex. Implements Sink.
func (m *MemorySink) Record(ex Exchange) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.exchanges = append(m.exchanges, ex)
}

// All returns a copy of every exchange recorded so far, in the order
// Record received them.
func (m *MemorySink) All() []Exchange {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Exchange, len(m.exchanges))
	copy(out, m.exchanges)
	return out
}

var _ Sink = (*MemorySink)(nil)
