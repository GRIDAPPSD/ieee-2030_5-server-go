package subscription

import (
	"testing"
	"time"
)

// TestNotificationClientTimeoutSet asserts that the http.Client used to
// deliver notifications has a non-zero Timeout. A zero Timeout means the
// client will wait forever for a slow/unresponsive subscriber, leaking the
// worker goroutine for the lifetime of the blocked POST.
func TestNotificationClientTimeoutSet(t *testing.T) {
	client := newNotificationClient()
	if client.Timeout == 0 {
		t.Fatal("notification client Timeout is 0 (no limit); worker goroutines may block forever on slow subscribers")
	}
	if client.Timeout < time.Second {
		t.Errorf("notification client Timeout %v is below 1-second sanity floor", client.Timeout)
	}
}

// TestNotificationClientTimeoutConstant asserts the named constant is positive.
func TestNotificationClientTimeoutConstant(t *testing.T) {
	if notificationClientTimeout <= 0 {
		t.Errorf("notificationClientTimeout must be positive, got %v", notificationClientTimeout)
	}
}
