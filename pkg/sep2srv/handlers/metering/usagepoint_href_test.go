package metering_test

import (
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/handlers/metering"
)

// UsagePointIDFromHref inverts UsagePointHref and refuses every other shape.
func TestUsagePointIDFromHref(t *testing.T) {
	t.Parallel()
	for _, id := range []string{"1", "abc-9"} {
		got, ok := metering.UsagePointIDFromHref(metering.UsagePointHref(id))
		if !ok || got != id {
			t.Errorf("UsagePointIDFromHref(UsagePointHref(%q)) = %q, %v; want the id back", id, got, ok)
		}
	}
	for _, href := range []string{"", "/upt/", "/upt/a/b", "/mup/1", "upt/1", "/edev/1/upt/1"} {
		if got, ok := metering.UsagePointIDFromHref(href); ok || got != "" {
			t.Errorf("UsagePointIDFromHref(%q) = %q, %v; want no id", href, got, ok)
		}
	}
}
