package storetest

import (
	"context"
	"errors"
	"sync"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
)

// ErrBackendUnavailable is the transient failure the fault decorators return
// when armed: the backend did not answer, which is neither "the resource is
// absent" nor "the collection is empty".
//
// It is deliberately NOT one of the four sentinels in pkg/store, and
// [TestErrBackendUnavailableIsNotASentinel] pins that. A caller matching with
// errors.Is sees neither ErrNotFound nor ErrAlreadyExists nor
// ErrUnsupportedSort nor ErrInvalidListOptions, which is what makes it the
// right probe for the contract's "ANY other non-nil error" clause: a route
// that renders it as 404, as an empty list, or as a synthesized default is
// conflating a broken backend with a missing resource.
var ErrBackendUnavailable = errors.New("storetest: backend unavailable")

// Fault is the arming switch a set of fault decorators share.
//
// One switch drives a whole fleet of stores, which is what makes "the backend
// went away" expressible as a single event rather than as a per-store loop
// whose middle is a half-failed server. A route-coverage table arms it once and
// every store behind every mounted route fails together.
//
// The zero value is disarmed and ready to use. It is safe for concurrent use,
// because an armed store is read by whatever goroutines the server has in
// flight.
type Fault struct {
	mu  sync.RWMutex
	err error
}

// Arm makes every decorator sharing this switch return err from every
// operation. Passing nil is the same as [Fault.Disarm].
func (f *Fault) Arm(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.err = err
}

// Disarm restores normal operation: every decorator sharing this switch
// delegates to the store it wraps again.
func (f *Fault) Disarm() {
	f.Arm(nil)
}

// Err returns the armed error, or nil when the switch is disarmed.
func (f *Fault) Err() error {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return f.err
}

// NewFaultyResourceStore wraps inner in a decorator that fails every operation
// while fault is armed and delegates to inner otherwise.
//
// It decorates rather than reimplements on purpose. [Fake] is an independent
// implementation that can also be told to fail, and it is the right tool for
// testing the CONTRACT. This is the right tool for testing a CONSUMER of the
// contract, because the happy path stays byte for byte whatever the wrapped
// implementation does: wrap the production in-memory store and the only thing
// that differs from production is the injected failure itself. A consumer test
// that passed against a reimplementation might be passing because the
// reimplementation is simpler, and that is precisely the doubt this removes.
func NewFaultyResourceStore[T store.Copier[T]](inner store.ResourceStore[T], fault *Fault) store.ResourceStore[T] {
	return &faultyResourceStore[T]{inner: inner, fault: fault}
}

type faultyResourceStore[T store.Copier[T]] struct {
	inner store.ResourceStore[T]
	fault *Fault
}

func (s *faultyResourceStore[T]) Get(ctx context.Context, id string) (T, error) {
	if err := s.fault.Err(); err != nil {
		var zero T
		return zero, err
	}
	return s.inner.Get(ctx, id)
}

func (s *faultyResourceStore[T]) List(ctx context.Context, opts store.ListOptions) (store.ListResult[T], error) {
	if err := s.fault.Err(); err != nil {
		return store.ListResult[T]{}, err
	}
	return s.inner.List(ctx, opts)
}

func (s *faultyResourceStore[T]) Count(ctx context.Context) (uint32, error) {
	if err := s.fault.Err(); err != nil {
		return 0, err
	}
	return s.inner.Count(ctx)
}

func (s *faultyResourceStore[T]) Create(ctx context.Context, id string, resource T) error {
	if err := s.fault.Err(); err != nil {
		return err
	}
	return s.inner.Create(ctx, id, resource)
}

func (s *faultyResourceStore[T]) Update(ctx context.Context, id string, resource T) error {
	if err := s.fault.Err(); err != nil {
		return err
	}
	return s.inner.Update(ctx, id, resource)
}

func (s *faultyResourceStore[T]) Delete(ctx context.Context, id string) error {
	if err := s.fault.Err(); err != nil {
		return err
	}
	return s.inner.Delete(ctx, id)
}

// NewFaultyEndDeviceStore is [NewFaultyResourceStore] for the EndDevice store,
// whose two identity lookups are outside the generic contract and would
// otherwise be lost by wrapping.
func NewFaultyEndDeviceStore(inner store.EndDeviceStore, fault *Fault) store.EndDeviceStore {
	return &faultyEndDeviceStore{
		ResourceStore: NewFaultyResourceStore[sep2.EndDevice](inner, fault),
		inner:         inner,
		fault:         fault,
	}
}

type faultyEndDeviceStore struct {
	store.ResourceStore[sep2.EndDevice]
	inner store.EndDeviceStore
	fault *Fault
}

func (s *faultyEndDeviceStore) GetBySFDI(ctx context.Context, sfdi string) (sep2.EndDevice, error) {
	if err := s.fault.Err(); err != nil {
		return sep2.EndDevice{}, err
	}
	return s.inner.GetBySFDI(ctx, sfdi)
}

