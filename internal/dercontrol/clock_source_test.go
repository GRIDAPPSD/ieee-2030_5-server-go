package dercontrol

import (
	"os"
	"regexp"
	"testing"
)

// timeNowCall matches a direct time.Now() call.
var timeNowCall = regexp.MustCompile(`\btime\.Now\(\)`)

// TestIssuer_ReadsClockThroughSep2Time fails, in both build modes, if
// issuer.go calls time.Now() directly instead of sep2time.Now(). Under the
// default (untagged) nowFunc the two return the identical value, so no
// runtime assertion in this package can tell them apart there; the
// csip_test_hooks-gated test in clock_hooks_test.go proves the tagged
// build's offset actually reaches Issue and Cancel, but only this
// source check can catch the swap when the tag is off (#563).
func TestIssuer_ReadsClockThroughSep2Time(t *testing.T) {
	src, err := os.ReadFile("issuer.go")
	if err != nil {
		t.Fatalf("read issuer.go: %v", err)
	}
	if timeNowCall.Match(src) {
		t.Fatalf("issuer.go calls time.Now() directly; read the clock through sep2time.Now so the csip_test_hooks offset is honored")
	}
}
