// Package ssedeadline is the one owner of extending a long-lived stream's
// write deadline. A listener's WriteTimeout is set once before the handler
// runs and would otherwise cut the stream, so each stream pushes the
// deadline out before every write.
package ssedeadline

import (
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/logsafe"
)

// Extend sets rc's write deadline to d from now. A failure (a
// ResponseWriter that does not support deadlines) is handed to onErr, which
// owns the logging cadence; the stream then ends at the listener's own
// WriteTimeout, as it would have without the call.
func Extend(rc *http.ResponseController, d time.Duration, onErr func(error)) {
	if err := rc.SetWriteDeadline(time.Now().Add(d)); err != nil {
		onErr(err)
	}
}

// Stream is one SSE response's write-deadline lifecycle: each write is
// preceded by Extend, and Watch cuts a write blocked on a reader that
// stopped reading once the server is shutting down.
type Stream struct {
	rc    *http.ResponseController
	label string
	write time.Duration
	grace time.Duration

	mu      sync.Mutex
	closing bool
	logged  bool
}

// NewStream returns the lifecycle for w. write is the deadline set before
// each write and grace the one used once Watch sees done close. label
// prefixes the failure log line.
func NewStream(w http.ResponseWriter, label string, write, grace time.Duration) *Stream {
	return &Stream{rc: http.NewResponseController(w), label: label, write: write, grace: grace}
}

// Extend sets the next write's deadline. Under mu so it cannot undo the
// watcher's shorter deadline set from another goroutine. A failure is
// logged once per stream rather than per write; without the extension the
// listener's WriteTimeout ends the stream.
func (s *Stream) Extend() {
	s.mu.Lock()
	defer s.mu.Unlock()
	d := s.write
	if s.closing {
		d = s.grace
	}
	Extend(s.rc, d, func(err error) {
		if !s.logged {
			s.logged = true
			log.Printf("%s: stream write-deadline extension failed: %s", logsafe.Str(s.label), logsafe.Str(err.Error()))
		}
	})
}

// Watch pulls the write deadline in to the grace once done closes, so a
// write already blocked on a stalled reader cannot hold Shutdown for the
// full write deadline. A nil done watches nothing. The returned stop ends
// the watcher and waits for it.
func (s *Stream) Watch(done <-chan struct{}) (stop func()) {
	if done == nil {
		return func() {}
	}
	quit := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		select {
		case <-done:
			s.mu.Lock()
			s.closing = true
			s.mu.Unlock()
			s.Extend()
		case <-quit:
		}
	}()
	return func() {
		close(quit)
		wg.Wait()
	}
}
