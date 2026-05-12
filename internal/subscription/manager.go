package subscription

import (
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"log"
	"net/http"
	"sync"

	"github.com/GRIDAPPSD/ieee-2030_5-go/pkg/sep2"
)

const notificationContentType = "application/sep+xml"

// SubscriptionLister provides lookup of subscriptions by resource href.
type SubscriptionLister interface {
	ListByResource(ctx context.Context, resourceHref string) ([]sep2.Subscription, error)
}

type notificationTask struct {
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
	subs, err := m.store.ListByResource(ctx, resourceHref)
	if err != nil {
		log.Printf("notification: list subscriptions for %s: %v", resourceHref, err)
		return
	}

	for _, sub := range subs {
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
		if err := m.deliver(ctx, task); err != nil {
			log.Printf("notification: deliver to %s: %v", task.notificationURI, err)
		}
	}
}

// deliver POSTs a single notification. The supplied ctx is attached to the
// outgoing request so that worker shutdown cancels in-flight deliveries
// instead of waiting for TCP timeouts. Errors are wrapped with %w so callers
// can use errors.Is to detect context cancellation or other sentinel causes.
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

	if resp.StatusCode >= 400 {
		return fmt.Errorf("POST notification to %s: status %d", task.notificationURI, resp.StatusCode)
	}
	return nil
}
