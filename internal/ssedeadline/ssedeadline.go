// Package ssedeadline is the one owner of extending a long-lived stream's
// write deadline. A listener's WriteTimeout is set once before the handler
// runs and would otherwise cut the stream, so each stream pushes the
// deadline out before every write.
package ssedeadline

import (
	"net/http"
	"time"
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
