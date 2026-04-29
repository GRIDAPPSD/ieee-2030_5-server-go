package subscription

import (
	"bytes"
	"context"
	"encoding/xml"
	"log"
	"net/http"
	"sync"

	"github.com/craig8/ieee-2030_5-go/pkg/sep2"
)

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
		m.deliver(ctx, task)
	}
}

func (m *Manager) deliver(_ context.Context, task notificationTask) {
	resp, err := m.client.Post(task.notificationURI, "application/sep+xml", bytes.NewReader(task.payload))
	if err != nil {
		log.Printf("notification: POST to %s failed: %v", task.notificationURI, err)
		return
	}
	_ = resp.Body.Close()

	if resp.StatusCode >= 400 {
		log.Printf("notification: POST to %s returned %d", task.notificationURI, resp.StatusCode)
	}
}
