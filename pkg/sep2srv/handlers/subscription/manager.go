package subscription

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

const notificationContentType = "application/sep+xml"

// notificationClientTimeout is the deadline for a single outbound
// notification POST. Zero would mean "wait forever"; 30 s is generous
// for a LAN-reachable subscriber while preventing worker goroutine leaks
// against slow or unreachable notification URIs.
const notificationClientTimeout = 30 * time.Second

// SubscriptionLister provides lookup of subscriptions by resource href.
// The returned records pair each subscription with its storage ID so the
// Manager can identify a specific subscription when calling Delete on a
// receiver-terminated subscription (CSIP V1.2 ERR-002,
// GRIDAPPSD/ieee-2030_5-server-go#225).
type SubscriptionLister interface {
	ListByResource(ctx context.Context, resourceHref string) ([]memory.SubscriptionRecord, error)
}

// SubscriptionWriter is an optional capability the Manager uses to remove
// a subscription whose POST receiver indicates it has terminated (HTTP
// 4xx). Stores that do not satisfy this interface get the legacy
// log-and-leave behavior; *memory.SubscriptionStore satisfies it.
type SubscriptionWriter interface {
	Delete(ctx context.Context, id string) error
}

// errDeleteAfter4xx is returned by deliver when the receiver rejects with
// a 4xx status and the Manager has dispatched a Delete for the
// subscription. The worker maps this back to a single info-level log line
// rather than the generic "deliver failed" line.
var errDeleteAfter4xx = errors.New("subscription deleted: receiver returned 4xx")

// ErrQueueFull is returned by NotifyRemoved when the worker pool's
// bounded queue cannot accept another task. Callers (e.g.
// HandleDeleteSubscription) treat this as best-effort: a full queue is
// logged but does not change the DELETE response: the spec doesn't
// require the final Notification, so dropping it is acceptable.
var ErrQueueFull = errors.New("notification queue full")

// ErrInvalidNotificationURI is returned by NotifyRemoved when the
// supplied Subscription has no NotificationURI. There is no transport
// target, so enqueueing the task would only generate a guaranteed
// failure log later on the worker.
var ErrInvalidNotificationURI = errors.New("subscription has no notificationURI")

// ErrManagerClosed is returned by NotifyRemoved when Close has already run.
// A send on the closed queue would panic (#460); this is the documented
// alternative. The spec doesn't require the final Notification, so callers
// treat it the same as ErrQueueFull: best-effort, not fatal to the caller.
var ErrManagerClosed = errors.New("notification manager closed")

type notificationTask struct {
	subscriptionID  string
	notificationURI string
	payload         []byte
}

// Manager handles dispatching notifications to subscribers.
// It uses a bounded worker pool to prevent goroutine explosion.
type Manager struct {
	store       SubscriptionLister
	client      *http.Client
	queue       chan notificationTask
	wg          sync.WaitGroup
	workerCount int
	// observer is an optional callback invoked on each notification
	// delivery outcome. nil means no-op. The outcome strings are:
	// "success", "client_error", "queue_full", "manager_closed". Server wires
	// obs.RecordNotification here; core has no prometheus dependency.
	observer func(outcome string)
	guard    *destinationGuard

	// closeMu guards closed and the close(queue) call. An enqueue holds the
	// read lock across its check-then-send so Close (the write lock) cannot
	// close the channel between the check and the send: that ordering is
	// what turns "send after close panics" into "send after close returns
	// ErrManagerClosed" (#460).
	closeMu sync.RWMutex
	closed  bool
}

// ManagerOption configures a Manager at construction.
type ManagerOption func(*Manager)

// WithDestinationPolicy sets the policy for both delivery and
// ValidateNotificationURI. Without it the zero-value DestinationPolicy applies.
func WithDestinationPolicy(p DestinationPolicy) ManagerOption {
	return func(m *Manager) { m.guard.policy = p }
}

// NewManager creates a NotificationManager with the given worker pool size.
func NewManager(store SubscriptionLister, workerCount, queueSize int, opts ...ManagerOption) *Manager {
	if workerCount < 1 {
		workerCount = 2
	}
	if queueSize < 1 {
		queueSize = 100
	}
	guard := newDestinationGuard(DestinationPolicy{})
	m := &Manager{
		store:       store,
		client:      newNotificationClient(guard, http.DefaultTransport),
		queue:       make(chan notificationTask, queueSize),
		workerCount: workerCount,
		guard:       guard,
	}
	for _, opt := range opts {
		opt(m)
	}
	return m
}

// ValidateNotificationURI reports whether uri is an acceptable notification
// destination under the Manager's DestinationPolicy. Pass it to
// HandleCreateSubscription so creation and delivery apply the same policy.
func (m *Manager) ValidateNotificationURI(ctx context.Context, uri string) error {
	return m.guard.validateURI(ctx, uri)
}

