// Package idmint is the one place the server mints the ids of resources it
// names itself (subscriptions, log events, text messages, and so on).
//
// An id is a nanosecond reading made strictly increasing within the process,
// so two requests that read the same clock value still get different ids.
// Callers format the number; the prefix and width they use stay theirs.
package idmint

import (
	"fmt"
	"sync"
	"time"
)

var (
	mu    sync.Mutex
	clock = time.Now
	last  int64 // highest number handed out so far
)

// SwapClock replaces the time source and returns a func that restores it.
// Tests use it to hold the clock still. It is not safe to call while other
// goroutines mint.
func SwapClock(f func() time.Time) (restore func()) {
	prev := clock
	clock = f
	return func() { clock = prev }
}

// Next returns one unique id number.
func Next() int64 { return Reserve(1) }

// Reserve returns the first of n consecutive unique id numbers, none of which
// any other call returns. A number is the clock reading, or one past the
// previous number when the clock has not moved on, so numbers also never
// repeat across a backwards clock step within the process.
func Reserve(n int) int64 {
	n = max(n, 1)
	mu.Lock()
	defer mu.Unlock()
	first := max(clock().UnixNano(), last+1)
	last = first + int64(n) - 1
	return first
}

// Prefixed returns "<prefix>-<number>".
func Prefixed(prefix string) string {
	return fmt.Sprintf("%s-%d", prefix, Next())
}

// Sortable returns the number as 20 zero-padded digits, so lexical order of
// ids minted by one process matches mint order.
func Sortable() string { return Format20(Next()) }

// Format20 formats n as 20 zero-padded digits.
func Format20(n int64) string { return fmt.Sprintf("%020d", n) }
