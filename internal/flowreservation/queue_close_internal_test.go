package flowreservation

import (
	"bytes"
	"context"
	"log"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
)

// blockingFRP holds every Create until release is closed, after telling the
// test it arrived.
type blockingFRP struct {
	*stubFRPStore
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (b *blockingFRP) Create(ctx context.Context, parentID, id string, r sep2.FlowReservationResponse) error {
	b.once.Do(func() { close(b.entered) })
	<-b.release
	return b.stubFRPStore.Create(ctx, parentID, id, r)
}

func newBlockedFallback(t *testing.T, failCreates int) (*Queue, *blockingFRP) {
	t.Helper()
	frq := sep2.FlowReservationRequest{MRID: "FRQ001", EnergyRequested: &sep2.SignedRealEnergy{Value: 10000}}
	frqStore := &stubFRQReader{}
	frqStore.put("dev1", "frq1", frq)
	frp := &blockingFRP{
		stubFRPStore: &stubFRPStore{failCreateTimes: failCreates},
		entered:      make(chan struct{}),
		release:      make(chan struct{}),
	}
	q := NewQueue(frqStore, frp, PermissiveGate{}, Config{Deadline: time.Millisecond, RetryBackoff: time.Millisecond, RetryAttempts: 10}, nil)
	q.Submit("dev1", "frq1", frq, 0)
	select {
	case <-frp.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("the fallback never reached its store write")
	}
	return q, frp
}

func waitClosed(t *testing.T, q *Queue) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		q.mu.Lock()
		c := q.closed
		q.mu.Unlock()
		if c {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("Close never marked the queue closed")
}

// Close returns only once a fallback already inside its store write has
// finished: nothing writes to the stores after Close returns. Waiting rather
// than abandoning keeps the response and its cleanup together.
func TestQueue_Close_WaitsForARunningFallback(t *testing.T) {
	q, frp := newBlockedFallback(t, 0)

	closed := make(chan struct{})
	go func() { q.Close(); close(closed) }()
	waitClosed(t, q)

	select {
	case <-closed:
		t.Fatal("Close returned while a fallback was still inside its store write")
	case <-time.After(100 * time.Millisecond):
	}
	if n := frp.count(); n != 0 {
		t.Fatalf("responses stored before the write was released = %d, want 0", n)
	}

	close(frp.release)
	select {
	case <-closed:
	case <-time.After(2 * time.Second):
		t.Fatal("Close did not return after the fallback finished")
	}
	if n := frp.count(); n != 1 {
		t.Errorf("responses stored once the fallback finished = %d, want 1", n)
	}
}

// A fallback that fails after Close does not claim to be retrying, and does
// not retry.
func TestQueue_FailedFallbackAfterClose_DoesNotLogOrScheduleARetry(t *testing.T) {
	var buf bytes.Buffer
	var mu sync.Mutex
	prev := log.Writer()
	log.SetOutput(writerFunc(func(p []byte) (int, error) { mu.Lock(); defer mu.Unlock(); return buf.Write(p) }))
	t.Cleanup(func() { log.SetOutput(prev) })

	q, frp := newBlockedFallback(t, 1000)
	closed := make(chan struct{})
	go func() { q.Close(); close(closed) }()
	waitClosed(t, q)
	close(frp.release)
	<-closed

	time.Sleep(30 * time.Millisecond)
	mu.Lock()
	got := buf.String()
	mu.Unlock()
	if strings.Contains(got, "retrying in") {
		t.Errorf("log claims a retry after Close:\n%s", got)
	}
	if !strings.Contains(got, "not retrying") {
		t.Errorf("log has no line saying the retry was skipped:\n%s", got)
	}
	if n := frp.createCallCount(); n != 1 {
		t.Errorf("Create calls = %d, want 1: no retry after Close", n)
	}
}

type writerFunc func(p []byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) { return f(p) }
