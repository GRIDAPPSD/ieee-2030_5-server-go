package sep2admin

import (
	"context"
	"errors"
	"fmt"
	"runtime/debug"
	"time"
)

// viewSentinels collects every sentinel below, in declaration order.
// TestInvokeViewDistinguishesFailureModes and
// TestViewSentinelsAreAllDistinct iterate it instead of a hand-copied
// list, so a sentinel declared later is covered the moment it is
// declared.
var viewSentinels []error

// viewSentinel creates a sentinel error and appends it to viewSentinels.
// Declaring a new InvokeView sentinel through this function, rather than a
// bare errors.New, is what keeps viewSentinels complete without anyone
// having to remember to extend a separate list: the round-2 review found
// that aliasing one sentinel to another passed the whole suite because the
// old list named only three of five.
func viewSentinel(msg string) error {
	err := errors.New(msg)
	viewSentinels = append(viewSentinels, err)
	return err
}

// Sentinel errors for the failure modes InvokeView bounds. Every one is
// meant to be checked with errors.Is, matching this package's existing
// sentinel style, never by comparing strings.
var (
	// ErrViewFailed wraps an error a Panel's View itself returned.
	ErrViewFailed = viewSentinel("sep2admin: panel View returned an error")

	// ErrViewPanicked is returned when a Panel's View panics. See
	// InvokeView's doc comment for what the recovery means for later
	// calls to the same Panel, for what it cannot reach, and for the
	// reporting seam neither gap has today.
	//
	// The error text embeds a full stack (runtime/debug.Stack()),
	// including absolute host filesystem paths. Nothing in this package
	// logs or serves it: whatever eventually surfaces InvokeView's error
	// to an HTTP client must not return this text verbatim, and a caller
	// that logs only errors.Is(err, ErrViewPanicked) loses the stack
	// entirely. This package does not choose between those for its
	// caller.
	ErrViewPanicked = viewSentinel("sep2admin: panel View panicked")

	// ErrViewTimedOut is returned when ctx.Done() fires because a
	// deadline elapsed while View was running: InvokeView's own bound
	// from timeout, or an ancestor context's deadline. It is never
	// returned for an ancestor that was explicitly cancelled (see
	// ErrViewCanceled) or for a deadline that had already elapsed before
	// View was ever invoked (see ErrViewNotInvoked).
	ErrViewTimedOut = viewSentinel("sep2admin: panel View did not return before its deadline")

	// ErrViewCanceled is returned when ctx.Done() fires because the
	// caller (or an ancestor context) was cancelled, not because a
	// deadline elapsed. A client disconnect propagating through ctx must
	// not be reported as ErrViewTimedOut: that sentinel means the
	// deadline itself elapsed, and conflating the two would blame the
	// panel for a request nobody is waiting on any more.
	ErrViewCanceled = viewSentinel("sep2admin: caller's context was cancelled before View returned")

	// ErrViewNotInvoked is returned when InvokeView refuses to call View
	// at all because an ancestor's deadline had already elapsed before
	// the call began. It is distinct from ErrViewTimedOut: that sentinel
	// means a running View missed its deadline, which cannot be true of
	// a View that was never invoked.
	ErrViewNotInvoked = viewSentinel("sep2admin: caller's context deadline had already elapsed; View was never invoked")

	// ErrInvalidTimeout is returned when timeout is not positive.
	// InvokeView refuses it outright rather than passing it to
	// context.WithTimeout: an unvalidated non-positive timeout still
	// invokes View and still costs a full call and a goroutine, for a
	// config field that was never set.
	ErrInvalidTimeout = viewSentinel("sep2admin: InvokeView timeout must be positive")
)

