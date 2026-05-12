//go:build csip_test_hooks

// Build-tag-gated time-advance hook for the CSIP V1.2 conformance harness
// (CORE-006). Adds a test-controlled offset to the Time resource's clock
// without touching wall-clock time.
//
// Enabled only when the binary is built with `-tags csip_test_hooks`.
// The companion file time.go (no tag predicate) declares the nowFunc
// package var defaulted to time.Now; this file's init() swaps it to a
// closure that adds the atomic offset. With the tag off, this file is
// not compiled and nowFunc remains the unmodified time.Now reference —
// the production binary is bit-identical to one without IEEE-025 wired.
//
// The offset is stored as nanoseconds in an atomic.Int64 so the time
// mutation surface in internal/server/test_mutations.go and any read
// path through HandleTime can race-safely contend. IEEE-025.

package handler

import (
	"sync/atomic"
	"time"
)

// clockOffsetNanos is the wall-clock offset applied by nowFunc when the
// csip_test_hooks tag is set. Mutated only via AdvanceClock.
var clockOffsetNanos atomic.Int64

func init() {
	nowFunc = func() time.Time {
		off := clockOffsetNanos.Load()
		if off == 0 {
			return time.Now()
		}
		return time.Now().Add(time.Duration(off))
	}
}

// AdvanceClock shifts the test-only clock offset by d. Positive values
// move the reported time forward, negative values backward. The shift is
// additive (cumulative): calling AdvanceClock(1s) twice advances by 2s.
//
// Only callable from packages that compile under the csip_test_hooks tag.
// In production builds this symbol does not exist.
func AdvanceClock(d time.Duration) {
	clockOffsetNanos.Add(int64(d))
}

// ClockOffset returns the current cumulative test-only clock offset.
// Exposed for assertion helpers in tagged test code; not for production
// callers.
func ClockOffset() time.Duration {
	return time.Duration(clockOffsetNanos.Load())
}

// ResetClockOffset zeroes the test-only clock offset. Intended for test
// teardown between cases that share process state.
func ResetClockOffset() {
	clockOffsetNanos.Store(0)
}
