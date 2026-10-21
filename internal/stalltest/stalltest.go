// Package stalltest is the one check tests use to show that a call returns
// while the test holds a path the call must not wait on.
package stalltest

import (
	"testing"
	"time"
)

// CallBound is how long a call gets to return while the test holds the
// path. The path is released only at cleanup, so the bound only separates
// "returned" from "waits on the held path" and sits far above any
// scheduling delay.
const CallBound = 10 * time.Second

// Returns runs call and fails the test if it has not returned within
// CallBound. The call runs on its own goroutine so a blocked call fails the
// test instead of hanging it.
func Returns(t testing.TB, what string, call func()) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		call()
	}()
	select {
	case <-done:
	case <-time.After(CallBound):
		t.Fatalf("%s did not return within %v while the test held the path it must not wait on", what, CallBound)
	}
}
