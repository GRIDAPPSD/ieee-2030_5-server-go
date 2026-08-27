package auth

import (
	"sync"
	"time"
)

// TicketStore issues and validates short-lived, one-time-use auth tickets.
// Tickets are used by browser SSE clients (EventSource) that cannot send
// Authorization headers. A valid admin session exchanges its Bearer token
// for a ticket, then passes the ticket as a query parameter.
//
// One-time use is load-bearing here and must not be relaxed: the value
// travels in a URL, so it also lands in browser history, autocomplete, any
// outbound Referer and the access log. Cookie sessions are a different
// credential kind and live in SessionStore.
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
	ticket, err := newRandomID(32)
	if err != nil {
		return "", err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.purgeExpiredLocked()
	s.tickets[ticket] = time.Now().Add(s.ttl)
	return ticket, nil
}

// Redeem validates and consumes a ticket. Returns true if the ticket was
// valid and not expired. Only a valid ticket is consumed: the expiry compare
// runs before the delete so an already-expired entry is left for the sweep
// rather than being reported as a redemption that happened.
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
	if !time.Now().Before(exp) {
		return false
	}
	delete(s.tickets, ticket)
	return true
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
