package sep2capture

import (
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// perDirectionCap bounds stored bytes per direction per exchange
// (operator-approved 2026-09-21). Memory held per open connection would
// otherwise be bounded only by net/http's own limits (1 MiB headers,
// ReadTimeout for bodies), and one record could outgrow a segment once PR 3
// writes it to disk.
const perDirectionCap = 4 * 1024 * 1024

// growBuffer accumulates one direction's bytes up to perDirectionCap,
// tracking the true length so truncation is visible rather than silent.
type growBuffer struct {
	bytes     []byte
	trueLen   int64
	truncated bool
}

func (g *growBuffer) append(b []byte) {
	g.trueLen += int64(len(b))
	if g.truncated {
		return
	}
	room := perDirectionCap - len(g.bytes)
	if room <= 0 {
		g.truncated = true
		return
	}
	if len(b) > room {
		g.bytes = append(g.bytes, b[:room]...)
		g.truncated = true
		return
	}
	g.bytes = append(g.bytes, b...)
}

func (g *growBuffer) direction() Direction {
	return Direction{Bytes: g.bytes, TrueLen: g.trueLen, Truncated: g.truncated}
}

// building is the exchange currently open on a connection.
type building struct {
	id          uint64
	started     time.Time
	req, resp   growBuffer
	handlerRuns int
	lastErr     error
}

// connRecorder holds the per-connection state Attach's wrapped conn and
// ConnState hook both mutate: which exchange is open, the identity derived
// once at handshake, and any byte read early for the next exchange.
type connRecorder struct {
	rs         *recorderSet
	connID     uint64
	remoteAddr string
	clientLFDI string
	clientSFDI string

	mu           sync.Mutex
	current      *building
	pendingCarry []byte

	// finished carries each closed exchange, in closing order, to its own
	// goroutine (started by newConnRecorder) so Sink.Record never runs on
	// the connection's own goroutine (design Q5). It is sized generously
	// rather than bounded and non-blocking: PR 3's segment-log queue is the
	// real bounded, drop-counted replacement this PR does not build.
	// closeFinal closes it once the last exchange is sent, which is the
	// dispatch goroutine's exit path.
	finished chan Exchange
}

// newConnRecorder starts the per-connection recorder and its dispatch
// goroutine. Callers must eventually call closeFinal so that goroutine
// exits.
func newConnRecorder(rs *recorderSet, connID uint64, remoteAddr string) *connRecorder {
	rec := &connRecorder{
		rs:         rs,
		connID:     connID,
		remoteAddr: remoteAddr,
		finished:   make(chan Exchange, 256),
	}
	go rec.dispatch()
	return rec
}

func (rec *connRecorder) dispatch() {
	for ex := range rec.finished {
		rec.rs.sink.Record(ex)
	}
}

// recordInbound appends a completed Read's bytes to the open exchange.
//
// isPeek marks a Read for exactly one byte: net/http's connReader arms a
// background single-byte read to detect the next request's arrival before
// this exchange's ConnState transition fires (net/http/server.go,
// connReader.backgroundRead; armed via startBackgroundRead, called before
// the handler runs for a bodyless request). Nothing else on this path reads
// one byte at a time, so that byte is held back rather than attributed to
// the exchange that is still open: rollover moves it to the exchange that
// opens next, which is what it belongs to.
func (rec *connRecorder) recordInbound(b []byte, isPeek bool) {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if rec.current == nil {
		return
	}
	if isPeek {
		rec.pendingCarry = append(rec.pendingCarry, b...)
		return
	}
	if len(rec.pendingCarry) > 0 {
		// A further read on the still-open exchange shows the held byte
		// was not, after all, the pipelined peek: fold it back in first,
		// in the order it arrived.
		rec.current.req.append(rec.pendingCarry)
		rec.pendingCarry = nil
	}
	rec.current.req.append(b)
}

func (rec *connRecorder) recordOutbound(b []byte) {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if rec.current == nil {
		return
	}
	rec.current.resp.append(b)
}

func (rec *connRecorder) noteError(err error) {
	if err == nil {
		return
	}
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if rec.current != nil {
		rec.current.lastErr = err
	}
}

func (rec *connRecorder) markHandlerRan() {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if rec.current != nil {
		rec.current.handlerRuns++
	}
}

// open starts the first exchange on a new connection.
func (rec *connRecorder) open(id uint64) {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	rec.current = &building{id: id, started: time.Now()}
}

// rollover closes the current exchange at a StateIdle boundary and opens
// the next one, carrying over any byte the background peek already read.
func (rec *connRecorder) rollover(nextID uint64) {
	rec.mu.Lock()
	prev := rec.current
	carry := rec.pendingCarry
	rec.pendingCarry = nil
	next := &building{id: nextID, started: time.Now()}
	if len(carry) > 0 {
		next.req.append(carry)
	}
	rec.current = next
	rec.mu.Unlock()

	rec.finish(prev)
}

// closeFinal closes the last exchange on a connection at StateClosed or
// StateHijacked. Any pending carry byte has no next exchange to move to, so
// it is kept on the exchange that is actually closing rather than dropped.
func (rec *connRecorder) closeFinal() {
	rec.mu.Lock()
	b := rec.current
	rec.current = nil
	carry := rec.pendingCarry
	rec.pendingCarry = nil
	rec.mu.Unlock()

	if b != nil && len(carry) > 0 {
		b.req.append(carry)
	}
	rec.finish(b)
	close(rec.finished)
}

// finish marks one closed exchange and hands it to this connection's
// dispatch goroutine, which calls Sink.Record off the connection's own
// goroutine and in the order exchanges actually closed (design Q5: a slow
// or erroring sink must never delay or break the connection it came from).
func (rec *connRecorder) finish(b *building) {
	if b == nil {
		return
	}
	mark, errText := classify(b.handlerRuns, len(b.resp.bytes) > 0, b.lastErr)
	rec.finished <- Exchange{
		ID:          b.id,
		ConnID:      rec.connID,
		ClientLFDI:  rec.clientLFDI,
		ClientSFDI:  rec.clientSFDI,
		Started:     b.started,
		Ended:       time.Now(),
		Request:     b.req.direction(),
		Response:    b.resp.direction(),
		Mark:        mark,
		Error:       errText,
		HandlerRuns: b.handlerRuns,
	}
}

// classify applies the design's Q2 table. A timeout or a TLS-level error
// overrides the outcome regardless of whether the handler ran, since both
// mean the exchange did not finish cleanly; otherwise a handler that ran at
// all makes it MarkHandled (the malformed-chunked-body row stays "handled"
// even though the body was malformed), then a written-but-unhandled
// response is MarkRejectedBeforeHandler, and no response at all is
// MarkNoResponse.
func classify(handlerRuns int, wroteAny bool, err error) (Mark, string) {
	switch {
	case err != nil && isTimeout(err):
		return MarkIncomplete, err.Error()
	case err != nil && isTLSError(err):
		return MarkConnectionError, err.Error()
	case handlerRuns > 0:
		return MarkHandled, ""
	case wroteAny:
		return MarkRejectedBeforeHandler, ""
	case err != nil:
		return MarkNoResponse, err.Error()
	default:
		return MarkNoResponse, ""
	}
}

func isTimeout(err error) bool {
	ne, ok := err.(net.Error)
	return ok && ne.Timeout()
}

// isTLSError matches crypto/tls's and core's gotls fork's error strings,
// which both prefix every error "tls: " (crypto/tls/conn.go's
// RecordHeaderError.Error and the plain errors.New calls throughout both
// packages; verified in the vendored gotls tree, GOROOT/src/crypto/tls).
func isTLSError(err error) bool {
	return strings.HasPrefix(err.Error(), "tls: ")
}

// recorderSet is the state one Attach call shares between the listener (to
// open and close exchanges), the annotation middleware (to mark a handler
// ran), and the ConnState hook (to drive both). Exchange and connection IDs
// are unique for the lifetime of one recorderSet, not globally: two Attach
// calls on two listeners number their own exchanges independently.
type recorderSet struct {
	sink Sink

	mu     sync.Mutex
	byAddr map[string]*connRecorder

	nextExchangeID atomic.Uint64
	nextConnID     atomic.Uint64
}

func newRecorderSet(sink Sink) *recorderSet {
	return &recorderSet{sink: sink, byAddr: make(map[string]*connRecorder)}
}

// wrap creates the connRecorder for a newly accepted connection, deriving
// its client identity from ConnectionState if the conn already carries one
// (design Q1 rule 3: every exchange inherits it, including ones no handler
// saw).
func (rs *recorderSet) wrap(c net.Conn) *recordingConn {
	rec := newConnRecorder(rs, rs.nextConnID.Add(1), c.RemoteAddr().String())
	if lfdi, sfdi, ok := identityFrom(c); ok {
		rec.clientLFDI = lfdi
		rec.clientSFDI = sfdi
	}

	rs.mu.Lock()
	rs.byAddr[rec.remoteAddr] = rec
	rs.mu.Unlock()

	return &recordingConn{Conn: c, rec: rec}
}

func (rs *recorderSet) forget(rec *connRecorder) {
	rs.mu.Lock()
	if rs.byAddr[rec.remoteAddr] == rec {
		delete(rs.byAddr, rec.remoteAddr)
	}
	rs.mu.Unlock()
}

// byRemoteAddr finds the recorder for a live connection. RemoteAddr is
// unique among open connections on one listener (design Q1 rule 5); it is
// only ever ambiguous once a connection has closed and its port has been
// reused, and by then its recorder has already been forgotten.
func (rs *recorderSet) byRemoteAddr(addr string) *connRecorder {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	return rs.byAddr[addr]
}

// observe is Attach's ConnState hook. It opens the first exchange at
// StateNew, rolls over at StateIdle, and closes the last exchange at
// StateClosed or StateHijacked. Any net.Conn that is not one of ours (a
// hijacked connection's replacement, for instance) is ignored.
func (rs *recorderSet) observe(c net.Conn, state http.ConnState) {
	rc, ok := c.(*recordingConn)
	if !ok {
		return
	}
	switch state {
	case http.StateNew:
		rc.rec.open(rs.nextExchangeID.Add(1))
	case http.StateIdle:
		rc.rec.rollover(rs.nextExchangeID.Add(1))
	case http.StateClosed, http.StateHijacked:
		rc.rec.closeFinal()
		rs.forget(rc.rec)
	}
}
