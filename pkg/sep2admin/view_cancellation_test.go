package sep2admin

import (
	"context"
	"errors"
	"testing"
	"time"
)

// TestInvokeViewBoundsAViewThatIgnoresCancellation is the proof that
// asserting that a well-behaved View honours cancellation would prove
// nothing about enforcement, so this View never inspects ctx at all and
// only returns once the test releases it well after the deadline (#368).
// InvokeView must still return by its own timeout.
func TestInvokeViewBoundsAViewThatIgnoresCancellation(t *testing.T) {
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })

	p := graftPanel("ignores-cancellation", 1)
	p.View = blockingView(release)

	const timeout = 20 * time.Millisecond
	start := time.Now()
	_, err := InvokeView(context.Background(), p, timeout)
	elapsed := time.Since(start)

	if !errors.Is(err, ErrViewTimedOut) {
		t.Fatalf("InvokeView: err = %v, want ErrViewTimedOut", err)
	}
	// A generous multiple of the deadline, not the deadline itself: the
	// property under test is "bounded by the server", not "returns in
	// exactly timeout", and a tight bound would make this test flaky
	// under scheduler load.
	if max := 10 * timeout; elapsed > max {
		t.Fatalf("InvokeView took %v to return after a %v deadline against a View that never checks ctx; want under %v: the server did not bound it", elapsed, timeout, max)
	}
}

// TestInvokeViewDistinguishesCallerCancellationFromElapsedDeadline is the
// probe from the idiom review, reproduced as a permanent test: a caller's
// own context cancelled well before InvokeView's own (much longer) timeout
// elapses must not be reported as ErrViewTimedOut. That sentinel means the
// deadline itself elapsed; a cancelled caller is a different outcome.
func TestInvokeViewDistinguishesCallerCancellationFromElapsedDeadline(t *testing.T) {
	parent, cancel := context.WithCancel(context.Background())
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	time.AfterFunc(10*time.Millisecond, cancel)

	p := graftPanel("caller-cancelled", 1)
	p.View = blockingView(release)

	_, err := InvokeView(parent, p, time.Hour)
	if !errors.Is(err, ErrViewCanceled) {
		t.Fatalf("InvokeView: err = %v, want ErrViewCanceled", err)
	}
	if errors.Is(err, ErrViewTimedOut) {
		t.Fatalf("InvokeView: err = %v also matches ErrViewTimedOut, want a cancelled caller distinguishable from an elapsed deadline", err)
	}
}

// TestInvokeViewDoesNotInvokeViewWhenContextIsAlreadyDone is the other half
// of the same finding: a caller whose context is already cancelled on
// entry gets that told back without InvokeView running the View at all, so
// no backend work happens for a request nobody is waiting on.
func TestInvokeViewDoesNotInvokeViewWhenContextIsAlreadyDone(t *testing.T) {
	parent, cancel := context.WithCancel(context.Background())
	cancel()

	p := graftPanel("already-done-on-entry", 1)
	viewCalled := make(chan struct{}, 1)
	p.View = func(_ context.Context) (Descriptor, error) {
		viewCalled <- struct{}{}
		return Descriptor{Version: CurrentDescriptorVersion}, nil
	}

	_, err := InvokeView(parent, p, time.Hour)
	if !errors.Is(err, ErrViewCanceled) {
		t.Fatalf("InvokeView: err = %v, want ErrViewCanceled", err)
	}
	select {
	case <-viewCalled:
		t.Fatal("View was invoked on a context already done on entry, want it never invoked")
	case <-time.After(50 * time.Millisecond):
	}
}

// TestInvokeViewDoesNotInvokeViewWhenAnAncestorDeadlineHasAlreadyElapsed is
// the deadline half of the same finding (round-2 review N1): a context
// whose deadline had already elapsed before the call began must also never
// reach View, and must be reported distinguishably from a View that
// actually ran and missed its deadline. Narrowing the entry check in
// InvokeView to fire only on errors.Is(err, context.Canceled) survived the
// whole suite before this test existed, because nothing here checked the
// deadline-already-elapsed half of ctx.Err() on entry.
func TestInvokeViewDoesNotInvokeViewWhenAnAncestorDeadlineHasAlreadyElapsed(t *testing.T) {
	parent, cancel := context.WithTimeout(context.Background(), -time.Second)
	defer cancel()

	p := graftPanel("deadline-already-elapsed-on-entry", 1)
	viewCalled := make(chan struct{}, 1)
	p.View = func(_ context.Context) (Descriptor, error) {
		viewCalled <- struct{}{}
		return Descriptor{Version: CurrentDescriptorVersion}, nil
	}

	_, err := InvokeView(parent, p, time.Hour)
	if !errors.Is(err, ErrViewNotInvoked) {
		t.Fatalf("InvokeView: err = %v, want ErrViewNotInvoked", err)
	}
	if errors.Is(err, ErrViewTimedOut) {
		t.Fatalf("InvokeView: err = %v also matches ErrViewTimedOut, want an already-elapsed ancestor deadline distinguishable from a View that actually ran out of time", err)
	}
	select {
	case <-viewCalled:
		t.Fatal("View was invoked for a context whose deadline had already elapsed on entry, want it never invoked")
	case <-time.After(50 * time.Millisecond):
	}
}
