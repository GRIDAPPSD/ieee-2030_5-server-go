package flowreservation

// PendingTimers is the number of requests the queue still holds a deadline
// timer for, so a test can tell a request left the queue.
func (q *Queue) PendingTimers() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.timers)
}
