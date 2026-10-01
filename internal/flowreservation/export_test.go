package flowreservation

import (
	"sync"
	"time"
)

// PendingTimers is the number of requests the queue still holds a deadline
// timer for, so a test can tell a request left the queue.
func (q *Queue) PendingTimers() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.timers)
}

// TimerRecord is one timer a recording queue was asked to arm.
type TimerRecord struct {
	Delay time.Duration
	fire  func()

	mu      sync.Mutex
	stopped bool
}

// Stop marks the timer stopped, as a real timer's Stop would.
func (r *TimerRecord) Stop() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	was := !r.stopped
	r.stopped = true
	return was
}

// Fire runs the timer's callback on the caller's goroutine.
func (r *TimerRecord) Fire() { r.fire() }

// Timers lists the timers a queue armed since RecordTimers.
type Timers struct {
	mu   sync.Mutex
	recs []*TimerRecord
}

// Live returns the armed timers that were not stopped, oldest first.
func (ts *Timers) Live() []*TimerRecord {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	var live []*TimerRecord
	for _, r := range ts.recs {
		r.mu.Lock()
		if !r.stopped {
			live = append(live, r)
		}
		r.mu.Unlock()
	}
	return live
}

// RecordTimers replaces the queue's clock with one that never fires on its
// own: every timer is recorded with the delay it was armed for, and a test
// fires it by hand, so no test waits out a real hold.
func (q *Queue) RecordTimers() *Timers {
	ts := &Timers{}
	q.after = func(d time.Duration, f func()) timer {
		r := &TimerRecord{Delay: d, fire: f}
		ts.mu.Lock()
		ts.recs = append(ts.recs, r)
		ts.mu.Unlock()
		return r
	}
	return ts
}
