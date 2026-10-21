package idmint_test

import (
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/idmint"
)

// Not parallel: it swaps the process-wide clock.
func TestNumbersAreUniqueAndIncreasingUnderAHeldClock(t *testing.T) {
	t.Cleanup(idmint.SwapClock(func() time.Time { return time.Unix(1700000000, 42) }))

	first := idmint.Next()
	// At least the clock reading: an earlier number from a real clock in this
	// process (another test, or -count above 1) may already be higher.
	if want := int64(1700000000)*1e9 + 42; first < want {
		t.Errorf("first number = %d, want at least the clock reading %d", first, want)
	}
	if second := idmint.Next(); second != first+1 {
		t.Errorf("second number = %d, want %d", second, first+1)
	}
}

func TestReserveHandsOutAWholeRun(t *testing.T) {
	t.Cleanup(idmint.SwapClock(func() time.Time { return time.Unix(1700000000, 0) }))

	base := idmint.Reserve(5)
	if next := idmint.Next(); next != base+5 {
		t.Errorf("number after Reserve(5) = %d, want %d: the run must be exclusive", next, base+5)
	}
}

func TestBackwardsClockStepDoesNotRepeat(t *testing.T) {
	now := time.Unix(1700000000, 0)
	t.Cleanup(idmint.SwapClock(func() time.Time { return now }))

	a := idmint.Next()
	now = now.Add(-time.Hour)
	if b := idmint.Next(); b <= a {
		t.Errorf("number after a backwards step = %d, want more than %d", b, a)
	}
}

func TestFormats(t *testing.T) {
	t.Cleanup(idmint.SwapClock(func() time.Time { return time.Unix(0, 7) }))

	if got := idmint.Prefixed("sub"); !strings.HasPrefix(got, "sub-") {
		t.Errorf("Prefixed = %q, want the sub- prefix", got)
	}
	if got := idmint.Format20(12); got != "00000000000000000012" {
		t.Errorf("Format20(12) = %q", got)
	}
	a, b := idmint.Sortable(), idmint.Sortable()
	if len(a) != 20 || a >= b {
		t.Errorf("Sortable gave %q then %q, want 20 digits in increasing order", a, b)
	}
}

func TestConcurrentNumbersAreDistinct(t *testing.T) {
	const n = 200
	got := make([]int64, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() { defer wg.Done(); got[i] = idmint.Next() }()
	}
	wg.Wait()
	seen := map[int64]bool{}
	for _, v := range got {
		if seen[v] {
			t.Fatalf("number %d handed out twice", v)
		}
		seen[v] = true
	}
}

// Run with -race: swapping the clock while another goroutine mints must not
// be a data race.
func TestSwapClockWhileMinting(t *testing.T) {
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-stop:
				return
			default:
				idmint.Next()
			}
		}
	}()
	for range 5000 {
		restore := idmint.SwapClock(time.Now)
		restore()
	}
	close(stop)
	<-done
}
