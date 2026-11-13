package derwrite

import (
	"testing"
	"time"
)

func TestOwner_ZeroValueIsUsable(t *testing.T) {
	var o Owner
	o.Lock()
	o.Unlock()
	o.Lock()
	o.Unlock()
}

func TestOwner_LockExcludesASecondLock(t *testing.T) {
	var o Owner
	o.Lock()
	acquired := make(chan struct{})
	go func() {
		o.Lock()
		close(acquired)
		o.Unlock()
	}()
	select {
	case <-acquired:
		t.Fatal("second Lock returned while the first was held")
	case <-time.After(100 * time.Millisecond):
	}
	o.Unlock()
	select {
	case <-acquired:
	case <-time.After(5 * time.Second):
		t.Fatal("second Lock never returned after Unlock")
	}
}
