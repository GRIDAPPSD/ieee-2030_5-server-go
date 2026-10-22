package devicelog

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func TestResolverCachesUntilTTLThenAsksAgain(t *testing.T) {
	var calls atomic.Int32
	now := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	r := NewResolver(ResolverConfig{
		CacheTTL: time.Minute,
		Registered: func(context.Context, string) (bool, error) {
			calls.Add(1)
			return true, nil
		},
		Target: func(context.Context, string) (string, error) { return lfdiB, nil },
	})
	r.now = func() time.Time { return now }

	for i := 0; i < 3; i++ {
		got := r.Resolve(lfdiA, "7")
		if !got.Registered || got.Target != lfdiB || got.Err != nil {
			t.Fatalf("Resolve = %+v; want registered with target", got)
		}
	}
	if calls.Load() != 1 {
		t.Errorf("store asked %d times within the TTL; want 1", calls.Load())
	}
	now = now.Add(time.Minute + time.Second)
	r.Resolve(lfdiA, "7")
	if calls.Load() != 2 {
		t.Errorf("store asked %d times after the TTL; want 2", calls.Load())
	}
}

// A store that ignores its context entirely must cost the caller at most the
// lookup timeout, then at most a cache read until the error entry expires.
func TestResolverStoreIgnoringContextCannotStallTheCaller(t *testing.T) {
	block := make(chan struct{})
	t.Cleanup(func() { close(block) })
	r := NewResolver(ResolverConfig{
		LookupTimeout: 30 * time.Millisecond,
		Registered: func(context.Context, string) (bool, error) {
			<-block
			return true, nil
		},
	})

	start := time.Now()
	got := r.Resolve(lfdiA, "")
	if !errors.Is(got.Err, ErrLookupTimeout) {
		t.Fatalf("Resolve error = %v; want ErrLookupTimeout", got.Err)
	}
	if d := time.Since(start); d > 400*time.Millisecond {
		t.Errorf("Resolve took %v against a wedged store; want about the 30ms bound", d)
	}
	start = time.Now()
	if got := r.Resolve(lfdiA, ""); got.Err == nil {
		t.Error("second Resolve inside the error TTL returned no error")
	}
	if d := time.Since(start); d > 10*time.Millisecond {
		t.Errorf("repeat Resolve took %v; want a cache read", d)
	}
}

func TestResolverInFlightLimitFailsFastAndNeverGrowsGoroutines(t *testing.T) {
	block := make(chan struct{})
	t.Cleanup(func() { close(block) })
	var started atomic.Int32
	r := NewResolver(ResolverConfig{
		LookupTimeout: 20 * time.Millisecond,
		MaxInFlight:   2,
		Registered: func(context.Context, string) (bool, error) {
			started.Add(1)
			<-block
			return true, nil
		},
	})
	keys := []string{"A", "B", "C", "D", "E"}
	var busy int
	for _, k := range keys {
		if errors.Is(r.Resolve(k, "").Err, ErrLookupBusy) {
			busy++
		}
	}
	if started.Load() != 2 || busy != 3 {
		t.Errorf("started %d store calls and refused %d; want 2 and 3 with a limit of 2", started.Load(), busy)
	}
}

func TestResolverStorePanicIsAnErrorNotACrash(t *testing.T) {
	r := NewResolver(ResolverConfig{
		Registered: func(context.Context, string) (bool, error) { panic("boom") },
	})
	if got := r.Resolve(lfdiA, ""); got.Err == nil {
		t.Error("a panicking store produced no error")
	}
}

func TestResolverDefiniteNoIsNotAnError(t *testing.T) {
	r := NewResolver(ResolverConfig{
		Registered: func(context.Context, string) (bool, error) { return false, nil },
		Target:     func(context.Context, string) (string, error) { return "", nil },
	})
	if got := r.Resolve(lfdiA, "7"); got.Err != nil || got.Registered || got.Target != "" {
		t.Errorf("Resolve = %+v; want a clean not-registered answer", got)
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
	r.now = func() time.Time { return now }

	if r.Resolve(lfdiA, "").Registered {
		t.Fatal("registered before registration")
	}
	registered.Store(true)
	if r.Resolve(lfdiA, "").Registered {
		t.Error("negative answer not reused inside its TTL")
	}
	now = now.Add(1100 * time.Millisecond)
	if !r.Resolve(lfdiA, "").Registered {
		t.Error("device still unregistered after the short negative TTL; want it re-asked")
	}
}
