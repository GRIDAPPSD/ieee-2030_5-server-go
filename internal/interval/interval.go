// Package interval owns the computations on a start-plus-duration interval,
// when it ends and whether two overlap, for packages that cannot import
// each other.
package interval

// End is the first second after an interval that begins at start and lasts
// duration seconds.
func End(start int64, duration uint32) int64 {
	return start + int64(duration)
}

// Overlaps reports whether the half-open intervals [aStart, aEnd) and
// [bStart, bEnd) share a second. Touching is not overlap, and an empty
// interval covers no second, so it overlaps nothing.
func Overlaps(aStart, aEnd, bStart, bEnd int64) bool {
	if aStart >= aEnd || bStart >= bEnd {
		return false
	}
	return aStart < bEnd && bStart < aEnd
}
