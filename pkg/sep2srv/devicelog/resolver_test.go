package devicelog

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func fixedNow(now *time.Time) func() time.Time { return func() time.Time { return *now } }

func TestResolverAnswersFromCacheAfterOneBackgroundLookup(t *testing.T) {
	var calls atomic.Int32
	now := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	r := NewResolver(ResolverConfig{
		CacheTTL:  time.Minute,
		TargetTTL: time.Minute,
		Registered: func(context.Context, string) (bool, error) {
			calls.Add(1)
			return true, nil
		},
		Target: func(context.Context, string) (string, error) { return lfdiB, nil },
	})
	r.now = fixedNow(&now)

	if got := r.Resolve(lfdiA, "7"); got.Known || got.Target != "" || got.Err != nil {
		t.Fatalf("first Resolve = %+v; want not known, no error, while the lookup runs", got)
	}
	r.wg.Wait()
	for i := 0; i < 3; i++ {
		got := r.Resolve(lfdiA, "7")
		if !got.Known || !got.Registered || got.Target != lfdiB {
			t.Fatalf("Resolve = %+v; want registered with target", got)
		}
	}
	if calls.Load() != 1 {
		t.Errorf("store asked %d times within the TTL; want 1", calls.Load())
	}
	now = now.Add(time.Minute + time.Second)
	r.Resolve(lfdiA, "7")
	r.wg.Wait()
	if calls.Load() != 2 {
		t.Errorf("store asked %d times after the TTL; want 2", calls.Load())
	}
}

// Resolve must return at once whatever the store does, and lines that miss
// while a lookup runs must share it.
func TestResolverNeverWaitsOnAStoreThatIgnoresItsContext(t *testing.T) {
	block := make(chan struct{})
	var started atomic.Int32
	r := NewResolver(ResolverConfig{
		MaxInFlight: 2,
		Registered: func(context.Context, string) (bool, error) {
			started.Add(1)
			<-block
			return true, nil
		},
	})
	start := time.Now()
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); r.Resolve("A", "") }()
	}
	wg.Wait()
	for _, k := range []string{"B", "C", "D", "E"} {
		if got := r.Resolve(k, ""); got.Known {
			t.Errorf("Resolve(%s) = %+v while the store is wedged; want not known", k, got)
		}
	}
	if d := time.Since(start); d > 200*time.Millisecond {
		t.Errorf("54 Resolve calls took %v against a wedged store; want no waiting", d)
	}
	for i := 0; i < 200 && started.Load() < 2; i++ {
		time.Sleep(5 * time.Millisecond)
	}
	if started.Load() != 2 {
		t.Errorf("store lookups started = %d; want 2 (one for A shared by 50 lines, one for B, limit 2)", started.Load())
	}
	if r.Refused() != 3 {
		t.Errorf("Refused = %d; want 3 (C, D, E)", r.Refused())
	}
	close(block)
	r.wg.Wait()
}

// A slow store that honours its context must not make a line wait or drop:
// each Resolve is a cache read.
func TestResolverSlowStoreCostsTheCallerNothing(t *testing.T) {
	r := NewResolver(ResolverConfig{
		LookupTimeout: 100 * time.Millisecond,
		Registered: func(ctx context.Context, _ string) (bool, error) {
			<-ctx.Done()
			return false, ctx.Err()
		},
	})
	start := time.Now()
	for i := 0; i < 300; i++ {
		r.Resolve("A", "")
	}
	if d := time.Since(start); d > 100*time.Millisecond {
		t.Errorf("300 Resolve calls took %v with a slow store; want a cache read each", d)
	}
	r.wg.Wait()
	got := r.Resolve("A", "")
	if got.Known || !errors.Is(got.Err, context.DeadlineExceeded) {
		t.Errorf("after the timeout Resolve = %+v; want not known with the deadline error", got)
	}
}

func TestResolverStorePanicIsAnErrorNotACrash(t *testing.T) {
	r := NewResolver(ResolverConfig{
		Registered: func(context.Context, string) (bool, error) { panic("boom") },
	})
	r.Resolve(lfdiA, "")
	r.wg.Wait()
	if got := r.Resolve(lfdiA, ""); got.Err == nil || got.Known {
		t.Errorf("Resolve = %+v; want the panic as an error", got)
	}
}

func TestResolverDefiniteNoIsKnownNotAnError(t *testing.T) {
	r := NewResolver(ResolverConfig{
		Registered: func(context.Context, string) (bool, error) { return false, nil },
		Target:     func(context.Context, string) (string, error) { return "", nil },
	})
	r.Resolve(lfdiA, "7")
	r.wg.Wait()
	if got := r.Resolve(lfdiA, "7"); !got.Known || got.Registered || got.Target != "" || got.Err != nil {
		t.Errorf("Resolve = %+v; want a known not-registered answer", got)
	}
}

func TestResolverNegativeAnswerExpiresQuicklySoANewDeviceMovesToItsOwnFile(t *testing.T) {
	var registered atomic.Bool
	now := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	r := NewResolver(ResolverConfig{
		CacheTTL: time.Hour,
		ErrorTTL: time.Second,
		Registered: func(context.Context, string) (bool, error) {
			return registered.Load(), nil
		},
	})
	r.now = fixedNow(&now)

	r.Resolve(lfdiA, "")
	r.wg.Wait()
	registered.Store(true)
	if got := r.Resolve(lfdiA, ""); !got.Known || got.Registered {
		t.Fatalf("Resolve = %+v; want the cached not-registered answer", got)
	}
	now = now.Add(1100 * time.Millisecond)
	r.Resolve(lfdiA, "")
	r.wg.Wait()
	if got := r.Resolve(lfdiA, ""); !got.Registered {
		t.Errorf("Resolve = %+v after the short negative TTL; want registered", got)
	}
}

// The target is the one answer an id reuse can make wrong, so it expires on
// its own short TTL even while the registration answer is still cached.
func TestResolverTargetExpiresBeforeTheRegistrationAnswer(t *testing.T) {
	var target atomic.Value
	target.Store(lfdiA)
	now := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	r := NewResolver(ResolverConfig{
		CacheTTL:   time.Hour,
		TargetTTL:  time.Second,
		Registered: func(context.Context, string) (bool, error) { return true, nil },
		Target:     func(context.Context, string) (string, error) { return target.Load().(string), nil },
	})
	r.now = fixedNow(&now)

	r.Resolve(lfdiA, "7")
	r.wg.Wait()
	if got := r.Resolve(lfdiA, "7"); got.Target != lfdiA {
		t.Fatalf("Target = %q; want %s", got.Target, lfdiA)
	}
	target.Store(lfdiB) // id 7 deleted and recreated for another LFDI
	now = now.Add(1100 * time.Millisecond)
	r.Resolve(lfdiA, "7")
	r.wg.Wait()
	got := r.Resolve(lfdiA, "7")
	if !got.Known || !got.Registered || got.Target != lfdiB {
		t.Errorf("Resolve = %+v; want the new target %s with registration still cached", got, lfdiB)
	}
}
