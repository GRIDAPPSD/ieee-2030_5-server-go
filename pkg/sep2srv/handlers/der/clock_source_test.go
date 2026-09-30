package der_test

import (
	"os"
	"regexp"
	"testing"
)

// timeNowCall matches a direct time.Now() call.
var timeNowCall = regexp.MustCompile(`\btime\.Now\(\)`)

// TestDerivedStatusControlStore_ReadsClockThroughSep2Time fails, in both
// build modes, if status_store.go calls time.Now() directly instead of
// sep2time.Now(). Under the default (untagged) nowFunc the two return the
// identical value, so no runtime assertion in this package can tell them
// apart there; the csip_test_hooks-gated test in clock_hooks_test.go proves
// the tagged build's offset actually reaches the derived EventStatus, but
// only this source check can catch the swap when the tag is off. Mirrors
// internal/dercontrol/clock_source_test.go (#563), the same finding
// applied to the serve side (#564 fix round 1).
func TestDerivedStatusControlStore_ReadsClockThroughSep2Time(t *testing.T) {
	src, err := os.ReadFile("status_store.go")
	if err != nil {
		t.Fatalf("read status_store.go: %v", err)
	}
	if timeNowCall.Match(src) {
		t.Fatalf("status_store.go calls time.Now() directly; read the clock through sep2time.Now so the csip_test_hooks offset is honored")
	}
}
