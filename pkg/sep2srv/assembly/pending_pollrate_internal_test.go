package assembly

import (
	"testing"
	"time"
)

// The pending pollRate is whole seconds, never 0: 2030.5 makes pollRate the
// polling interval, and 0 would tell a client to poll without pause.
func TestPendingPollRateSeconds(t *testing.T) {
	t.Parallel()
	cases := []struct {
		d    time.Duration
		want uint32
	}{
		{0, 30},
		{-5 * time.Second, 30},
		{500 * time.Millisecond, 1},
		{1500 * time.Millisecond, 1},
		{7 * time.Second, 7},
	}
	for _, tc := range cases {
		if got := pendingPollRateSeconds(tc.d); got != tc.want {
			t.Errorf("pendingPollRateSeconds(%v) = %d, want %d", tc.d, got, tc.want)
		}
	}
}
