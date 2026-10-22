package devicelog

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

// Defaults for NewResolver.
const (
	// DefaultCacheTTL is how long a positive registration answer is reused.
	// No store notifies the resolver of a change, so a deleted device or a
	// dropped management pair reaches the log only when its entry expires.
	DefaultCacheTTL = 30 * time.Second
	// DefaultErrorTTL is how long a negative answer (not registered, no such
	// EndDevice) or a failed lookup is reused. It is short so a device that
	// has just registered gets its own file within this interval.
	DefaultErrorTTL = time.Second
	// DefaultTargetTTL is how long a resolved target is reused. An EndDevice
	// id can be deleted and recreated for another LFDI through a path no
	// protocol request sees (a fixture, the admin plane), and nothing tells
	// the resolver, so the target is the one answer kept short: a reused id
	// can show its old device for at most this long.
	DefaultTargetTTL = time.Second
	// DefaultLookupTimeout is the context deadline given to one lookup.
	DefaultLookupTimeout = 250 * time.Millisecond
	// DefaultMaxInFlight bounds lookups still running against the stores.
	DefaultMaxInFlight = 16

	maxCacheEntries = 4096
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

	// CacheTTL, ErrorTTL, TargetTTL, LookupTimeout and MaxInFlight take the
	// Default values when zero.
	CacheTTL      time.Duration
	ErrorTTL      time.Duration
	TargetTTL     time.Duration
	LookupTimeout time.Duration
	MaxInFlight   int
}

type cacheEntry[T any] struct {
	val     T
	err     error
	expires time.Time
}

// cache is one keyed answer store with the set of keys being looked up.
type cache[T comparable] struct {
	entries  map[string]cacheEntry[T]
	inflight map[string]struct{}
}

func newCache[T comparable]() cache[T] {
	return cache[T]{entries: make(map[string]cacheEntry[T]), inflight: make(map[string]struct{})}
}

// CachingResolver implements the Writer's lookup without ever waiting on a
// store. Resolve reads the cache; on a miss it starts one background lookup
// for that key, shared by every line that misses meanwhile, and answers
// "not known" now. At most MaxInFlight lookups run at once; past that a miss
// starts none. A lookup that ignores its context holds its slot and its key
// until it returns, so a wedged store costs a fixed number of goroutines.
type CachingResolver struct {
	cfg ResolverConfig
	sem chan struct{}
	now func() time.Time
	wg  sync.WaitGroup

	refused atomic.Uint64

	mu      sync.Mutex
	actors  cache[bool]
	targets cache[string]
}

// NewResolver builds a CachingResolver from cfg.
func NewResolver(cfg ResolverConfig) *CachingResolver {
	if cfg.CacheTTL <= 0 {
		cfg.CacheTTL = DefaultCacheTTL
	}
	if cfg.ErrorTTL <= 0 {
		cfg.ErrorTTL = DefaultErrorTTL
	}
	if cfg.TargetTTL <= 0 {
		cfg.TargetTTL = DefaultTargetTTL
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
		actors:  newCache[bool](),
		targets: newCache[string](),
	}
}

// Refused counts misses that started no lookup because MaxInFlight lookups
// were already running.
func (r *CachingResolver) Refused() uint64 { return r.refused.Load() }

// Resolve is the Writer's Resolver. It never waits on a store.
func (r *CachingResolver) Resolve(actor, edev string) Resolution {
	reg, regErr, regHit := lookup(r, &r.actors, actor, r.cfg.Registered, r.cfg.CacheTTL)
	res := Resolution{Known: regHit && regErr == nil, Registered: reg, Err: regErr}
	if edev != "" && r.cfg.Target != nil {
		if t, tErr, tHit := lookup(r, &r.targets, edev, r.cfg.Target, r.cfg.TargetTTL); tHit && tErr == nil {
			res.Target = t
		}
	}
	return res
}

// lookup returns the cached answer for key and whether there was a fresh
// one. A miss starts a background lookup unless one is already running for
// the key or the in-flight limit is reached.
func lookup[T comparable](r *CachingResolver, c *cache[T], key string, f func(context.Context, string) (T, error), posTTL time.Duration) (val T, err error, hit bool) {
	var zero T
	if f == nil {
		return zero, nil, false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if e, ok := c.entries[key]; ok && r.now().Before(e.expires) {
		return e.val, e.err, true
	}
	if _, running := c.inflight[key]; running {
		return zero, nil, false
	}
	select {
	case r.sem <- struct{}{}:
	default:
		r.refused.Add(1)
		return zero, nil, false
	}
	c.inflight[key] = struct{}{}
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		defer func() { <-r.sem }()
		ctx, cancel := context.WithTimeout(context.Background(), r.cfg.LookupTimeout)
		defer cancel()
		val, err := safely(ctx, key, f)
		ttl := posTTL
		if err != nil || val == zero {
			ttl = r.cfg.ErrorTTL
		}
		r.mu.Lock()
		if len(c.entries) >= maxCacheEntries {
			clear(c.entries)
		}
		c.entries[key] = cacheEntry[T]{val: val, err: err, expires: r.now().Add(ttl)}
		delete(c.inflight, key)
		r.mu.Unlock()
	}()
	return zero, nil, false
}

// safely runs f and turns a store panic into an error, so it cannot take the
// server down from a goroutine no request owns.
func safely[T any](ctx context.Context, key string, f func(context.Context, string) (T, error)) (val T, err error) {
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("device log: lookup panicked: %v", p)
		}
	}()
	return f(ctx, key)
}
