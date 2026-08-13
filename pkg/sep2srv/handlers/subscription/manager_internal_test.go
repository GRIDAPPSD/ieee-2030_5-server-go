package subscription

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

// TestDeliverHappyPath verifies the POST carries the expected payload and
// Content-Type, and that deliver returns nil on 2xx.
func TestDeliverHappyPath(t *testing.T) {
	t.Parallel()

	var gotBody []byte
	var gotContentType string
	var gotMethod string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotContentType = r.Header.Get("Content-Type")
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read request body: %v", err)
		}
		gotBody = body
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	m := NewManager(&mockSubStore{}, 1, 1)

	payload := []byte("<Notification/>")
	task := notificationTask{notificationURI: srv.URL, payload: payload}

	if err := m.deliver(context.Background(), task); err != nil {
		t.Fatalf("deliver returned error: %v", err)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("method = %q, want POST", gotMethod)
	}
	if gotContentType != notificationContentType {
		t.Errorf("Content-Type = %q, want %q", gotContentType, notificationContentType)
	}
	if string(gotBody) != string(payload) {
		t.Errorf("body = %q, want %q", string(gotBody), string(payload))
	}
}

// TestDeliverContextCanceled verifies that a parent context canceled mid-flight
// produces a wrapped error satisfying errors.Is(err, context.Canceled).
func TestDeliverContextCanceled(t *testing.T) {
	t.Parallel()

	released := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Block until the test signals — long enough that ctx cancel wins.
		<-released
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	defer close(released)

	m := NewManager(&mockSubStore{}, 1, 1)
	ctx, cancel := context.WithCancel(context.Background())

	task := notificationTask{notificationURI: srv.URL, payload: []byte("<Notification/>")}

	errCh := make(chan error, 1)
	go func() {
		errCh <- m.deliver(ctx, task)
	}()

	// Let the request reach the handler, then cancel.
	time.Sleep(20 * time.Millisecond)
	cancel()

	select {
	case err := <-errCh:
		if err == nil {
			t.Fatal("deliver returned nil, want context.Canceled wrapped error")
		}
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("errors.Is(err, context.Canceled) = false; err = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("deliver did not return after parent context cancel")
	}
}

// TestDeliverNon200 verifies that a 5xx response yields a non-nil error.
func TestDeliverNon200(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	m := NewManager(&mockSubStore{}, 1, 1)
	task := notificationTask{notificationURI: srv.URL, payload: []byte("<Notification/>")}

	err := m.deliver(context.Background(), task)
	if err == nil {
		t.Fatal("deliver returned nil for 500 response, want non-nil error")
	}
}

// TestWorkerShutdownCancelsInFlightDeliver verifies that canceling the
// worker's parent context unblocks an in-flight deliver promptly, rather
// than waiting for the underlying TCP / handler timeout.
func TestWorkerShutdownCancelsInFlightDeliver(t *testing.T) {
	t.Parallel()

	var inHandler atomic.Bool
	released := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		inHandler.Store(true)
		// Sleep far longer than the test bound; the worker's ctx cancel
		// must abort the client request before this wakes.
		select {
		case <-released:
		case <-r.Context().Done():
		case <-time.After(30 * time.Second):
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	defer close(released)

	m := NewManager(&mockSubStore{}, 1, 1)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		m.Start(ctx)
		close(done)
	}()

	// Enqueue a task that will park the worker inside deliver.
	m.queue <- notificationTask{
		notificationURI: srv.URL,
		payload:         []byte("<Notification/>"),
	}

	// Wait until the handler reports it has the request.
	waitDeadline := time.After(1 * time.Second)
	for !inHandler.Load() {
		select {
		case <-waitDeadline:
			t.Fatal("handler never observed the request")
		default:
			time.Sleep(5 * time.Millisecond)
		}
	}

	// Cancel the worker's parent ctx. The in-flight deliver must abort
	// promptly, the worker must drain, and Start must return.
	shutdownStart := time.Now()
	cancel()

	select {
	case <-done:
		elapsed := time.Since(shutdownStart)
		if elapsed > 500*time.Millisecond {
			t.Fatalf("shutdown took %v, want < 500ms (TCP-timeout-style stall)", elapsed)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("manager did not shut down after parent context cancel")
	}
}

// mockSubStore satisfies SubscriptionLister with a static list. Duplicated
// (intentionally) in the external test file; the internal tests can't import
// the _test package so a local copy is the idiomatic Go pattern here.
type mockSubStore struct {
	subs []sep2.Subscription
}

func (m *mockSubStore) ListByResource(_ context.Context, href string) ([]memory.SubscriptionRecord, error) {
	var result []memory.SubscriptionRecord
	for i, s := range m.subs {
		if s.SubscribedResource == href {
			result = append(result, memory.SubscriptionRecord{
				ID:           fmt.Sprintf("mock-sub-%d", i),
				Subscription: s,
			})
		}
	}
	return result, nil
}
