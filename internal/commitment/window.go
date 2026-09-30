// Package commitment holds the one rule that #714 exists to enforce: a
// fleet window carries at most one live grant or one unlinked control, and
// an execution carrying out a grant stays inside that grant's bounds. The
// package is pure: it reads no store and holds no state of its own, so
// every value it works with is handed in by a caller.
package commitment

// Window is the half-open interval [Start, Start+Duration). Duration zero
// covers no instant, so it overlaps nothing and is trivially within any
// other window.
type Window struct {
	Start    int64
	Duration uint32
}

// End returns the first second not covered by w.
func (w Window) End() int64 {
	return w.Start + int64(w.Duration)
}

// Overlaps reports whether w and o share a covered instant. Touching at an
// instant is not overlap (the classic half-open test), and a window of
// duration zero is guarded explicitly: without the guard, the half-open
// test alone would read a zero-duration point as inside a wider window it
// never covers.
func (w Window) Overlaps(o Window) bool {
	if w.Duration == 0 || o.Duration == 0 {
		return false
	}
	return w.Start < o.End() && o.Start < w.End()
}

// Within reports whether every instant w covers is also covered by o.
// Duration zero is guarded explicitly, the same choice Overlaps makes: a
// zero-duration w covers no instant, so it is vacuously within any o,
// wherever it sits. Without the guard the plain range check below reads a
// zero-duration point as within o only when its Start falls inside o's
// closed range, which is inconsistent with Overlaps already treating a
// zero-duration window as asserting nothing.
func (w Window) Within(o Window) bool {
	if w.Duration == 0 {
		return true
	}
	return w.Start >= o.Start && w.End() <= o.End()
}
