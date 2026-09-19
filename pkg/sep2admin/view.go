package sep2admin

import (
	"context"
	"errors"
	"fmt"
	"runtime/debug"
	"time"
)

// Sentinel errors for the three View failure modes InvokeView bounds.
// Every one is meant to be checked with errors.Is, matching this package's
// existing sentinel style, never by comparing strings.
var (
	// ErrViewFailed wraps an error a Panel's View itself returned.
	ErrViewFailed = errors.New("sep2admin: panel View returned an error")

	// ErrViewPanicked is returned when a Panel's View panics. See
	// InvokeView's doc comment for what the recovery means for later
	// calls to the same Panel.
	ErrViewPanicked = errors.New("sep2admin: panel View panicked")

	// ErrViewTimedOut is returned when a Panel's View does not return
	// before InvokeView's deadline. The deadline is enforced by
	// InvokeView itself, never trusted to the View honouring ctx.
	ErrViewTimedOut = errors.New("sep2admin: panel View did not return before its deadline")
)

// InvokeView calls p.View and converts each way a View can take down its
// caller -- an error, a panic, or never returning before timeout elapses --
// into one of this package's own sentinels (ErrViewFailed, ErrViewPanicked,
// ErrViewTimedOut) instead of letting any of the three propagate.
//
// The panic is recovered inside the SAME goroutine that ran View: that is
// the only place a Go panic can be recovered before it kills the whole
// process, panics in other goroutines included. Recovery touches no state
// shared with any other call, so the same Panel is exactly as callable on
// the next InvokeView call as one that never panicked.
//
// timeout bounds InvokeView regardless of what View does with ctx: a View
// that never checks ctx.Done() is still bounded, because the bound comes
// from InvokeView's own select, not from View's cooperation. InvokeView
// stops WAITING at the deadline; it does not and cannot kill the goroutine
// still running View, since Go has no forced preemption of a goroutine. A
// View that never returns leaks that goroutine for as long as it keeps
// running.
func InvokeView(ctx context.Context, p Panel, timeout time.Duration) (Descriptor, error) {
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
		return Descriptor{}, fmt.Errorf("%w: %w", ErrViewTimedOut, ctx.Err())
	}
}
