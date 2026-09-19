package sep2admin

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"
)

// blockingView returns a ViewFunc that ignores ctx entirely and blocks
// until release is closed, then returns a valid Descriptor. It is the
// hostile-timeout panel used across this package's InvokeView tests (#368),
// deliberately not honouring cancellation.
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

// TestInvokeViewDistinguishesFailureModes is the proof that the three
// failure modes criterion 7 names must be told apart by the caller, not
// folded into one generic failure (#368). Each case is checked against
// every OTHER package sentinel via viewSentinels, not a copied subset: the
// round-2 review found aliasing ErrViewCanceled to ErrViewFailed, or
// ErrInvalidTimeout to ErrViewTimedOut, passed the whole suite when this
// list named only three of five.
func TestInvokeViewDistinguishesFailureModes(t *testing.T) {
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })

	viewErr := errors.New("boom")
	all := viewSentinels

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
			// A hostile timeout panel that never even looks at ctx,
			// reused here to prove the three modes stay distinguishable
			// from each other too.
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

// TestInvokeViewRecoveredPanicDoesNotAffectSubsequentInvocations proves
// InvokeView's doc comment claim that a recovered panic leaves no state
// behind (#368): the identical Panel is invoked twice here, panicking only
// on the first call, and the second call must succeed normally.
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

// TestInvokeViewDiscardsDescriptorOnFailurePaths pins the two surviving
// mutants the silent-failure review reported: today's behaviour on both
// failure paths is a zero Descriptor, and nothing asserted it, so carrying
// the View's partial result through on error, or a filled Descriptor
// through on timeout, passed the whole suite.
func TestInvokeViewDiscardsDescriptorOnFailurePaths(t *testing.T) {
	t.Run("error path", func(t *testing.T) {
		p := graftPanel("partial-descriptor-on-error", 1)
		p.View = func(_ context.Context) (Descriptor, error) {
			return Descriptor{Version: CurrentDescriptorVersion}, errors.New("boom")
		}

		d, err := InvokeView(context.Background(), p, time.Second)
		if !errors.Is(err, ErrViewFailed) {
			t.Fatalf("InvokeView: err = %v, want ErrViewFailed", err)
		}
		if !reflect.DeepEqual(d, Descriptor{}) {
			t.Fatalf("InvokeView: Descriptor = %+v, want the zero value: a caller must not see the View's partial result on error", d)
		}
	})

	t.Run("timeout path", func(t *testing.T) {
		release := make(chan struct{})
		t.Cleanup(func() { close(release) })

		p := graftPanel("descriptor-on-timeout", 1)
		p.View = blockingView(release)

		d, err := InvokeView(context.Background(), p, 20*time.Millisecond)
		if !errors.Is(err, ErrViewTimedOut) {
			t.Fatalf("InvokeView: err = %v, want ErrViewTimedOut", err)
		}
		if !reflect.DeepEqual(d, Descriptor{}) {
			t.Fatalf("InvokeView: Descriptor = %+v, want the zero value on the timeout path", d)
		}
	})
}

// TestViewSentinelsAreAllDistinct extends TestInvokeViewDistinguishesFailureModes
// to every pair in viewSentinels, including ErrViewCanceled, ErrViewNotInvoked
// and ErrInvalidTimeout, which no InvokeView call in this file returns as a
// table-test "want" and so are never otherwise compared against each other.
// A sentinel declared later through viewSentinel is covered the moment it
// is declared, with no list here to remember to extend.
func TestViewSentinelsAreAllDistinct(t *testing.T) {
	const wantAtLeast = 6 // Failed, Panicked, TimedOut, Canceled, NotInvoked, InvalidTimeout.
	if len(viewSentinels) < wantAtLeast {
		t.Fatalf("viewSentinels has %d entries, want at least %d: every package-level sentinel in view.go must be declared through viewSentinel to be covered here", len(viewSentinels), wantAtLeast)
	}
	for i, a := range viewSentinels {
		for j, b := range viewSentinels {
			if i == j {
				continue
			}
			if errors.Is(a, b) {
				t.Fatalf("viewSentinels[%d] (%v) matches viewSentinels[%d] (%v), want every InvokeView sentinel distinguishable from every other", i, a, j, b)
			}
		}
	}
}