// SetObserver wires an outcome callback into the Manager. fn is called
// with one of the outcome strings ("success", "client_error",
// "queue_full", "manager_closed") after each delivery attempt and each
// dropped notification. Pass nil to
// disable. Server passes obs.RecordNotification to feed Prometheus
// counters without pulling prometheus/client_golang into core.
func (m *Manager) SetObserver(fn func(outcome string)) {
	m.observer = fn
}

// Start launches worker goroutines, blocks until ctx is cancelled, then
// closes the queue and returns once every worker has exited.
//
// internal/server starts its HTTP drain on the same cancellation, and neither
// waits on the other, so an enqueue from a draining request can race Close;
// Close and enqueue make that race safe rather than impossible (#460).
func (m *Manager) Start(ctx context.Context) {
	for i := 0; i < m.workerCount; i++ {
		m.wg.Add(1)
		go m.worker(ctx)
	}

	<-ctx.Done()
	m.Close()
	m.wg.Wait()
}

// Close closes the queue and does not wait for the workers, which exit once
// it is empty. Tasks a worker takes after Start's ctx is cancelled are
// dropped, counted and logged, not delivered. Safe to call more than once and
// concurrently with enqueues: see the closeMu field comment.
func (m *Manager) Close() {
	m.closeMu.Lock()
	defer m.closeMu.Unlock()
	if m.closed {
		return
	}
	m.closed = true
	close(m.queue)
}

// outcomeManagerClosed is the observer outcome for a notification dropped
// because the manager is shutting down.
const outcomeManagerClosed = "manager_closed"

func (m *Manager) observe(outcome string) {
	if m.observer != nil {
		m.observer(outcome)
	}
}

// enqueue attempts a non-blocking send of task, returning ErrManagerClosed
// once Close has run and ErrQueueFull when the buffer is full. Every refusal
// is a dropped notification, so it reaches the observer; that happens after
// trySend releases closeMu, so Close never waits on the callback.
func (m *Manager) enqueue(task notificationTask) error {
	err := m.trySend(task)
	switch {
	case errors.Is(err, ErrManagerClosed):
		m.observe(outcomeManagerClosed)
	case errors.Is(err, ErrQueueFull):
		m.observe("queue_full")
	}
	return err
}

// trySend holds the read lock for the whole check-then-send, which is what
// prevents the send-after-close panic: see the closeMu field comment.
func (m *Manager) trySend(task notificationTask) error {
	m.closeMu.RLock()
	defer m.closeMu.RUnlock()
	if m.closed {
		return ErrManagerClosed
	}
	select {
	case m.queue <- task:
		return nil
	default:
		return ErrQueueFull
	}
}

// NotifyRemoved enqueues a final "Removed" Notification (Status=3) targeted
// at exactly one subscriber, identified by the supplied Subscription's
// NotificationURI. The Notification's SubscriptionURI field carries the
// subscription's Href and SubscribedResource carries the resource it was
// observing, so the subscriber can correlate locally.
//
// Unlike Notify(href, status), which fans out to every subscription
// matching the resource, NotifyRemoved targets a single subscription:
// the one being deleted. Callers must hand the subscription record they
// have *before* the store-level Delete; once Delete returns, the
// subscription is gone and the ListByResource lookup that Notify uses
// would miss it.
//
// Returns ErrInvalidNotificationURI synchronously if the supplied
// subscription has no NotificationURI, ErrQueueFull when the worker
// pool's bounded queue cannot accept the task, or ErrManagerClosed once
// Close has run. All three are recoverable from the caller's perspective:
// the spec does not require the final Notification, so the deletion
// proceeds either way.
//
// Per CSIP V1.2 section 11.6 (GRIDAPPSD/ieee-2030_5-server-go#169).
func (m *Manager) NotifyRemoved(_ context.Context, sub sep2.Subscription) error {
	if sub.NotificationURI == "" {
		return fmt.Errorf("notify removed for %q: %w", sub.Href, ErrInvalidNotificationURI)
	}

	notification := sep2.Notification{
		Resource:           sep2.Resource{Href: sub.SubscribedResource},
		SubscribedResource: sub.SubscribedResource,
		SubscriptionURI:    sub.Href,
		Status:             sep2.NotificationStatusRemoved,
	}

	payload, err := xml.Marshal(&notification)
	if err != nil {
		return fmt.Errorf("notify removed: marshal Notification for %q: %w", sub.Href, err)
	}

	task := notificationTask{
		// subscriptionID intentionally left empty: the receiver-side
		// delete-on-4xx path in deliver() would be a double-delete here
		// (the caller is about to Delete or has just Deleted the sub).
		notificationURI: sub.NotificationURI,
		payload:         payload,
	}

	if err := m.enqueue(task); err != nil {
		return fmt.Errorf("notify removed for %q: %w", sub.Href, err)
	}
	return nil
}

