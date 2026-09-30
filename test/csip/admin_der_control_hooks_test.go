//go:build csip_test_hooks

package csip_test

import (
	"testing"
	"time"

	coresep2time "github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/handlers/sep2time"
)

// Under csip_test_hooks the admin-created control procedure also advances
// the server clock past the control's start and reads its status again.
func init() {
	adminDERControlAdvance = func(t *testing.T, d time.Duration) {
		t.Helper()
		coresep2time.ResetClockOffset()
		t.Cleanup(coresep2time.ResetClockOffset)
		coresep2time.AdvanceClock(d)
	}
}
