package sep2capture

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
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

// sseSubscribedHook, when set by a test in this package, runs synchronously
// right after handleStream subscribes and before it writes any response
// bytes. It lets a test force events onto the new subscription (and force
// an overflow) before the stream's own goroutine ever reads from it,
// proving the close-on-drop path deterministically instead of racing the
// scheduler. Production never sets it.
var sseSubscribedHook func()

// replayHook, when set by a test in this package, runs synchronously right
// after history replay finishes (or is skipped, for a fresh connection)
// and before the live select loop starts. Combined with subscribeHook
// (store_reader.go), it lets a test record a genuinely new exchange at
// exactly the point where every replayed Seq is already in the replayed
// set, so the live loop's first receive is deterministic. Production
// never sets it.
var replayHook func()

// handleStream is the Live stream route (Q7 item 4): one SSE "data:" line
// per newly recorded exchange, "id:" set to "<epoch>-<seq>" (Store.epoch's
// doc, store.go; seq is the exchange's publish sequence, Summary.Seq, not
// its exchange id: see that field's doc in index.go), with a heartbeat
// comment line on sseHeartbeatInterval so an idle stream still extends its
// own write deadline and survives any intermediary's idle timeout. It
// extends the connection's write deadline itself through
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

	resume, resumeErr := parseResume(r, s.epoch)
	if resumeErr != nil {
		writeError(w, http.StatusBadRequest, "after or Last-Event-ID must be a non-negative integer")
		return
	}
	// A resume point above every Seq this Store has ever published cannot
	// be one: unlike /exchanges' after= (an exchange id), this route's
	// after= and Last-Event-ID name a Seq (handler.go's route doc), and
	// the two spaces diverge by every dropped, refused or failed record
	// (PR 620 review, MEDIUM: a client that mixed up the two got a 200
	// and an empty replay, indistinguishable from "nothing was missed").
	// maxPublishSeq only grows, so a value valid here stays valid by the
	// time summariesAfter runs. A resume stamped with a different
	// incarnation's epoch skips this check: a restart resets Seq to 1, so
	// the same comparison against the new, small maxPublishSeq would
	// either 400 a value that is really just old, or silently pass one
	// that now coincides with this run's own early Seqs (PR 620 review,
	// MEDIUM: measured both ways against a fresh Store). It is resolved
	// to a full replay below instead, which is safe either way.
	if resume != nil && !resume.foreignEpoch && resume.seq > s.maxPublishSeq.Load() {
		writeError(w, http.StatusBadRequest, "after or Last-Event-ID names a publish sequence this stream has never issued")
		return
	}

	// Subscribe before reading any history: everything recorded from this
	// instant on is guaranteed to reach ch, so nothing recorded after this
	// line can fall in the gap between "read history" and "start
	// watching live" below (store_reader.go's add-then-publish order on
	// one goroutine is what makes this true).
	ch := s.Subscribe(r.Context(), func() {
		// Unblocks a Write already in flight to this connection the
		// instant the subscription ends, rather than leaving it to run
		// out whatever write deadline the last successful write set
		// (subscription.forceExpire's doc, store_reader.go).
		if err := rc.SetWriteDeadline(time.Now()); err != nil {
			s.logSSEDeadlineErr(err)
		}
	})
	if sseSubscribedHook != nil {
		sseSubscribedHook()
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	if err := rc.SetWriteDeadline(time.Now().Add(sseWriteDeadlineExtension)); err != nil {
		s.logSSEDeadlineErr(err)
	}
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	var replayed replaySet
	if resume != nil {
		afterSeq := resume.seq
		if resume.foreignEpoch {
			// Recognised as a previous incarnation's resume point: this
			// Store never issued it, so there is no position within it
			// to resume from. Everything this incarnation has indexed
			// so far is the only replay that can never silently miss
			// its own early events (handleStream's doc above).
			afterSeq = 0
		}
		history := s.summariesAfter(afterSeq)
		replayed = make(replaySet, len(history))
		for _, sum := range history {
			if !s.writeSSEEvent(w, rc, flusher, sum) {
				return
			}
			replayed.add(sum.Seq)
		}
	}
	if replayHook != nil {
		replayHook()
	}

	ticker := time.NewTicker(sseHeartbeatInterval)
	defer ticker.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case sum, open := <-ch:
			if !open {
				// Either the reader disconnected (ctx.Done, handled
				// above and racing harmlessly with this case) or
				// publish dropped this subscription for falling behind
				// (segment_writer.go). Either way the response ends
				// here, so EventSource reconnects and resumes from the
				// last id: it saw rather than the stream silently
				// continuing with a gap already in it.
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
			if replayed.has(sum.Seq) {
				continue
			}
			if !s.writeSSEEvent(w, rc, flusher, sum) {
				return
			}
		case <-ticker.C:
			if err := rc.SetWriteDeadline(time.Now().Add(sseWriteDeadlineExtension)); err != nil {
				s.logSSEDeadlineErr(err)
			}
			if _, err := fmt.Fprint(w, ": keep-alive\n\n"); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

// replaySet is the one-time membership test handleStream's live loop uses
// to filter its subscribe channel against the history it already
// replayed, keyed by Seq (see summariesAfter and Summary.Seq's doc): a Seq
// present here has already been sent once and must never be sent again,
// however it arrives a second time.
type replaySet map[uint64]struct{}

func (r replaySet) add(seq uint64) { r[seq] = struct{}{} }

func (r replaySet) has(seq uint64) bool {
	_, ok := r[seq]
	return ok
}

// resumePoint is what parseResume decodes from Last-Event-ID or after=.
// seq is a publish sequence within THIS Store's own incarnation; when
// foreignEpoch is true, the value was recognised as stamped with a
// different incarnation's epoch (Store.epoch's doc, store.go), and seq is
// meaningless: the position it names does not exist in this incarnation's
// Seq space, not even coincidentally (a fresh Store's own Seq counter also
// starts at 1 on every restart, so a small foreign value could otherwise
// look like a valid, and wrong, resume point here).
type resumePoint struct {
	seq          uint64
	foreignEpoch bool
}

// errMalformedResume is returned by parseResume for a Last-Event-ID or
// after= value that parses as neither a bare uint64 nor "<epoch>-<seq>".
var errMalformedResume = errors.New("sep2capture: malformed resume value")

// parseResume reads the resume point a reconnecting client names, per Q7
// item 3: the standard Last-Event-ID header when present, else this
// route's own after= query parameter, else no resume (nil, a fresh
// stream). Both name a Seq (the value handleStream sent as "id:"), not an
// exchange id: see Summary.Seq's doc in index.go for why the two differ. A
// value present but unparseable is reported to the caller rather than
// silently treated as "no resume".
//
// The value handleStream itself sends is always "<epoch>-<seq>"
// (Store.epoch's doc, store.go, and this Store's own epoch, the second
// argument here): a client that echoes it back, as EventSource does via
// Last-Event-ID, is compared against epoch by exact match. A bare uint64
// is still accepted as a Seq within THIS incarnation, for a caller that
// built its own resume point from a summary's "seq" JSON field (the
// /exchanges handoff, handler.go's route doc) rather than an "id:" line.
func parseResume(r *http.Request, epoch uint64) (*resumePoint, error) {
	v := r.Header.Get("Last-Event-ID")
	if v == "" {
		v = r.URL.Query().Get("after")
	}
	if v == "" {
		return nil, nil
	}
	if dash := strings.LastIndexByte(v, '-'); dash >= 0 {
		gotEpoch, err1 := strconv.ParseUint(v[:dash], 10, 64)
		seq, err2 := strconv.ParseUint(v[dash+1:], 10, 64)
		if err1 != nil || err2 != nil {
			return nil, errMalformedResume
		}
		if gotEpoch != epoch {
			return &resumePoint{foreignEpoch: true}, nil
		}
		return &resumePoint{seq: seq}, nil
	}
	seq, err := strconv.ParseUint(v, 10, 64)
	if err != nil {
		return nil, err
	}
	return &resumePoint{seq: seq}, nil
}

// writeSSEEvent writes one summary as an SSE event, extending the write
// deadline first (see handleStream's doc). It reports whether the write
// succeeded; the caller returns immediately on false; a write error means
// the reader is gone or has fallen far enough behind that the connection
// itself failed, not something to retry. "id:" carries "<epoch>-<sum.Seq>"
// (Store.epoch's doc, store.go): see Summary.Seq's doc in index.go for why
// the stream resumes on Seq instead of the exchange id carried in the
// JSON body's own "id" field.
func (s *Store) writeSSEEvent(w http.ResponseWriter, rc *http.ResponseController, flusher http.Flusher, sum Summary) bool {
	if err := rc.SetWriteDeadline(time.Now().Add(sseWriteDeadlineExtension)); err != nil {
		s.logSSEDeadlineErr(err)
	}
	body, err := json.Marshal(toSummaryJSON(sum))
	if err != nil {
		return true // a marshal failure is not a connection failure; skip this event and keep the stream open
	}
	if _, err := fmt.Fprintf(w, "id: %d-%d\ndata: %s\n\n", s.epoch, sum.Seq, body); err != nil {
		return false
	}
	flusher.Flush()
	return true
}

// logSSEDeadlineErr logs a failed write-deadline extension at most once a
// minute (mirrors logWriteErr's cadence; kept as separate state so a
// ResponseWriter wrapper that never supports deadlines does not also
// throttle unrelated disk write-error logging). A wrapper of that shape
// still ends the stream at the server's own WriteTimeout, same as before
// (PR 620 review, MEDIUM: the failure was previously discarded via `_ =`
// at every call site), but now with a line in the error log instead of
// nothing.
func (s *Store) logSSEDeadlineErr(err error) {
	s.sseDeadlineErrLogMu.Lock()
	defer s.sseDeadlineErrLogMu.Unlock()
	if time.Since(s.sseDeadlineErrLogAt) < time.Minute {
		return
	}
	s.sseDeadlineErrLogAt = time.Now()
	s.errorLog.Printf("sep2capture: SSE write-deadline extension failed: %v", err)
}