// Notify looks up all subscriptions for the given resource and enqueues
// notification tasks for each subscriber. Non-blocking: drops if queue full.
func (m *Manager) Notify(ctx context.Context, resourceHref string, status uint8) {
	records, err := m.store.ListByResource(ctx, resourceHref)
	if err != nil {
		log.Printf("notification: list subscriptions for %s: %v", resourceHref, err)
		return
	}

	for _, rec := range records {
		sub := rec.Subscription
		notification := sep2.Notification{
			Resource:           sep2.Resource{Href: resourceHref},
			SubscribedResource: sub.SubscribedResource,
			SubscriptionURI:    sub.Href,
			Status:             status,
		}

		payload, err := xml.Marshal(&notification)
		if err != nil {
			log.Printf("notification: marshal for %s: %v", redactURI(sub.NotificationURI), err)
			continue
		}

		task := notificationTask{
			subscriptionID:  rec.ID,
			notificationURI: sub.NotificationURI,
			payload:         payload,
		}

		if err := m.enqueue(task); err != nil {
			if errors.Is(err, ErrManagerClosed) {
				log.Printf("notification: manager closed, dropping for %s", redactURI(sub.NotificationURI))
				continue
			}
			log.Printf("notification: queue full, dropping for %s", redactURI(sub.NotificationURI))
		}
	}
}

func (m *Manager) worker(ctx context.Context) {
	defer m.wg.Done()
	for task := range m.queue {
		// A cancelled ctx fails every delivery, so a task still buffered at
		// shutdown is counted as dropped instead of attempted.
		if ctx.Err() != nil {
			m.observe(outcomeManagerClosed)
			log.Printf("notification: manager shutting down, dropping for %s", redactURI(task.notificationURI))
			continue
		}
		err := m.deliver(ctx, task)
		switch {
		case err == nil:
			m.observe("success")
		case errors.Is(err, errDeleteAfter4xx):
			m.observe("client_error")
			log.Printf("notification: %s receiver returned 4xx, subscription %q deleted",
				redactURI(task.notificationURI), task.subscriptionID)
		case errors.Is(err, ErrDestinationUnresolved):
			log.Printf("notification: cannot resolve destination %q for subscription %q: %v",
				redactURI(task.notificationURI), task.subscriptionID, err)
		case errors.Is(err, ErrRefusedDestination):
			log.Printf("notification: refused destination %q for subscription %q: %v",
				redactURI(task.notificationURI), task.subscriptionID, err)
		default:
			log.Printf("notification: deliver to %s: %v", redactURI(task.notificationURI), err)
		}
	}
}

// deliver POSTs a single notification. The supplied ctx is attached to the
// outgoing request so that worker shutdown cancels in-flight deliveries
// instead of waiting for TCP timeouts. Errors are wrapped with %w so callers
// can use errors.Is to detect context cancellation or other sentinel causes.
//
// On a 4xx response, deliver dispatches Delete on the store's
// SubscriptionWriter capability (if implemented) and returns
// errDeleteAfter4xx so the worker can log the termination at the
// appropriate level. 5xx responses are left in place: that's transient
// receiver failure, not a subscription-level signal (CSIP V1.2 ERR-002).
func (m *Manager) deliver(ctx context.Context, task notificationTask) error {
	target := redactURI(task.notificationURI)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, task.notificationURI, bytes.NewReader(task.payload))
	if err != nil {
		// The parse error can quote part of the URI, so it is not included.
		return fmt.Errorf("build notification request for %s: unparseable URI", target)
	}
	req.Header.Set("Content-Type", notificationContentType)

	resp, err := m.client.Do(req)
	if err != nil {
		return fmt.Errorf("POST notification to %s: %w", target, withoutURL(err))
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= 400 && resp.StatusCode < 500 {
		// Receiver has terminated the subscription. Best-effort delete;
		// a failed delete is logged but does not change the return: the
		// caller still needs to know this was a 4xx outcome.
		if writer, ok := m.store.(SubscriptionWriter); ok && task.subscriptionID != "" {
			if delErr := writer.Delete(ctx, task.subscriptionID); delErr != nil {
				log.Printf("notification: delete subscription %q after 4xx from %s: %v",
					task.subscriptionID, target, delErr)
			}
		}
		return fmt.Errorf("POST notification to %s: status %d: %w",
			target, resp.StatusCode, errDeleteAfter4xx)
	}

	if resp.StatusCode >= 500 {
		return fmt.Errorf("POST notification to %s: status %d (transient)",
			target, resp.StatusCode)
	}
	return nil
}
