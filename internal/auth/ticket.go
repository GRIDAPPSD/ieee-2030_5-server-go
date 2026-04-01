package auth

import (
	"crypto/rand"
	"encoding/hex"
	"sync"
	"time"
)

// TicketStore issues and validates short-lived, one-time-use auth tickets.
// Tickets are used by browser SSE clients (EventSource) that cannot send
// Authorization headers. A valid admin session exchanges its Bearer token
// for a ticket, then passes the ticket as a query parameter.
type TicketStore struct {
	mu      sync.Mutex
	tickets map[string]time.Time
	ttl     time.Duration
}

// NewTicketStore creates a ticket store with the given TTL for tickets.
func NewTicketStore(ttl time.Duration) *TicketStore {
	return &TicketStore{
		tickets: make(map[string]time.Time),
		ttl:     ttl,
	}
}

// Issue creates a new ticket and returns it. The ticket is valid for the
// store's configured TTL and can only be redeemed once.
func (s *TicketStore) Issue() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	ticket := hex.EncodeToString(b)

	s.mu.Lock()
	defer s.mu.Unlock()
	s.purgeExpiredLocked()
	s.tickets[ticket] = time.Now().Add(s.ttl)
	return ticket, nil
}

// Redeem validates and consumes a ticket. Returns true if the ticket was
// valid and not expired. The ticket is deleted regardless of outcome.
func (s *TicketStore) Redeem(ticket string) bool {
	if ticket == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	exp, ok := s.tickets[ticket]
	if !ok {
		return false
	}
	delete(s.tickets, ticket)
	return time.Now().Before(exp)
}

// Len returns the number of outstanding tickets (for testing).
func (s *TicketStore) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.tickets)
}

func (s *TicketStore) purgeExpiredLocked() {
	now := time.Now()
	for k, exp := range s.tickets {
		if now.After(exp) {
			delete(s.tickets, k)
		}
	}
}
