package handler

import (
	"testing"
	"time"
)

// A history that is open but holds nothing for the window must not answer in
// place of the live readings: every group keeps a covered point per bucket.
func TestPower_AggregatorEnergyEmptyHistoryDoesNotReplaceLive(t *testing.T) {
	p := newPowerHarness(t)
	twoAggregatorsAndOneDirect(t, p)
	hist := openHistory(t, t.TempDir(), 48*time.Hour, powerNow)

	got, _ := aggRoute(t, p.withHistory(hist), "scope=all&window=3600")
	total := 0
	for _, key := range []string{powerAgg, powerAgg2, "direct"} {
		covered := 0
		for _, pt := range got.entry(t, key).Discharged {
			if pt[1] != nil {
				covered++
			}
		}
		if covered != 720 {
			t.Errorf("%s covered points = %d, want 720 (3600 s in 5 s buckets)", key, covered)
		}
		total += covered
	}
	if total != 2160 {
		t.Errorf("covered group points = %d, want 2160", total)
	}
}
