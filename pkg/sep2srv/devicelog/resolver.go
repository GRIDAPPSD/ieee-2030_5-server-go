package devicelog

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

// Defaults for NewResolver.
const (
	// DefaultCacheTTL is how long a registered, not-registered or target
	// answer is reused. No store notifies the resolver of a change, so a new
	// registration or management pair reaches the log only when its entry
	// expires: a device's lines move from the shared file to its own within
	// one TTL.
	DefaultCacheTTL = 30 * time.Second
	// DefaultErrorTTL is how long a failed lookup is reused, so a stalled
	// store costs one wait per key per interval and not one per line.
	DefaultErrorTTL = time.Second
	// DefaultLookupTimeout is the longest the writer waits for one lookup.
	DefaultLookupTimeout = 250 * time.Millisecond
	// DefaultMaxInFlight bounds lookups still running against the stores.
	DefaultMaxInFlight = 16

	maxCacheEntries = 4096
)

var (
	// ErrLookupTimeout means a lookup did not answer within the timeout.
	ErrLookupTimeout = errors.New("device log: lookup timed out")
	// ErrLookupBusy means too many earlier lookups are still running, so none
	// was started.
	ErrLookupBusy = errors.New("device log: too many lookups in flight")
)

// ResolverConfig configures NewResolver.
type ResolverConfig struct {
	// Registered reports whether actor has an EndDevice record or a
	// management pair. A definite no is (false, nil); only a failure to find
	// out is an error.
	Registered func(ctx context.Context, actor string) (bool, error)
	// Target returns the canonical LFDI of the EndDevice edev names, or ""
	// with a nil error when it does not resolve.
	Target func(ctx context.Context, edev string) (string, error)

	// CacheTTL, ErrorTTL, LookupTimeout and MaxInFlight take the Default
	// values when zero.
	CacheTTL      time.Duration
	ErrorTTL      time.Duration
	LookupTimeout time.Duration
	MaxInFlight   int
}

type cacheEntry[T any] struct {
	val     T
	err     error
	expires time.Time
}

// CachingResolver implements the Writer's lookup with a hard bound: each store call
// runs in its own goroutine and the caller waits at most LookupTimeout for
// it, so a store that ignores its context cannot stall the writer. At most
// MaxInFlight calls run at once; past that a lookup fails at once with
// ErrLookupBusy. Answers are cached per actor and per EndDevice id.
type CachingResolver struct {
	cfg ResolverConfig
	sem chan struct{}
	now func() time.Time

	mu      sync.Mutex
	actors  map[string]cacheEntry[bool]
	targets map[string]cacheEntry[string]
}

// NewResolver builds a bounded, caching Resolver from cfg.
func NewResolver(cfg ResolverConfig) *CachingResolver {
	if cfg.CacheTTL <= 0 {
		cfg.CacheTTL = DefaultCacheTTL
	}
	if cfg.ErrorTTL <= 0 {
		cfg.ErrorTTL = DefaultErrorTTL
	}
	if cfg.LookupTimeout <= 0 {
		cfg.LookupTimeout = DefaultLookupTimeout
	}
	if cfg.MaxInFlight <= 0 {
		cfg.MaxInFlight = DefaultMaxInFlight
	}
	return &CachingResolver{
		cfg:     cfg,
		sem:     make(chan struct{}, cfg.MaxInFlight),
		now:     time.Now,
		actors:  make(map[string]cacheEntry[bool]),
		targets: make(map[string]cacheEntry[string]),
	}
}

// Resolve is the Writer's Resolver.
func (r *CachingResolver) Resolve(actor, edev string) Resolution {
	registered, regErr := cached(r, r.actors, actor, r.cfg.Registered)
	var target string
	var tErr error
	if edev != "" && r.cfg.Target != nil {
		target, tErr = cached(r, r.targets, edev, r.cfg.Target)
	}
	return Resolution{Registered: registered, Target: target, Err: errors.Join(regErr, tErr)}
}

func cached[T any](r *CachingResolver, m map[string]cacheEntry[T], key string, f func(context.Context, string) (T, error)) (T, error) {
	if f == nil {
		var zero T
		return zero, nil
	}
	now := r.now()
	r.mu.Lock()
	e, ok := m[key]
	r.mu.Unlock()
	if ok && now.Before(e.expires) {
		return e.val, e.err
	}
	val, err := bounded(r, key, f)
	ttl := r.cfg.CacheTTL
	if err != nil {
		ttl = r.cfg.ErrorTTL
	}
	r.mu.Lock()
	if len(m) >= maxCacheEntries {
		clear(m)
	}
	m[key] = cacheEntry[T]{val: val, err: err, expires: now.Add(ttl)}
	r.mu.Unlock()
	return val, err
}

// bounded runs f and waits at most LookupTimeout for it. The in-flight slot
// is held until f really returns, so abandoned calls stay counted.
func bounded[T any](r *CachingResolver, key string, f func(context.Context, string) (T, error)) (T, error) {
	var zero T
	select {
	case r.sem <- struct{}{}:
	default:
		return zero, ErrLookupBusy
	}
	type result struct {
		val T
		err error
	}
	done := make(chan result, 1)
	ctx, cancel := context.WithTimeout(context.Background(), r.cfg.LookupTimeout)
	go func() {
		defer func() { <-r.sem; cancel() }()
		defer func() {
			// A store panic must not take the server down from a goroutine
			// no request owns.
			if p := recover(); p != nil {
				done <- result{err: fmt.Errorf("device log: lookup panicked: %v", p)}
			}
		}()
		v, err := f(ctx, key)
		done <- result{v, err}
	}()
	timer := time.NewTimer(r.cfg.LookupTimeout)
	defer timer.Stop()
	select {
	case res := <-done:
		return res.val, res.err
	case <-timer.C:
		return zero, ErrLookupTimeout
	}
}
