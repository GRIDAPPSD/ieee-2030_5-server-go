package adminplane

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/auth"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2admin"
)

// The write deadline is extended before every write and a heartbeat is
// written on an idle stream, the traffic-capture stream's pattern
// (pkg/sep2capture/handler_sse.go): the listener's WriteTimeout, set once
// before the handler runs, would otherwise cut the stream. Vars so a test
// can shrink them.
var (
	panelStreamWriteDeadline     = 60 * time.Second
	panelStreamHeartbeatInterval = 10 * time.Second
)

// Stream bounds. The queue holds what a source sent and the reader has not
// yet been written; it is sized above a replay of a few hundred events, and
// a source that overruns it is a reader too slow to keep up.
const (
	maxOpenPanelStreams    = 8
	maxStreamQueueEvents   = 512
	maxStreamQueueBytes    = 4 << 20
	maxStreamQueryBytes    = 4096
	streamParamRefusal     = "invalid stream parameter"
	streamReaderTooSlow    = "stream closed: reader too slow"
	streamInvalidEventText = "stream closed: panel sent an invalid event"
)

type streamEventJSON struct {
	ID   string `json:"id,omitempty"`
	Time string `json:"time"`
	Kind string `json:"kind"`
	Text string `json:"text"`
}

// streamQueue is the non-blocking hand-off between a panel's source and
// the one handler writing its stream. A full queue ends the stream instead
// of blocking the source, so a slow browser never stalls the embedder.
type streamQueue struct {
	mu     sync.Mutex
	frames [][]byte
	bytes  int
	lastID uint64
	end    string // final status text once the stream is over; "" while open
	closed bool   // the handler has returned
	ready  chan struct{}
}

func newStreamQueue(after uint64) *streamQueue {
	return &streamQueue{lastID: after, ready: make(chan struct{}, 1)}
}

func (q *streamQueue) send(panelID string) sep2admin.StreamSendFunc {
	return func(ev sep2admin.StreamEvent) bool {
		q.mu.Lock()
		defer q.mu.Unlock()
		if q.closed || q.end != "" {
			return false
		}
		if ev.ID != 0 && ev.ID <= q.lastID {
			return true
		}
		frame, err := encodeStreamEvent(ev)
		if err != nil {
			log.Printf("admin: panel %q: stream event refused: %v", panelID, err)
			q.finishLocked(streamInvalidEventText)
			return false
		}
		if len(q.frames) >= maxStreamQueueEvents || q.bytes+len(frame) > maxStreamQueueBytes {
			q.finishLocked(streamReaderTooSlow)
			return false
		}
		q.frames = append(q.frames, frame)
		q.bytes += len(frame)
		q.lastID = ev.ID
		q.signal()
		return true
	}
}

// finishLocked drops whatever is queued: the reader resumes from the last
// event it was actually written, so nothing is lost, only re-asked for.
func (q *streamQueue) finishLocked(text string) {
	q.end = text
	q.frames = nil
	q.bytes = 0
	q.signal()
}

func (q *streamQueue) signal() {
	select {
	case q.ready <- struct{}{}:
	default:
	}
}

func (q *streamQueue) take() ([][]byte, string) {
	q.mu.Lock()
	defer q.mu.Unlock()
	frames, end := q.frames, q.end
	q.frames, q.bytes = nil, 0
	return frames, end
}

func (q *streamQueue) close() {
	q.mu.Lock()
	q.closed = true
	q.frames = nil
	q.mu.Unlock()
}

func encodeStreamEvent(ev sep2admin.StreamEvent) ([]byte, error) {
	switch {
	case ev.ID == 0:
		return nil, errors.New("event ID is 0")
	case ev.Time.IsZero():
		return nil, errors.New("event time is zero")
	case ev.Kind != sep2admin.StreamMessage && ev.Kind != sep2admin.StreamStatus:
		return nil, errors.New("unknown event kind")
	case !utf8.ValidString(ev.Text):
		return nil, errors.New("event text is not valid UTF-8")
	}
	id := strconv.FormatUint(ev.ID, 10)
	data, err := json.Marshal(streamEventJSON{ID: id, Time: ev.Time.UTC().Format(time.RFC3339Nano), Kind: string(ev.Kind), Text: ev.Text})
	if err != nil {
		return nil, err
	}
	if len(data) > sep2admin.MaxStreamEventBytes {
		return nil, fmt.Errorf("event is %d encoded bytes, over the cap of %d", len(data), sep2admin.MaxStreamEventBytes)
	}
	return []byte("id: " + id + "\ndata: " + string(data) + "\n\n"), nil
}

// statusFrame is the plane's own final event. It carries no id, so a
// client's Last-Event-ID stays on the last event the source sent.
func statusFrame(text string) []byte {
	data, _ := json.Marshal(streamEventJSON{Time: time.Now().UTC().Format(time.RFC3339Nano), Kind: string(sep2admin.StreamStatus), Text: text}) // a struct of strings always encodes
	return []byte("data: " + string(data) + "\n\n")
}

