package auth

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"sync"
	"time"
)

// ErrSessionStoreFull reports that the session table is at capacity. Issue
// fails closed rather than evicting a live operator session, so a caller
// that hits this refuses the login instead of silently displacing someone.
var ErrSessionStoreFull = errors.New("auth: admin session store full")

// maxSessions bounds the table. Only a successful admin-key login mints a
// session, so the cap guards against a leaked key, not ordinary use.
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

// Issue mints a new session id.
func (s *SessionStore) Issue() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	id := hex.EncodeToString(b)

	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.purgeExpiredLocked(now)
	if len(s.sessions) >= maxSessions {
		return "", ErrSessionStoreFull
	}
	s.sessions[id] = session{
		idleDeadline: now.Add(s.idle),
		absDeadline:  now.Add(s.absolute),
	}
	return id, nil
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
