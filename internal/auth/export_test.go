package auth

import "time"

// SetClock replaces the store's time source for tests in package auth_test.
func (s *SessionStore) SetClock(now func() time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.now = now
}