func (s *faultyEndDeviceStore) GetByLFDI(ctx context.Context, lfdi string) (sep2.EndDevice, error) {
	if err := s.fault.Err(); err != nil {
		return sep2.EndDevice{}, err
	}
	return s.inner.GetByLFDI(ctx, lfdi)
}

// parentCascader is the optional bulk-parent-removal capability the metering
// handler asserts for at the point of use. It is redeclared here, at the
// consumer, because the handler's copy is unexported and this decorator has to
// answer the same question the handler asks.
type parentCascader interface {
	DeleteParent(ctx context.Context, parentID string) (uint32, error)
}

// NewFaultyScopedStore wraps inner in a decorator that fails every operation
// while fault is armed and delegates to inner otherwise.
//
// # Why the capability is preserved rather than dropped
//
// Optional capabilities are asserted for at the point of use: the metering
// handler asks whether the readings store can cascade a parent delete, and
// refuses DELETE /mup/{id} with a 500 when it cannot. A decorator that dropped
// the capability would push that route down the refusal branch, so the route
// would answer 500 no matter what the fault switch was doing, and a test
// asserting 500 there would be measuring the wrapper rather than the handler.
//
// So the returned decorator claims the capability exactly when inner has it,
// and never otherwise: a wrapper must not invent a capability its inner store
// lacks, and must not hide one it has. That is why this returns an interface
// and picks between two concrete types rather than declaring DeleteParent
// unconditionally.
func NewFaultyScopedStore[T store.Copier[T]](inner store.ScopedStore[T], fault *Fault) store.ScopedStore[T] {
	base := &faultyScopedStore[T]{inner: inner, fault: fault}
	if cascader, ok := inner.(parentCascader); ok {
		return &faultyCascadingScopedStore[T]{faultyScopedStore: base, cascader: cascader}
	}
	return base
}

type faultyScopedStore[T store.Copier[T]] struct {
	inner store.ScopedStore[T]
	fault *Fault
}

func (s *faultyScopedStore[T]) Get(ctx context.Context, parentID, id string) (T, error) {
	if err := s.fault.Err(); err != nil {
		var zero T
		return zero, err
	}
	return s.inner.Get(ctx, parentID, id)
}

func (s *faultyScopedStore[T]) List(ctx context.Context, parentID string, opts store.ListOptions) (store.ListResult[T], error) {
	if err := s.fault.Err(); err != nil {
		return store.ListResult[T]{}, err
	}
	return s.inner.List(ctx, parentID, opts)
}

func (s *faultyScopedStore[T]) Count(ctx context.Context, parentID string) (uint32, error) {
	if err := s.fault.Err(); err != nil {
		return 0, err
	}
	return s.inner.Count(ctx, parentID)
}

func (s *faultyScopedStore[T]) HasParent(ctx context.Context, parentID string) (bool, error) {
	if err := s.fault.Err(); err != nil {
		return false, err
	}
	return s.inner.HasParent(ctx, parentID)
}

func (s *faultyScopedStore[T]) Parents(ctx context.Context) ([]string, error) {
	if err := s.fault.Err(); err != nil {
		return nil, err
	}
	return s.inner.Parents(ctx)
}

func (s *faultyScopedStore[T]) Create(ctx context.Context, parentID, id string, resource T) error {
	if err := s.fault.Err(); err != nil {
		return err
	}
	return s.inner.Create(ctx, parentID, id, resource)
}

func (s *faultyScopedStore[T]) Update(ctx context.Context, parentID, id string, resource T) error {
	if err := s.fault.Err(); err != nil {
		return err
	}
	return s.inner.Update(ctx, parentID, id, resource)
}

func (s *faultyScopedStore[T]) Delete(ctx context.Context, parentID, id string) error {
	if err := s.fault.Err(); err != nil {
		return err
	}
	return s.inner.Delete(ctx, parentID, id)
}

// faultyCascadingScopedStore is the decorator for an inner store that provides
// the optional parent-cascade capability. See [NewFaultyScopedStore].
type faultyCascadingScopedStore[T store.Copier[T]] struct {
	*faultyScopedStore[T]
	cascader parentCascader
}

func (s *faultyCascadingScopedStore[T]) DeleteParent(ctx context.Context, parentID string) (uint32, error) {
	if err := s.fault.Err(); err != nil {
		return 0, err
	}
	return s.cascader.DeleteParent(ctx, parentID)
}

// Compile-time proof that the decorators satisfy the contracts they wrap.
var (
	_ store.ResourceStore[Resource] = (*faultyResourceStore[Resource])(nil)
	_ store.ScopedStore[Resource]   = (*faultyScopedStore[Resource])(nil)
	_ store.ScopedStore[Resource]   = (*faultyCascadingScopedStore[Resource])(nil)
	_ parentCascader                = (*faultyCascadingScopedStore[Resource])(nil)
	_ store.EndDeviceStore          = (*faultyEndDeviceStore)(nil)
)