// InvokeView calls p.View and converts each way a View can take down its
// caller (an error, a panic, or never returning before timeout elapses)
// into one of this package's own sentinels (ErrViewFailed, ErrViewPanicked,
// ErrViewTimedOut, ErrViewCanceled) instead of letting any of the three
// propagate.
//
// The panic is recovered inside the SAME goroutine that ran View, which is
// the only place a Go panic can be recovered before it kills the whole
// process. That containment reaches only a panic raised on THAT goroutine:
// a View that starts a goroutine of its own and panics there is outside
// it, and takes the whole process down like any other unrecovered panic.
// A panic recovered here after InvokeView has already returned via the
// timeout branch is also outside what a caller ever sees: it is formatted
// and sent into done, and nothing reads done again. Neither gap has a
// reporting seam in this package today; see #607. Recovery itself touches
// no state shared with any other call, so the same Panel is exactly as
// callable on the next InvokeView call as one that never panicked: that is
// a property of InvokeView's own machinery, not a claim about whether a
// real View's internal state is safe to reuse after a panic mid-mutation.
//
// timeout bounds InvokeView regardless of what View does with ctx: a View
// that never checks ctx.Done() is still bounded, because the bound comes
// from InvokeView's own select, not from View's cooperation. InvokeView
// stops WAITING at the deadline; it does not and cannot kill the goroutine
// still running View, since Go has no forced preemption of a goroutine. A
// View that never returns leaks that goroutine for as long as it keeps
// running.
//
// timeout must be positive: InvokeView refuses it with ErrInvalidTimeout
// rather than invoking View for a deadline that has already elapsed.
//
// ctx.Done() firing while View is running is reported as ErrViewTimedOut
// when a deadline actually elapsed (InvokeView's own bound or an
// ancestor's), and as ErrViewCanceled when ctx, or an ancestor, was
// explicitly cancelled instead (an HTTP request context on client
// disconnect, for example). InvokeView never invokes View at all if ctx
// was already done on entry: a caller that is already gone gets that told
// back without paying for a View call, as ErrViewCanceled for
// cancellation and as ErrViewNotInvoked, not ErrViewTimedOut, when an
// ancestor's deadline had already elapsed before the call began, since no
// View ever ran to have missed it.
func InvokeView(ctx context.Context, p Panel, timeout time.Duration) (Descriptor, error) {
	if timeout <= 0 {
		return Descriptor{}, ErrInvalidTimeout
	}
	if err := ctx.Err(); err != nil {
		return Descriptor{}, entryRefusalErr(err)
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	type result struct {
		d   Descriptor
		err error
	}
	// Buffered so the goroutine below never blocks on a send after
	// InvokeView has already returned via the ctx.Done() case: without
	// the buffer, a View that outlives its deadline would also leak its
	// result-sending goroutine, not just the View call itself.
	done := make(chan result, 1)

	go func() {
		defer func() {
			if r := recover(); r != nil {
				done <- result{err: fmt.Errorf("%w: %v\n%s", ErrViewPanicked, r, debug.Stack())}
			}
		}()
		d, err := p.View(ctx)
		if err != nil {
			done <- result{err: fmt.Errorf("%w: %w", ErrViewFailed, err)}
			return
		}
		done <- result{d: d}
	}()

	select {
	case res := <-done:
		return res.d, res.err
	case <-ctx.Done():
		return Descriptor{}, doneErr(ctx.Err())
	}
}

// doneErr names ctx.Err()'s cause for the select in InvokeView: ErrViewTimedOut
// when a deadline elapsed, ErrViewCanceled when ctx was cancelled instead.
//
// That select races this branch against a View result landing at nearly
// the same instant: about 6 of 200 runs report the context's outcome here
// even though View had already returned. No Descriptor is lost either way,
// only the reported cause flips; making the race deterministic is not
// resolved here, see #607.
func doneErr(err error) error {
	if errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("%w: %w", ErrViewTimedOut, err)
	}
	return fmt.Errorf("%w: %w", ErrViewCanceled, err)
}

// entryRefusalErr reports why InvokeView refuses to invoke View at all
// because ctx was already done before the call began: ErrViewCanceled for
// cancellation, and ErrViewNotInvoked, never ErrViewTimedOut, when an
// ancestor's deadline had already elapsed. ErrViewTimedOut means a running
// View missed its deadline, which is not what happened here: View was
// never called.
func entryRefusalErr(err error) error {
	if errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("%w: %w", ErrViewNotInvoked, err)
	}
	return fmt.Errorf("%w: %w", ErrViewCanceled, err)
}
