// Package interval owns the one computation of when a start-plus-duration
// interval ends, for packages that cannot import each other.
package interval

// End is the first second after an interval that begins at start and lasts
// duration seconds.
func End(start int64, duration uint32) int64 {
	return start + int64(duration)
}
