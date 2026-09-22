package sep2capture

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"
)

// sseWriteDeadlineExtension and sseHeartbeatInterval are package vars, not
// consts, so a test in this package can shrink them (save, set, defer
// restore) to prove the deadline-extension mechanism against a short
// WriteTimeout in seconds rather than minutes. Production leaves them at
// these defaults: comfortably under the shortest WriteTimeout either
// consumer runs today (server-go's admin default is 30s; the bridge
// design targets 10s), with enough heartbeats before either expiry that
// one delayed write is never fatal.
var (
	sseWriteDeadlineExtension = 60 * time.Second
	sseHeartbeatInterval      = 10 * time.Second
)

// handleStream is the Live stream route (Q7 item 4): one SSE "data:" line
// per newly recorded exchange, "id:" set to the exchange id, and a
// heartbeat comment line on sseHeartbeatInterval so an idle stream still
// extends its own write deadline and survives any intermediary's idle
// timeout. It extends the connection's write deadline itself through
// http.ResponseController before every write, since the server's own
// WriteTimeout (set once, before the handler runs, per
// net/http/server.go) would otherwise cut the stream at that timeout
// regardless of how live it still is.
func (s *Store) handleStream(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming not supported")
		return
	}
	rc := http.NewResponseController(w)

	resumeAfter, resumeErr := parseResume(r)
	if resumeErr != nil {
		writeError(w, http.StatusBadRequest, "after or Last-Event-ID must be a non-negative integer")
		return
	}

	// Subscribe before reading any history: everything recorded from this
	// instant on is guaranteed to reach ch, so nothing recorded after this
	// line can fall in the gap between "read history" and "start
	// watching live" below (store_reader.go's add-then-publish order on
	// one goroutine is what makes this true).
	ch := s.Subscribe(r.Context())

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	_ = rc.SetWriteDeadline(time.Now().Add(sseWriteDeadlineExtension))
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	var replayed replaySet
	if resumeAfter != nil {
		history := s.summariesAfter(*resumeAfter)
		replayed = make(replaySet, len(history))
		for _, sum := range history {
			if !writeSSEEvent(w, rc, flusher, sum) {
				return
			}
			replayed.add(sum.ID)
		}
	}

	ticker := time.NewTicker(sseHeartbeatInterval)
	defer ticker.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case sum, open := <-ch:
			if !open {
				return
			}
			// A replayed id can still arrive here: publish (writeOne,
			// segment_writer.go) sends to every current subscriber
			// regardless of when it subscribed, so an exchange indexed
			// in the race window between Subscribe above and the
			// history read reaches both. Each id publishes at most
			// once in a Store's lifetime (duplicates are refused
			// before publish), so this check only ever needs to fire
			// for ids already in the one-time replay set, never again
			// after that.
			if replayed.has(sum.ID) {
				continue
			}
			if !writeSSEEvent(w, rc, flusher, sum) {
				return
			}
		case <-ticker.C:
			_ = rc.SetWriteDeadline(time.Now().Add(sseWriteDeadlineExtension))
			if _, err := fmt.Fprint(w, ": keep-alive\n\n"); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

// replaySet is the one-time membership test handleStream's live loop uses
// to filter its subscribe channel against the history it already
// replayed: an id present here has already been sent once and must never
// be sent again, however it arrives a second time.
type replaySet map[uint64]struct{}

func (r replaySet) add(id uint64) { r[id] = struct{}{} }

func (r replaySet) has(id uint64) bool {
	_, ok := r[id]
	return ok
}

// parseResume reads the resume point a reconnecting client names, per
// Q7 item 3: the standard Last-Event-ID header when present, else this
// route's own after= query parameter, else no resume (nil, a fresh
// stream). A value present but unparseable is reported to the caller
// rather than silently treated as "no resume".
func parseResume(r *http.Request) (*uint64, error) {
	v := r.Header.Get("Last-Event-ID")
	if v == "" {
		v = r.URL.Query().Get("after")
	}
	if v == "" {
		return nil, nil
	}
	id, err := strconv.ParseUint(v, 10, 64)
	if err != nil {
		return nil, err
	}
	return &id, nil
}

// writeSSEEvent writes one summary as an SSE event, extending the write
// deadline first (see handleStream's doc). It reports whether the write
// succeeded; the caller returns immediately on false; a write error means
// the reader is gone or has fallen far enough behind that the connection
// itself failed, not something to retry.
func writeSSEEvent(w http.ResponseWriter, rc *http.ResponseController, flusher http.Flusher, sum Summary) bool {
	_ = rc.SetWriteDeadline(time.Now().Add(sseWriteDeadlineExtension))
	body, err := json.Marshal(toSummaryJSON(sum))
	if err != nil {
		return true // a marshal failure is not a connection failure; skip this event and keep the stream open
	}
	if _, err := fmt.Fprintf(w, "id: %d\ndata: %s\n\n", sum.ID, body); err != nil {
		return false
	}
	flusher.Flush()
	return true
}
