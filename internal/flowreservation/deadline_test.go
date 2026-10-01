package flowreservation_test

import (
	"testing"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/flowreservation"
)

func TestDeadlineAt(t *testing.T) {
	t.Parallel()
	const created = 1790000000
	req := func(start *int64) sep2.FlowReservationRequest {
		frq := sep2.FlowReservationRequest{CreationTime: created}
		if start != nil {
			frq.IntervalRequested = &sep2.DateTimeInterval{Start: *start, Duration: 600}
		}
		return frq
	}
	at := func(s int64) *int64 { return &s }
	tests := []struct {
		name string
		cfg  flowreservation.Config
		frq  sep2.FlowReservationRequest
		want int64
	}{
		{"default bound, no interval", flowreservation.Config{}, req(nil), created + 300},
		{"configured bound", flowreservation.Config{Deadline: 90 * time.Second}, req(at(created + 3600)), created + 90},
		{"capped at the requested start", flowreservation.Config{}, req(at(created + 120)), created + 120},
		{"a start already past is due at creation", flowreservation.Config{}, req(at(created - 50)), created},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := flowreservation.DeadlineAt(tc.cfg, tc.frq); got != tc.want {
				t.Errorf("DeadlineAt = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestEffectiveDeadline(t *testing.T) {
	t.Parallel()
	if got := (flowreservation.Config{}).EffectiveDeadline(); got != flowreservation.DefaultDeadline {
		t.Errorf("unset = %v, want the default %v", got, flowreservation.DefaultDeadline)
	}
	if got := (flowreservation.Config{Deadline: 42 * time.Second}).EffectiveDeadline(); got != 42*time.Second {
		t.Errorf("set = %v, want 42s", got)
	}
}
