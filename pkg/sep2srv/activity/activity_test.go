package activity_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/activity"
)

const threshold = 5 * time.Minute

func TestStateAgainstThreshold(t *testing.T) {
	t.Parallel()
	base := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	cur := base
	r := activity.NewWithClock(func() time.Time { return cur })
	r.Record("lfdi-a")

	cases := []struct {
		name string
		now  time.Time
		want activity.Comms
	}{
		{"just recorded is online", base, activity.Online},
		{"threshold minus 1s is online", base.Add(threshold - time.Second), activity.Online},
		{"at threshold is offline", base.Add(threshold), activity.Offline},
		{"past threshold is offline", base.Add(threshold + time.Hour), activity.Offline},
	}
	for _, tc := range cases {
		if got := r.State("lfdi-a", tc.now, threshold); got != tc.want {
			t.Errorf("%s: State = %q, want %q", tc.name, got, tc.want)
		}
	}
	if got := r.State("lfdi-never", base, threshold); got != activity.NotSeen {
		t.Errorf("unrecorded device: State = %q, want %q", got, activity.NotSeen)
	}
}

func TestNilRecorderIsUnknown(t *testing.T) {
	t.Parallel()
	var r *activity.Recorder
	r.Record("lfdi-a")
	if got := r.State("lfdi-a", time.Now(), threshold); got != activity.Unknown {
		t.Errorf("nil recorder State = %q, want %q", got, activity.Unknown)
	}
	if _, _, ok := r.Last("lfdi-a"); ok {
		t.Error("nil recorder Last ok = true, want false")
	}
}

func TestLastHoldsTimeAndCount(t *testing.T) {
	t.Parallel()
	t0 := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	cur := t0
	r := activity.NewWithClock(func() time.Time { return cur })
	r.Record("lfdi-a")
	cur = t0.Add(7 * time.Second)
	r.Record("lfdi-a")
	r.Record("lfdi-b")

	at, n, ok := r.Last("lfdi-a")
	if !ok || !at.Equal(t0.Add(7*time.Second)) || n != 2 {
		t.Errorf("Last(a) = %v, %d, %v; want %v, 2, true", at, n, ok, t0.Add(7*time.Second))
	}
	if _, n, _ := r.Last("lfdi-b"); n != 1 {
		t.Errorf("Last(b) count = %d, want 1", n)
	}
}

func TestMiddlewareRecordsByIdentity(t *testing.T) {
	t.Parallel()
	r := activity.New()
	identity := func(ctx context.Context) (string, string, bool) {
		v, ok := ctx.Value(ctxKey{}).(string)
		return v, "sfdi", ok
	}
	inner := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	h := r.Middleware(identity)(inner)

	req := httptest.NewRequest(http.MethodGet, "/dcap", nil)
	h.ServeHTTP(httptest.NewRecorder(), req.WithContext(context.WithValue(req.Context(), ctxKey{}, "lfdi-a")))
	if _, n, ok := r.Last("lfdi-a"); !ok || n != 1 {
		t.Errorf("identified request: Last count = %d ok=%v, want 1 true", n, ok)
	}

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/dcap", nil))
	if rec.Code != http.StatusNoContent {
		t.Errorf("unidentified request status = %d, want pass-through 204", rec.Code)
	}
	if _, n, ok := r.Last(""); ok || n != 0 {
		t.Errorf("unidentified request recorded under empty LFDI: n=%d ok=%v", n, ok)
	}
}

type ctxKey struct{}

// A certificate-derived LFDI is 40 uppercase hex characters, but an EndDevice
// record may hold any case its writer used; both must land on one entry.
func TestLFDICaseDoesNotSplitAnEntry(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	r := activity.NewWithClock(func() time.Time { return now })
	r.Record("ABCDEF0123456789ABCDEF0123456789ABCDEF01")

	for _, lfdi := range []string{"ABCDEF0123456789ABCDEF0123456789ABCDEF01", "abcdef0123456789abcdef0123456789abcdef01"} {
		if got := r.State(lfdi, now, threshold); got != activity.Online {
			t.Errorf("State(%q) = %q, want online", lfdi, got)
		}
		if _, n, ok := r.Last(lfdi); !ok || n != 1 {
			t.Errorf("Last(%q) count = %d ok=%v, want 1 true", lfdi, n, ok)
		}
	}
}

// Record and Last run from many goroutines so -race has something to see, and
// every Record must be counted.
func TestConcurrentRecordAndLast(t *testing.T) {
	t.Parallel()
	r := activity.New()
	const workers, each = 16, 200
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < each; i++ {
				r.Record("lfdi-a")
				r.Last("lfdi-a")
				r.State("lfdi-a", time.Now(), threshold)
			}
		}()
	}
	wg.Wait()
	if _, n, _ := r.Last("lfdi-a"); n != workers*each {
		t.Errorf("count = %d, want %d", n, workers*each)
	}
}

// A request that reads the clock first must not overwrite a later one: the
// stored time never goes backwards.
func TestLastTimeNeverGoesBackwards(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	tick := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	r := activity.NewWithClock(func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		tick = tick.Add(time.Millisecond)
		return tick
	})
	var wg sync.WaitGroup
	for w := 0; w < 8; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var prev time.Time
			for i := 0; i < 200; i++ {
				r.Record("lfdi-a")
				at, _, _ := r.Last("lfdi-a")
				if at.Before(prev) {
					t.Errorf("last time went backwards: %v after %v", at, prev)
					return
				}
				prev = at
			}
		}()
	}
	wg.Wait()
}