// parseStreamParam reads the one param value. Any refusal is a bare error,
// so no requested value reaches a response or a log line.
func parseStreamParam(p *sep2admin.Stream, rawQuery string) (string, error) {
	if len(rawQuery) > maxStreamQueryBytes {
		return "", sep2admin.ErrInvalidStreamParam
	}
	q, err := url.ParseQuery(rawQuery)
	if err != nil || len(q) != 1 || len(q["param"]) != 1 {
		return "", sep2admin.ErrInvalidStreamParam
	}
	v := q["param"][0]
	if err := p.Param.Validate(v); err != nil {
		return "", err
	}
	return v, nil
}

// handleStream answers GET /api/ui/panels/{id}/stream?param=<text>. The
// parameter, the credential kind, Last-Event-ID and the open-stream cap
// are all checked before the panel's Open runs.
func (ps *panelSet) handleStream() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p, ok := ps.lookup(r.PathValue("id"))
		if !ok || p.Stream == nil {
			writePanelError(w, http.StatusNotFound, "no such panel")
			return
		}
		// A ticket travels in the URL and is one-time; a stream reconnects
		// on its own, so it authenticates with the session cookie (which
		// EventSource sends) or a Bearer key.
		if auth.AdmittedViaTicket(r) {
			writePanelError(w, http.StatusUnauthorized, "panel streams do not accept a ticket")
			return
		}
		param, err := parseStreamParam(p.Stream, r.URL.RawQuery)
		if err != nil {
			writePanelError(w, http.StatusBadRequest, streamParamRefusal)
			return
		}
		var after uint64
		if v := r.Header.Get("Last-Event-ID"); v != "" {
			if after, err = strconv.ParseUint(v, 10, 64); err != nil {
				writePanelError(w, http.StatusBadRequest, "Last-Event-ID must be a non-negative integer")
				return
			}
		}
		flusher, ok := w.(http.Flusher)
		if !ok {
			writePanelError(w, http.StatusInternalServerError, "streaming not supported")
			return
		}
		if ps.openStreams.Add(1) > maxOpenPanelStreams {
			ps.openStreams.Add(-1)
			writePanelError(w, http.StatusServiceUnavailable, "too many open panel streams")
			return
		}
		defer ps.openStreams.Add(-1)

		ctx, cancel := context.WithCancel(r.Context())
		defer cancel()
		q := newStreamQueue(after)
		defer q.close()
		if !ps.openStream(ctx, w, r, p, sep2admin.StreamRequest{Param: param, After: after}, q) {
			return
		}
		ps.serveStream(w, r, flusher, p.ID, q)
	}
}

// openStream runs Open under the panel timeout and panic recovery that
// View gets, and answers the failure itself, returning false. ctx outlives
// the call: it is the stream's own lifetime.
func (ps *panelSet) openStream(ctx context.Context, w http.ResponseWriter, r *http.Request, p sep2admin.Panel, req sep2admin.StreamRequest, q *streamQueue) bool {
	open := p.Stream.Open
	p.View = func(context.Context) (sep2admin.Descriptor, error) {
		return sep2admin.Descriptor{}, open(ctx, req, q.send(p.ID))
	}
	_, err := sep2admin.InvokeView(r.Context(), p, ps.timeout)
	switch {
	case err == nil:
		return true
	case errors.Is(err, sep2admin.ErrViewTimedOut), errors.Is(err, sep2admin.ErrViewNotInvoked):
		log.Printf("admin: panel %q: stream open: %v", p.ID, err)
		writePanelError(w, http.StatusGatewayTimeout, "panel did not answer in time")
	case errors.Is(err, sep2admin.ErrViewCanceled):
		writePanelError(w, http.StatusServiceUnavailable, "request canceled")
	default:
		log.Printf("admin: panel %q: stream open: %v", p.ID, err)
		writePanelError(w, http.StatusInternalServerError, "panel failed")
	}
	return false
}

func (ps *panelSet) serveStream(w http.ResponseWriter, r *http.Request, flusher http.Flusher, id string, q *streamQueue) {
	rc := http.NewResponseController(w)
	var deadlineLogged bool
	write := func(b []byte) bool {
		if err := rc.SetWriteDeadline(time.Now().Add(panelStreamWriteDeadline)); err != nil && !deadlineLogged {
			// Without the extension the listener's WriteTimeout ends the
			// stream; logged once per stream rather than per write.
			deadlineLogged = true
			log.Printf("admin: panel %q: stream write-deadline extension failed: %v", id, err)
		}
		if _, err := w.Write(b); err != nil {
			return false
		}
		flusher.Flush()
		return true
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	if err := rc.SetWriteDeadline(time.Now().Add(panelStreamWriteDeadline)); err != nil {
		deadlineLogged = true
		log.Printf("admin: panel %q: stream write-deadline extension failed: %v", id, err)
	}
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	ticker := time.NewTicker(panelStreamHeartbeatInterval)
	defer ticker.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-q.ready:
			frames, end := q.take()
			for _, f := range frames {
				if !write(f) {
					return
				}
			}
			if end != "" {
				write(statusFrame(end))
				return
			}
		case <-ticker.C:
			if !write([]byte(": keep-alive\n\n")) {
				return
			}
		}
	}
}
