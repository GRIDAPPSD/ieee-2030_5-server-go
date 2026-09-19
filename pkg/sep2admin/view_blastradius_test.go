package sep2admin

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// TestInvokeViewBlastRadiusIsOnePanel is the proof for criterion 7's
// isolation property (#368): several panels are invoked concurrently, one
// hostile in each of the three failure modes, and every well-behaved panel
// must still produce its own result. A single hostile panel cannot show
// this; the test needs the others alongside it.
func TestInvokeViewBlastRadiusIsOnePanel(t *testing.T) {
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })

	var mu sync.Mutex
	calls := map[string]int{}
	goodView := func(id string) ViewFunc {
		return func(_ context.Context) (Descriptor, error) {
			mu.Lock()
			calls[id]++
			mu.Unlock()
			return Descriptor{Version: CurrentDescriptorVersion}, nil
		}
	}

	good := []string{"good-a", "good-b", "good-c"}
	panels := map[string]Panel{}
	for i, id := range good {
		p := graftPanel(id, i+1)
		p.View = goodView(id)
		panels[id] = p
	}

	hostileErr := graftPanel("hostile-error", 10)
	hostileErr.View = func(_ context.Context) (Descriptor, error) { return Descriptor{}, errors.New("boom") }
	panels["hostile-error"] = hostileErr

	hostilePanic := graftPanel("hostile-panic", 11)
	hostilePanic.View = func(_ context.Context) (Descriptor, error) { panic("boom") }
	panels["hostile-panic"] = hostilePanic

	hostileTimeout := graftPanel("hostile-timeout", 12)
	hostileTimeout.View = blockingView(release)
	panels["hostile-timeout"] = hostileTimeout

	// Only hostile-timeout needs a short deadline; it is the one panel
	// that must actually hit it. The good panels and the error/panic
	// panels do no waiting, so giving them the same 20ms bound ties their
	// pass/fail to the scheduler rather than to the isolation property
	// under test: a loaded runner would fail this test even though
	// nothing about isolation had broken.
	const hostileTimeoutDeadline = 20 * time.Millisecond
	const generousDeadline = time.Second

	type outcome struct {
		d   Descriptor
		err error
	}
	results := make(map[string]outcome, len(panels))
	var resMu sync.Mutex
	var wg sync.WaitGroup
	for id, p := range panels {
		wg.Add(1)
		timeout := generousDeadline
		if id == "hostile-timeout" {
			timeout = hostileTimeoutDeadline
		}
		go func(id string, p Panel, timeout time.Duration) {
			defer wg.Done()
			d, err := InvokeView(context.Background(), p, timeout)
			resMu.Lock()
			results[id] = outcome{d, err}
			resMu.Unlock()
		}(id, p, timeout)
	}
	wg.Wait()

	for _, id := range good {
		r := results[id]
		if r.err != nil {
			t.Fatalf("%s: err = %v, want nil: a hostile sibling panel must not affect this one", id, r.err)
		}
		if r.d.Version != CurrentDescriptorVersion {
			t.Fatalf("%s: Descriptor.Version = %d, want %d", id, r.d.Version, CurrentDescriptorVersion)
		}
		mu.Lock()
		n := calls[id]
		mu.Unlock()
		if n != 1 {
			t.Fatalf("%s: View invoked %d times, want 1", id, n)
		}
	}

	if err := results["hostile-error"].err; !errors.Is(err, ErrViewFailed) {
		t.Fatalf("hostile-error: err = %v, want ErrViewFailed", err)
	}
	if err := results["hostile-panic"].err; !errors.Is(err, ErrViewPanicked) {
		t.Fatalf("hostile-panic: err = %v, want ErrViewPanicked", err)
	}
	if err := results["hostile-timeout"].err; !errors.Is(err, ErrViewTimedOut) {
		t.Fatalf("hostile-timeout: err = %v, want ErrViewTimedOut", err)
	}
}
