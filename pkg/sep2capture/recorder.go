package sep2capture

import (
	"errors"
	"log"
	"net"
	"net/http"
	"runtime/debug"
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
}

// newConnRecorder starts the per-connection recorder. Every exchange it
// finishes hands off to rs's own shared dispatch goroutine (see
// recorderSet.dispatch); this type starts nothing of its own.
func newConnRecorder(rs *recorderSet, connID uint64, remoteAddr string) *connRecorder {
	return &connRecorder{
		rs:         rs,
		connID:     connID,
		remoteAddr: remoteAddr,
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
//
// A further non-peek read never arrives on the still-open exchange while a
// peek byte is pending: GOROOT net/http/server.go's connReader.Read serves
// a buffered peek byte (cr.hasByte) directly from its own cr.byteBuf and
// returns without ever calling this connection's Read again, and
// connReader.startBackgroundRead refuses to arm a second background read
// while cr.hasByte is still set. So there is no fold-back case to handle
// here: coverage re-review at 65d5d5c found the branch that once did this
// unreachable (item 4, round 3), and this comment is why.
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
}

// finish marks one closed exchange and hands it to rs's shared dispatch
// goroutine, which calls Sink.Record off every connection's own goroutine
// and in the order exchanges are handed off: a slow or erroring sink must
// never delay or break the connection it came from.
//
// An exchange with no bytes in either direction is dropped rather than
// handed off at all: it is what a keep-alive client closing while idle
// leaves behind (the rollover at StateIdle opens a next exchange that never
// receives anything before StateClosed), and recording it would show every
// normal client as a failing one.
//
// The hand-off itself never blocks. rs.finished is one bounded queue shared
// by every connection this recorderSet owns, not one per connection (item
// 2, round 3: a per-connection queue let a hung Sink leak one dispatch
// goroutine per connection, unbounded in the number of connections, with
// nothing counted in Dropped until each connection's own queue happened to
// fill). Once the shared queue is full, the exchange is dropped and counted
// in rs.dropped instead of stalling this connection's own goroutine, which
// is also the goroutine net/http uses to read from and write to the client.
func (rec *connRecorder) finish(b *building) {
	if b == nil {
		return
	}
	if b.req.trueLen == 0 && b.resp.trueLen == 0 {
		return
	}
	mark, errText := classify(b.handlerRuns, len(b.resp.bytes) > 0, b.lastErr)
	ex := Exchange{
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
	select {
	case rec.rs.finished <- ex:
	default:
		rec.rs.dropped.Add(1)
	}
}

// classify sorts a closed exchange into a Mark. A read or write deadline
// always makes it MarkIncomplete, whatever else happened. Otherwise, any
// other error that arrived after the handler ran or a response was written
// makes it MarkConnectionError: a reset, a corrupt or unauthenticated TLS
// record, and any other transport-level failure all count, not only ones
// whose text happens to start "tls: ". No failure recorded here is ever
// reclassified as clean. With no error, a handler that ran at all makes it
// MarkHandled (a malformed chunked request body stays "handled" this way,
// since the handler still ran and answered: that failure is a parse error
// inside net/http, never surfaced as a Read error on this connection), then
// a written-but-unhandled response is MarkRejectedBeforeHandler, and no
// response at all is MarkNoResponse.
func classify(handlerRuns int, wroteAny bool, err error) (Mark, string) {
	switch {
	case err != nil && isTimeout(err):
		return MarkIncomplete, err.Error()
	case err != nil && (handlerRuns > 0 || wroteAny):
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
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}

// sinkQueueCapacity bounds how many closed exchanges can wait for Sink.Record
// across the whole recorderSet, not per connection (item 2, round 3): see
// finish's doc for why a shared bound replaced a per-connection one.
const sinkQueueCapacity = 256

// recorderSet is the state one Attach call shares between the listener (to
// open and close exchanges), the annotation middleware (to mark a handler
// ran), and the ConnState hook (to drive both). Exchange and connection IDs
// are unique for the lifetime of one recorderSet, not globally: two Attach
// calls on two listeners number their own exchanges independently.
type recorderSet struct {
	sink     Sink
	errorLog *log.Logger

	mu     sync.Mutex
	byAddr map[string]*connRecorder

	nextExchangeID atomic.Uint64
	nextConnID     atomic.Uint64

	// dropped counts exchanges that never reached Sink.Record: the shared
	// queue was full, or Record itself panicked. See Dropped.
	dropped atomic.Uint64

	// finished carries every closed exchange, from every connection this
	// recorderSet owns, to the single dispatch goroutine started below, so
	// Sink.Record never runs on a connection's own goroutine and the
	// goroutine count never grows with the number of connections.
	finished chan Exchange
	done     chan struct{}
	stopOnce sync.Once
	wg       sync.WaitGroup
}

func newRecorderSet(sink Sink, errorLog *log.Logger) *recorderSet {
	if errorLog == nil {
		errorLog = log.Default()
	}
	rs := &recorderSet{
		sink:     sink,
		errorLog: errorLog,
		byAddr:   make(map[string]*connRecorder),
		finished: make(chan Exchange, sinkQueueCapacity),
		done:     make(chan struct{}),
	}
	rs.wg.Add(1)
	go rs.dispatch()
	return rs
}

// dispatch calls Sink.Record for each exchange any connection this
// recorderSet owns finished, off every connection's own goroutine, until
// stop is called. A panicking Sink must not take down the server process:
// it is recovered, logged, and counted as a drop, and the next exchange
// still reaches Record normally.
func (rs *recorderSet) dispatch() {
	defer rs.wg.Done()
	for {
		select {
		case ex := <-rs.finished:
			rs.recordOne(ex)
		case <-rs.done:
			return
		}
	}
}

func (rs *recorderSet) recordOne(ex Exchange) {
	defer func() {
		if r := recover(); r != nil {
			rs.dropped.Add(1)
			rs.errorLog.Printf("sep2capture: Sink.Record panicked on connection %d, exchange %d: %v\n%s", ex.ConnID, ex.ID, r, debug.Stack())
		}
	}()
	rs.sink.Record(ex)
}

// stop ends the dispatch goroutine and waits for it to exit. Safe to call
// more than once or concurrently; only the first call has any effect. Any
// exchange still in rs.finished when it runs is abandoned, uncounted: Close
// is an abrupt shutdown, the same way http.Server.Close does not wait for
// in-flight handlers either.
func (rs *recorderSet) stop() {
	rs.stopOnce.Do(func() { close(rs.done) })
	rs.wg.Wait()
}

// wrap creates the connRecorder for a newly accepted connection. Identity is
// derived lazily, from recordingConn.ConnectionState, the first time
// net/http asks for it: wrap itself does not require c's TLS handshake, if
// any, to be complete yet.
func (rs *recorderSet) wrap(c net.Conn) *recordingConn {
	rec := newConnRecorder(rs, rs.nextConnID.Add(1), c.RemoteAddr().String())

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
// unique among open connections on one listener; it is only ever ambiguous
// once a connection has closed and its port has been reused, and by then
// its recorder has already been forgotten.
func (rs *recorderSet) byRemoteAddr(addr string) *connRecorder {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	return rs.byAddr[addr]
}

// observe is Attach's ConnState hook. It opens the first exchange at
// StateNew, rolls over at StateIdle, and closes the last exchange at
// StateClosed or StateHijacked. Any net.Conn that is not one of ours (a
// hijacked connection's replacement, for instance) is ignored, and so is one
// wrapped by a different recorderSet: Attach called twice on the same
// server chains both ConnState hooks onto srv.ConnState, so every recorded
// connection's hook fires here even when this recorderSet never wrapped it
// (only the recorderSet whose listener is actually served does). Acting on
// a connection this recorderSet does not own would finish exchanges into
// the wrong recorderSet's byAddr map, and clientLFDI/SFDI would come from
// whichever recorderSet the type assertion below matched.
func (rs *recorderSet) observe(c net.Conn, state http.ConnState) {
	rc, ok := c.(*recordingConn)
	if !ok || rc.rec.rs != rs {
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
