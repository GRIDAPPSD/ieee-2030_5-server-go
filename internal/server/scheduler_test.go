package server

import (
	"context"
	"testing"
	"time"

	"github.com/craig8/ieee-2030_5-go/pkg/sep2"
	"github.com/craig8/ieee-2030_5-go/pkg/store/memory"
)

func TestEvaluateControlScheduled(t *testing.T) {
	ctrl := sep2.DERControl{
		DERControlBase: &sep2.DERControlBase{},
	}
	ctrl.Interval = &sep2.DateTimeInterval{
		Start:    time.Now().Add(1 * time.Hour).Unix(),
		Duration: 3600,
	}

	updated, changed := EvaluateControl(ctrl, time.Now())
	if changed {
		t.Error("future control should not change status")
	}
	_ = updated
}

func TestEvaluateControlActivation(t *testing.T) {
	start := time.Now().Add(-5 * time.Minute)
	ctrl := sep2.DERControl{
		DERControlBase: &sep2.DERControlBase{},
	}
	ctrl.Interval = &sep2.DateTimeInterval{
		Start:    start.Unix(),
		Duration: 3600,
	}
	ctrl.EventStatus = &sep2.EventStatus{CurrentStatus: sep2.EventStatusScheduled}

	updated, changed := EvaluateControl(ctrl, time.Now())
	if !changed {
		t.Error("control should activate (within interval)")
	}
	if updated.EventStatus.CurrentStatus != sep2.EventStatusActive {
		t.Errorf("status = %d, want Active(%d)", updated.EventStatus.CurrentStatus, sep2.EventStatusActive)
	}
}

func TestEvaluateControlExpiration(t *testing.T) {
	start := time.Now().Add(-2 * time.Hour)
	ctrl := sep2.DERControl{
		DERControlBase: &sep2.DERControlBase{},
	}
	ctrl.Interval = &sep2.DateTimeInterval{
		Start:    start.Unix(),
		Duration: 3600, // 1 hour, already passed
	}
	ctrl.EventStatus = &sep2.EventStatus{CurrentStatus: sep2.EventStatusActive}

	updated, changed := EvaluateControl(ctrl, time.Now())
	if !changed {
		t.Error("control should expire (past interval)")
	}
	if updated.EventStatus.CurrentStatus != sep2.EventStatusComplete {
		t.Errorf("status = %d, want Complete(%d)", updated.EventStatus.CurrentStatus, sep2.EventStatusComplete)
	}
}

func TestEvaluateControlNoInterval(t *testing.T) {
	ctrl := sep2.DERControl{}
	_, changed := EvaluateControl(ctrl, time.Now())
	if changed {
		t.Error("no interval should not change")
	}
}

func TestGetActiveControls(t *testing.T) {
	store := memory.NewScopedStore[sep2.DERControl]()
	ctx := context.Background()
	now := time.Now()

	// Active control (started 5 min ago, lasts 1 hour)
	activeCtrl := sep2.DERControl{
		DERControlBase: &sep2.DERControlBase{},
	}
	activeCtrl.Href = "/derc/1"
	activeCtrl.Interval = &sep2.DateTimeInterval{
		Start:    now.Add(-5 * time.Minute).Unix(),
		Duration: 3600,
	}

	// Expired control (started 2 hours ago, lasted 1 hour)
	expiredCtrl := sep2.DERControl{
		DERControlBase: &sep2.DERControlBase{},
	}
	expiredCtrl.Href = "/derc/2"
	expiredCtrl.Interval = &sep2.DateTimeInterval{
		Start:    now.Add(-2 * time.Hour).Unix(),
		Duration: 3600,
	}

	// Future control
	futureCtrl := sep2.DERControl{
		DERControlBase: &sep2.DERControlBase{},
	}
	futureCtrl.Href = "/derc/3"
	futureCtrl.Interval = &sep2.DateTimeInterval{
		Start:    now.Add(1 * time.Hour).Unix(),
		Duration: 3600,
	}

	store.Create(ctx, "scope1", "1", activeCtrl)
	store.Create(ctx, "scope1", "2", expiredCtrl)
	store.Create(ctx, "scope1", "3", futureCtrl)

	active, err := GetActiveControls(ctx, store, "scope1", now)
	if err != nil {
		t.Fatal(err)
	}

	if len(active) != 1 {
		t.Fatalf("active count = %d, want 1", len(active))
	}
	if active[0].Href != "/derc/1" {
		t.Errorf("active control href = %q, want /derc/1", active[0].Href)
	}
}
