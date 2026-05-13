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

	"github.com/GRIDAPPSD/ieee-2030_5-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-go/pkg/store/memory"
)

const notificationContentType = "application/sep+xml"

// SubscriptionLister provides lookup of subscriptions by resource href.
// The returned records pair each subscription with its storage ID so the
// Manager can identify a specific subscription when calling Delete on a
// receiver-terminated subscription (IEEE-080, CSIP V1.2 ERR-002).
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
}

// NewManager creates a NotificationManager with the given worker pool size.
func NewManager(store SubscriptionLister, workerCount, queueSize int) *Manager {
	if workerCount < 1 {
		workerCount = 2
	}
	if queueSize < 1 {
		queueSize = 100
	}
	return &Manager{
		store:       store,
		client:      &http.Client{},
		queue:       make(chan notificationTask, queueSize),
		workerCount: workerCount,
	}
}

// Start launches worker goroutines. Blocks until ctx is cancelled.
func (m *Manager) Start(ctx context.Context) {
	for i := 0; i < m.workerCount; i++ {
		m.wg.Add(1)
		go m.worker(ctx)
	}

	<-ctx.Done()
	close(m.queue)
	m.wg.Wait()
}

// Notify looks up all subscriptions for the given resource and enqueues
// notification tasks for each subscriber. Non-blocking — drops if queue full.
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
			log.Printf("notification: marshal for %s: %v", sub.NotificationURI, err)
			continue
		}

		task := notificationTask{
			subscriptionID:  rec.ID,
			notificationURI: sub.NotificationURI,
			payload:         payload,
		}

		select {
		case m.queue <- task:
		default:
			log.Printf("notification: queue full, dropping for %s", sub.NotificationURI)
		}
	}
}

func (m *Manager) worker(ctx context.Context) {
	defer m.wg.Done()
	for task := range m.queue {
		err := m.deliver(ctx, task)
		switch {
		case err == nil:
			// success
		case errors.Is(err, errDeleteAfter4xx):
			log.Printf("notification: %s receiver returned 4xx, subscription %q deleted",
				task.notificationURI, task.subscriptionID)
		default:
			log.Printf("notification: deliver to %s: %v", task.notificationURI, err)
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
// appropriate level. 5xx responses are left in place — that's transient
// receiver failure, not a subscription-level signal (CSIP V1.2 ERR-002).
func (m *Manager) deliver(ctx context.Context, task notificationTask) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, task.notificationURI, bytes.NewReader(task.payload))
	if err != nil {
		return fmt.Errorf("build notification request for %s: %w", task.notificationURI, err)
	}
	req.Header.Set("Content-Type", notificationContentType)

	resp, err := m.client.Do(req)
	if err != nil {
		return fmt.Errorf("POST notification to %s: %w", task.notificationURI, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= 400 && resp.StatusCode < 500 {
		// Receiver has terminated the subscription. Best-effort delete;
		// a failed delete is logged but does not change the return — the
		// caller still needs to know this was a 4xx outcome.
		if writer, ok := m.store.(SubscriptionWriter); ok && task.subscriptionID != "" {
			if delErr := writer.Delete(ctx, task.subscriptionID); delErr != nil {
				log.Printf("notification: delete subscription %q after 4xx from %s: %v",
					task.subscriptionID, task.notificationURI, delErr)
			}
		}
		return fmt.Errorf("POST notification to %s: status %d: %w",
			task.notificationURI, resp.StatusCode, errDeleteAfter4xx)
	}

	if resp.StatusCode >= 500 {
		return fmt.Errorf("POST notification to %s: status %d (transient)",
			task.notificationURI, resp.StatusCode)
	}
	return nil
}
