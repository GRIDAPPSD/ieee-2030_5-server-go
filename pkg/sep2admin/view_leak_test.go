package sep2admin

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"sync"
	"testing"
	"time"
)

// TestInvokeViewLateReturnDoesNotLeakItsSenderGoroutine pins the channel
// buffer as a safety property: the silent-failure review found that
// dropping it passes the whole suite while reintroducing a permanent leak
// for any View that outlives its deadline and then returns.
//
// Every InvokeView call here has already returned via the ctx.Done() case
// (proved by wg.Wait completing) before release is closed, so the View's
// goroutine attempts done <- result only after nothing is reading done any
// more. With the buffer, that send never blocks and the goroutine exits;
// without it, every one of those goroutines blocks on the send forever.
func TestInvokeViewLateReturnDoesNotLeakItsSenderGoroutine(t *testing.T) {
	const (
		n       = 20
		timeout = 20 * time.Millisecond
	)

	baseline := settledGoroutineCount(t)

	release := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			p := graftPanel(fmt.Sprintf("late-return-%d", i), i+1)
			p.View = blockingView(release)
			if _, err := InvokeView(context.Background(), p, timeout); !errors.Is(err, ErrViewTimedOut) {
				t.Errorf("InvokeView: err = %v, want ErrViewTimedOut", err)
			}
		}(i)
	}
	wg.Wait()

	close(release)

	after := settledGoroutineCount(t)
	// Small slack for the runtime's own bookkeeping goroutines: the
	// property under test is "n sender goroutines did not leak", not
	// "goroutine count is exactly the baseline".
	if want := baseline + 2; after > want {
		t.Fatalf("goroutines after release = %d, want <= %d (baseline %d): the sender goroutine leaked because the result channel is not buffered", after, want, baseline)
	}
}

// settledGoroutineCount polls runtime.NumGoroutine() until it stops
// falling or a short budget elapses, so a reading is not taken mid-GC or
// mid-scheduler churn left over from an earlier subtest.
func settledGoroutineCount(t *testing.T) int {
	t.Helper()
	last := runtime.NumGoroutine()
	for i := 0; i < 50; i++ {
		time.Sleep(10 * time.Millisecond)
		runtime.Gosched()
		n := runtime.NumGoroutine()
		if n >= last {
			return n
		}
		last = n
	}
	return last
}
