package auth

import (
	"sync"
	"time"
)

// maxSessions bounds the table's memory, and that is all it does. At the cap
// Issue evicts the oldest session rather than refusing, because refusing turns
// the cap into a lockout: a browser that discards the Secure cookie makes
// every retry a successful mint that is never validated, so it holds a slot
// for the whole idle window and, with no logout route, the operator's next
// login fails until entries age out.
//
// Eviction cannot be abused to displace an operator. Reaching Issue requires
// passing the constant-time key compare, so anyone who can drive it already
// holds the admin key and already has full access.
const maxSessions = 64

// SessionStore holds browser admin sessions for the admin_ticket cookie.
//
// It is deliberately NOT a TicketStore. A ticket travels in a URL and is
// consumed on presentation; a session travels only in an HttpOnly cookie and
// is validated without being consumed, because one page load authenticates
// the document and each of its subresources separately. Keeping the two in
// separate stores also makes their values non-interchangeable.
type SessionStore struct {
	mu       sync.Mutex
	sessions map[string]session
	idle     time.Duration
	absolute time.Duration
}

type session struct {
	// issuedAt is the eviction order. Deriving it from absDeadline would work
	// only while every session shares one absolute lifetime, which is a
	// coincidence of the current construction rather than a property.
	issuedAt     time.Time
	idleDeadline time.Time
	absDeadline  time.Time
}

// NewSessionStore creates a session store. idle is the inactivity window,
// refreshed on each successful Validate. absolute is the total lifetime
// fixed at mint and never extended, so a continuously used session still
// expires.
func NewSessionStore(idle, absolute time.Duration) *SessionStore {
	return &SessionStore{
		sessions: make(map[string]session),
		idle:     idle,
		absolute: absolute,
	}
}

// Issue mints a new session id. The only error it can return is a failure of
// the system random source; capacity is handled by eviction, so a caller that
// gets an error has a broken host rather than a full table.
func (s *SessionStore) Issue() (string, error) {
	id, err := newRandomID(32)
	if err != nil {
		return "", err
	}

	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.purgeExpiredLocked(now)
	for len(s.sessions) >= maxSessions {
		s.evictOldestLocked()
	}
	s.sessions[id] = session{
		issuedAt:     now,
		idleDeadline: now.Add(s.idle),
		absDeadline:  now.Add(s.absolute),
	}
	return id, nil
}

// evictOldestLocked removes the session minted longest ago. The loop in Issue
// calls it until there is room, so an empty table cannot spin here: the caller
// only enters the loop when at least maxSessions entries exist.
func (s *SessionStore) evictOldestLocked() {
	var oldestID string
	var oldestAt time.Time
	for id, sess := range s.sessions {
		if oldestID == "" || sess.issuedAt.Before(oldestAt) {
			oldestID, oldestAt = id, sess.issuedAt
		}
	}
	delete(s.sessions, oldestID)
}

// Validate reports whether id names a live session, WITHOUT consuming it,
// and slides the idle deadline forward on success. The absolute deadline is
// never moved, so sliding cannot make a session immortal.
func (s *SessionStore) Validate(id string) bool {
	if id == "" {
		return false
	}
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()

	// Sweeping here, not only in Issue, keeps a store that has stopped
	// minting from holding expired entries forever, and leaves the lookup
	// below unable to see an expired session.
	s.purgeExpiredLocked(now)

	sess, ok := s.sessions[id]
	if !ok {
		return false
	}
	sess.idleDeadline = now.Add(s.idle)
	if sess.idleDeadline.After(sess.absDeadline) {
		sess.idleDeadline = sess.absDeadline
	}
	s.sessions[id] = sess
	return true
}

// Delete removes a session. A logout route is the intended caller; there is
// no such route today.
func (s *SessionStore) Delete(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.sessions, id)
}

// Len returns the number of live sessions (for testing).
func (s *SessionStore) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.sessions)
}

func (s *SessionStore) purgeExpiredLocked(now time.Time) {
	for id, sess := range s.sessions {
		if sessionExpired(sess, now) {
			delete(s.sessions, id)
		}
	}
}

func sessionExpired(sess session, now time.Time) bool {
	return !now.Before(sess.idleDeadline) || !now.Before(sess.absDeadline)
}
