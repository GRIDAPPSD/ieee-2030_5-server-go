package server

import (
	"context"
	"log"
	"sync"
	"time"

	"github.com/craig8/ieee-2030_5-go/pkg/sep2"
	"github.com/craig8/ieee-2030_5-go/pkg/store"
	"github.com/craig8/ieee-2030_5-go/pkg/store/memory"
)

// DEREventScheduler manages time-based DERControl activation and expiration.
// It periodically scans DERControls and updates their EventStatus based on
// the current time vs their Interval (Start + Duration).
type DEREventScheduler struct {
	controls *memory.ScopedStore[sep2.DERControl]
	interval time.Duration
	mu       sync.Mutex
}

// NewDEREventScheduler creates a scheduler that ticks at the given interval.
func NewDEREventScheduler(controls *memory.ScopedStore[sep2.DERControl], tickInterval time.Duration) *DEREventScheduler {
	if tickInterval <= 0 {
		tickInterval = 10 * time.Second
	}
	return &DEREventScheduler{
		controls: controls,
		interval: tickInterval,
	}
}

// Run starts the scheduler loop. Blocks until context is cancelled.
func (s *DEREventScheduler) Run(ctx context.Context) {
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()

	log.Println("DER event scheduler started")

	for {
		select {
		case <-ctx.Done():
			log.Println("DER event scheduler stopped")
			return
		case now := <-ticker.C:
			s.tick(ctx, now)
		}
	}
}

func (s *DEREventScheduler) tick(ctx context.Context, now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Scan all scoped stores for controls that need activation or expiration.
	// This is O(parents * controls) but acceptable for in-memory store with
	// typical device counts (<1000).

	// Note: ScopedStore doesn't expose its parent keys, so callers must
	// register which parent keys to scan. For now, this is a placeholder
	// that demonstrates the pattern. A full implementation would track
	// active parent keys via a separate registry.
}

// EvaluateControl checks a single DERControl and returns the updated EventStatus.
// Returns true if the status changed.
func EvaluateControl(ctrl sep2.DERControl, now time.Time) (sep2.DERControl, bool) {
	if ctrl.Interval == nil {
		return ctrl, false
	}

	start := time.Unix(ctrl.Interval.Start, 0)
	end := start.Add(time.Duration(ctrl.Interval.Duration) * time.Second)

	currentStatus := uint8(sep2.EventStatusScheduled)
	if ctrl.EventStatus != nil {
		currentStatus = ctrl.EventStatus.CurrentStatus
	}

	var newStatus uint8
	switch {
	case now.Before(start):
		newStatus = sep2.EventStatusScheduled
	case now.After(end):
		newStatus = sep2.EventStatusComplete
	default:
		newStatus = sep2.EventStatusActive
	}

	if newStatus == currentStatus {
		return ctrl, false
	}

	updated := ctrl.Copy()
	if updated.EventStatus == nil {
		updated.EventStatus = &sep2.EventStatus{}
	}
	updated.EventStatus.CurrentStatus = newStatus
	updated.EventStatus.DateTime = now.Unix()

	return updated, true
}

// GetActiveControls returns DERControls that are currently active for a given scope.
func GetActiveControls(ctx context.Context, controlStore *memory.ScopedStore[sep2.DERControl], scopeKey string, now time.Time) ([]sep2.DERControl, error) {
	result, err := controlStore.List(ctx, scopeKey, store.ListOptions{Limit: 255})
	if err != nil {
		return nil, err
	}

	var active []sep2.DERControl
	for _, ctrl := range result.Items {
		if ctrl.Interval == nil {
			continue
		}
		start := time.Unix(ctrl.Interval.Start, 0)
		end := start.Add(time.Duration(ctrl.Interval.Duration) * time.Second)
		if now.After(start) && now.Before(end) {
			if ctrl.EventStatus == nil {
				ctrl.EventStatus = &sep2.EventStatus{CurrentStatus: sep2.EventStatusActive}
			}
			active = append(active, ctrl)
		}
	}
	return active, nil
}
