package sep2admin

import (
	"context"
	"errors"
	"testing"
	"time"
)

// TestInvokeViewBoundsAViewThatIgnoresCancellation is the proof for item 2
// (criterion 8): asserting that a well-behaved View honours cancellation
// would prove nothing about enforcement, so this View never inspects ctx at
// all and only returns once the test releases it well after the deadline.
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
