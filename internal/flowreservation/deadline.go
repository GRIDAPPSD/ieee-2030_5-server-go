package flowreservation

import (
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
)

// DeadlineAt is the Unix second at which the deadline fallback decides a
// request nobody answered: cfg's bound after the request was created, capped
// at the requested start. It uses the same arithmetic as Queue.Submit, so a
// reader shows the instant the timer fires.
func DeadlineAt(cfg Config, frq sep2.FlowReservationRequest) int64 {
	q := Queue{cfg: cfg.withDefaults()}
	return frq.CreationTime + int64(q.deadlineDelay(frq, frq.CreationTime)/1e9)
}

// EffectiveDeadline is the bound the queue runs under: Deadline, or
// DefaultDeadline when it is unset.
func (c Config) EffectiveDeadline() time.Duration {
	return c.withDefaults().Deadline
}
