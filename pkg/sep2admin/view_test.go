package sep2admin

import (
	"context"
	"errors"
	"testing"
	"time"
)

// blockingView returns a ViewFunc that ignores ctx entirely and blocks
// until release is closed, then returns a valid Descriptor. It is the
// hostile-timeout panel used across this package's InvokeView tests: item 2
// requires a View that deliberately does not honour cancellation.
func blockingView(release <-chan struct{}) ViewFunc {
	return func(_ context.Context) (Descriptor, error) {
		<-release
		return Descriptor{Version: CurrentDescriptorVersion}, nil
	}
}

func TestInvokeViewReturnsTheDescriptorFromAWellBehavedView(t *testing.T) {
	p := graftPanel("well-behaved", 1)

	d, err := InvokeView(context.Background(), p, time.Second)
	if err != nil {
		t.Fatalf("InvokeView: err = %v, want nil", err)
	}
	if d.Version != CurrentDescriptorVersion {
		t.Fatalf("InvokeView: Descriptor.Version = %d, want %d", d.Version, CurrentDescriptorVersion)
	}
}

// TestInvokeViewDistinguishesFailureModes is the proof for item 4: the
// three failure modes criterion 7 names must be told apart by the caller,
// not folded into one generic failure.
func TestInvokeViewDistinguishesFailureModes(t *testing.T) {
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })

	viewErr := errors.New("boom")
	all := []error{ErrViewFailed, ErrViewPanicked, ErrViewTimedOut}

	cases := []struct {
		name    string
		view    ViewFunc
		timeout time.Duration
		want    error
	}{
		{
			name:    "error",
			view:    func(_ context.Context) (Descriptor, error) { return Descriptor{}, viewErr },
			timeout: time.Second,
			want:    ErrViewFailed,
		},
		{
			name:    "panic",
			view:    func(_ context.Context) (Descriptor, error) { panic("boom") },
			timeout: time.Second,
			want:    ErrViewPanicked,
		},
		{
			// A hostile timeout panel that never even looks at ctx: this
			// is item 2's own requirement (a View that ignores
			// cancellation), reused here to prove the three modes stay
			// distinguishable from each other too.
			name:    "timeout",
			view:    blockingView(release),
			timeout: 20 * time.Millisecond,
			want:    ErrViewTimedOut,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := graftPanel("hostile-"+tc.name, 1)
			p.View = tc.view

			_, err := InvokeView(context.Background(), p, tc.timeout)
			if !errors.Is(err, tc.want) {
				t.Fatalf("InvokeView: err = %v, want errors.Is match for %v", err, tc.want)
			}
			for _, other := range all {
				if other == tc.want {
					continue
				}
				if errors.Is(err, other) {
					t.Fatalf("InvokeView: err = %v also matches %v, want the three failure modes distinguishable", err, other)
				}
			}
		})
	}
}

// TestInvokeViewRecoveredPanicDoesNotAffectSubsequentInvocations is the
// proof for item 5: InvokeView's doc comment says a recovered panic leaves
// no state behind, so the identical Panel is invoked twice here, panicking
// only on the first call, and the second call must succeed normally.
func TestInvokeViewRecoveredPanicDoesNotAffectSubsequentInvocations(t *testing.T) {
	var calls int
	p := graftPanel("flaky", 1)
	p.View = func(_ context.Context) (Descriptor, error) {
		calls++
		if calls == 1 {
			panic("boom")
		}
		return Descriptor{Version: CurrentDescriptorVersion}, nil
	}

	_, err := InvokeView(context.Background(), p, time.Second)
	if !errors.Is(err, ErrViewPanicked) {
		t.Fatalf("first call: err = %v, want ErrViewPanicked", err)
	}

	d, err := InvokeView(context.Background(), p, time.Second)
	if err != nil {
		t.Fatalf("second call after a recovered panic: err = %v, want nil: the recovery must not leave the panel in a doubtful state", err)
	}
	if d.Version != CurrentDescriptorVersion {
		t.Fatalf("second call: Descriptor.Version = %d, want %d", d.Version, CurrentDescriptorVersion)
	}
	if calls != 2 {
		t.Fatalf("View invoked %d times, want 2", calls)
	}
}

// TestInvokeViewRejectsNonPositiveTimeout pins the probe from the
// silent-failure review: timeout <= 0 must be refused before View is ever
// invoked, not passed through to context.WithTimeout to fire immediately.
func TestInvokeViewRejectsNonPositiveTimeout(t *testing.T) {
	for _, timeout := range []time.Duration{0, -time.Second} {
		t.Run(timeout.String(), func(t *testing.T) {
			p := graftPanel("non-positive-timeout", 1)
			viewCalled := make(chan struct{}, 1)
			p.View = func(_ context.Context) (Descriptor, error) {
				viewCalled <- struct{}{}
				return Descriptor{Version: CurrentDescriptorVersion}, nil
			}

			_, err := InvokeView(context.Background(), p, timeout)
			if !errors.Is(err, ErrInvalidTimeout) {
				t.Fatalf("InvokeView(timeout=%v): err = %v, want ErrInvalidTimeout", timeout, err)
			}
			select {
			case <-viewCalled:
				t.Fatalf("InvokeView(timeout=%v): View was invoked, want it never invoked for an invalid timeout", timeout)
			case <-time.After(50 * time.Millisecond):
			}
		})
	}
}
