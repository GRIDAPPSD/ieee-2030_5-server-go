package sep2capture

import (
	"context"
	"errors"
	"fmt"
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
// once at handshake, and any byte read early for the next exchange. att
// scopes it to the Attach call that accepted it, both for byAddr lookups
// and as the route to the shared Recorder every exchange dispatches
// through.
type connRecorder struct {
	att        *attachment
	connID     uint64
	remoteAddr string
	clientLFDI string
	clientSFDI string

	mu           sync.Mutex
	current      *building
	pendingCarry []byte
}

// newConnRecorder starts the per-connection recorder. Every exchange it
// finishes hands off to att's Recorder's own shared dispatch goroutine (see
// Recorder.dispatch); this type starts nothing of its own.
func newConnRecorder(att *attachment, connID uint64, remoteAddr string) *connRecorder {
	return &connRecorder{
		att:        att,
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
// while cr.hasByte is still set. So there is no fold-back case for
// recordInbound to handle.
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

// finish marks one closed exchange and hands it to the shared Recorder's
// dispatch goroutine, which calls Sink.Record off every connection's own
// goroutine and in the order exchanges are handed off: a slow or erroring
// sink must never delay or break the connection it came from.
//
// An exchange with no bytes in either direction is dropped rather than
// handed off at all: it is what a keep-alive client closing while idle
// leaves behind (the rollover at StateIdle opens a next exchange that never
// receives anything before StateClosed), and recording it would show every
// normal client as a failing one.
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
	rec.att.r.enqueue(ex)
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

// sinkQueueCapacity bounds how many closed exchanges can wait for
// Sink.Record across the whole Recorder, not per connection: a queue scoped
// to one connection would let a hung Sink leak one dispatch goroutine per
// connection, unbounded in the number of connections, with nothing counted
// in Dropped until each connection's own queue happened to fill.
const sinkQueueCapacity = 256

// attachment is the state one Attach call owns: which server it wrapped
// (for the handshake bound, Q3), and the connections it has accepted, keyed
// by remote address so the annotation middleware can find the exchange a
// request belongs to. The dispatch goroutine, the shared queue, and the ID
// and drop counters live on r instead, so two Attach calls on one Recorder
// never interfere over those, only over which connections are theirs.
type attachment struct {
	r   *Recorder
	srv *http.Server

	mu     sync.Mutex
	byAddr map[string]*connRecorder
}

func newAttachment(r *Recorder, srv *http.Server) *attachment {
	return &attachment{r: r, srv: srv, byAddr: make(map[string]*connRecorder)}
}

// wrap creates the connRecorder for a newly accepted connection and
// returns the net.Conn Attach's listener hands to net/http: the TLS-capable
// type when c itself is TLS-capable (the same switch stateOf uses), the
// plain type otherwise (Q4). A plain conn returning ConnectionState
// unconditionally would make net/http populate a non-nil, zero-value r.TLS
// for a plaintext listener, indistinguishable from a real, empty handshake
// to any handler that only checks r.TLS != nil.
func (a *attachment) wrap(c net.Conn) net.Conn {
	rec := newConnRecorder(a, a.r.nextConnID.Add(1), c.RemoteAddr().String())

	a.mu.Lock()
	a.byAddr[rec.remoteAddr] = rec
	a.mu.Unlock()

	// Each branch builds its own recordingCore in place, rather than
	// through a shared local, because recordingCore holds a sync.Once and
	// copying an initialized one (even a zero-value one, by value through
	// an intermediate variable) is what go vet's copylocks check catches.
	if !isTLSCapable(c) {
		return &plainRecordingConn{recordingCore: recordingCore{Conn: c, rec: rec}}
	}
	return &recordingConn{recordingCore: recordingCore{Conn: c, rec: rec}}
}

func (a *attachment) forget(rec *connRecorder) {
	a.mu.Lock()
	if a.byAddr[rec.remoteAddr] == rec {
		delete(a.byAddr, rec.remoteAddr)
	}
	a.mu.Unlock()
}

// byRemoteAddr finds the recorder for a live connection. RemoteAddr is
// unique among open connections on one listener; it is only ever ambiguous
// once a connection has closed and its port has been reused, and by then
// its recorder has already been forgotten.
func (a *attachment) byRemoteAddr(addr string) *connRecorder {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.byAddr[addr]
}

// observe is Attach's ConnState hook. It opens the first exchange at
// StateNew, rolls over at StateIdle, and closes the last exchange at
// StateClosed or StateHijacked, tracking r's live-connection count across
// every attachment. Any net.Conn that is not one of ours (a hijacked
// connection's replacement, for instance) is ignored, and so is one wrapped
// by a different attachment: Attach called twice on the same server chains
// both ConnState hooks onto srv.ConnState, so every recorded connection's
// hook fires here even when this attachment never wrapped it. Acting on a
// connection this attachment does not own would finish exchanges into the
// wrong attachment's byAddr map, and clientLFDI/SFDI would come from
// whichever attachment the type assertion below matched.
func (a *attachment) observe(c net.Conn, state http.ConnState) {
	wc, ok := c.(wrappedConn)
	if !ok {
		return
	}
	rec := wc.recorderFor()
	if rec.att != a {
		return
	}
	switch state {
	case http.StateNew:
		a.r.liveConns.Add(1)
		rec.open(a.r.nextExchangeID.Add(1))
	case http.StateIdle:
		rec.rollover(a.r.nextExchangeID.Add(1))
	case http.StateClosed, http.StateHijacked:
		rec.closeFinal()
		a.forget(rec)
		a.r.liveConns.Add(-1)
	}
}

// annotate marks the exchange open on the request's connection as
// "a handler ran" before calling next. It reads no body and wraps nothing
// else: a handler that never touches the body still gets marked, and a
// handler that panics after this point still counts as having run.
//
// next is srv.Handler as Attach found it, nil included: a nil Handler is
// ordinary net/http usage (server.go's serverHandler substitutes
// DefaultServeMux at serve time), but Attach always replaces srv.Handler
// with this wrapper, so that substitution must happen here instead.
func (a *attachment) annotate(next http.Handler) http.Handler {
	if next == nil {
		next = http.DefaultServeMux
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if rec := a.byRemoteAddr(r.RemoteAddr); rec != nil {
			rec.markHandlerRan()
		}
		next.ServeHTTP(w, r)
	})
}

// Recorder is the dispatch goroutine, the shared queue, and the lifecycle
// every Attach call on it shares: created once by the caller, attached to
// one or more servers, and closed once after them (see NewRecorder and
// Close). Per-listener state (the byAddr map) lives on attachment instead,
// so two Attach calls on one Recorder never interfere over IDs, the drop
// count, or the queue, only over which connections are theirs.
type Recorder struct {
	sink     Sink
	errorLog *log.Logger

	nextExchangeID atomic.Uint64
	nextConnID     atomic.Uint64
	dropped        atomic.Uint64
	liveConns      atomic.Int64

	// intakeMu guards closed: enqueue holds it for reading around its
	// check-then-send, and Close's stage 2 holds it for writing once, to
	// flip closed and close finished with no send racing that close.
	intakeMu  sync.RWMutex
	closed    bool
	closeOnce sync.Once

	finished     chan Exchange
	dispatchDone chan struct{}

	inFlightMu sync.Mutex
	inFlight   bool
}

// NewRecorder creates a Recorder and starts its one dispatch goroutine.
// errorLog receives handshake-failure and Sink-panic reports; nil uses the
// standard logger, matching net/http's own ErrorLog default. Call Attach
// for every server and listener it should record, and Close exactly once,
// after every attached server has itself stopped (see Close's doc for the
// caller contract).
func NewRecorder(sink Sink, errorLog *log.Logger) *Recorder {
	if errorLog == nil {
		errorLog = log.Default()
	}
	r := &Recorder{
		sink:         sink,
		errorLog:     errorLog,
		finished:     make(chan Exchange, sinkQueueCapacity),
		dispatchDone: make(chan struct{}),
	}
	go r.dispatch()
	return r
}

// dispatch calls Sink.Record for each exchange any attachment on r
// finished, off every connection's own goroutine, until finished is closed
// and empty. A panicking Sink must not take down the server process: it is
// recovered, logged, and counted as a drop, and the next exchange still
// reaches Record normally.
func (r *Recorder) dispatch() {
	defer close(r.dispatchDone)
	for ex := range r.finished {
		r.setInFlight(true)
		r.recordOne(ex)
		r.setInFlight(false)
	}
}

func (r *Recorder) setInFlight(v bool) {
	r.inFlightMu.Lock()
	r.inFlight = v
	r.inFlightMu.Unlock()
}

// inFlightNow reports whether dispatch is currently blocked inside
// Sink.Record, for Close's abandon accounting; it does not clear the flag,
// since the goroutine that set it is the only one allowed to.
func (r *Recorder) inFlightNow() bool {
	r.inFlightMu.Lock()
	defer r.inFlightMu.Unlock()
	return r.inFlight
}

func (r *Recorder) recordOne(ex Exchange) {
	defer func() {
		if rv := recover(); rv != nil {
			r.dropped.Add(1)
			r.errorLog.Printf("sep2capture: Sink.Record panicked on connection %d, exchange %d: %v\n%s", ex.ConnID, ex.ID, rv, debug.Stack())
		}
	}()
	r.sink.Record(ex)
}

// enqueue hands ex to the dispatch goroutine, or counts it dropped: the
// shared queue is full, or Close has closed intake. The send itself never
// blocks (finish runs on the connection's own goroutine), and the read
// lock below only ever waits behind Close's brief write-lock window, never
// behind a slow Sink.
func (r *Recorder) enqueue(ex Exchange) {
	r.intakeMu.RLock()
	defer r.intakeMu.RUnlock()
	if r.closed {
		r.dropped.Add(1)
		return
	}
	select {
	case r.finished <- ex:
	default:
		r.dropped.Add(1)
	}
}

// Dropped returns how many exchanges r has dropped, across every Attach
// call it has ever served: the shared queue was full, Sink.Record
// panicked, or Close's own deadline abandoned them (see Close).
func (r *Recorder) Dropped() uint64 {
	return r.dropped.Load()
}

// Close runs the shutdown this package's caller contract requires, after
// every server r is attached to has itself stopped:
//
//	err := srv.Shutdown(ctx) // or srv.Close()
//	cerr := r.Close(ctx)     // after the server, same or a fresh deadline
//
// It runs in three stages, all bounded by the one ctx: wait for every
// attached connection to end (net/http can return from Shutdown before its
// last StateClosed hook has run, so skipping this would lose exactly those
// exchanges to Dropped); close intake, so no further exchange can be
// queued; then drain whatever is still queued. Every exchange with bytes
// that closes, before or after Close, ends up either passed to Sink.Record
// or counted in Dropped, never neither.
//
// If ctx ends before the drain finishes, Close counts every exchange still
// in the queue as dropped, counts the one exchange (if any) a still-running
// Record call has not returned as dropped too, and returns an error
// wrapping ctx.Err() with how many connections were still open and how many
// exchanges were abandoned. It never waits for the dispatch goroutine
// itself: Go cannot stop a goroutine stuck inside a hung Sink.Record, so a
// Sink that never returns strands exactly that one goroutine, not one per
// connection (see the Sink doc).
//
// Close is safe to call more than once; only the first call closes
// anything, and a later call simply repeats the same waits against
// whatever is left, which by then is normally nothing.
func (r *Recorder) Close(ctx context.Context) error {
	r.waitLiveZero(ctx)
	open := r.liveConns.Load()

	r.closeOnce.Do(func() {
		r.intakeMu.Lock()
		r.closed = true
		r.intakeMu.Unlock()
		close(r.finished)
	})

	select {
	case <-r.dispatchDone:
		return nil
	case <-ctx.Done():
	}

	abandoned := r.drainRemaining()
	if r.inFlightNow() {
		r.dropped.Add(1)
		abandoned++
	}
	return fmt.Errorf("sep2capture: Close: %w with %d connection(s) still open and %d exchange(s) abandoned", ctx.Err(), open, abandoned)
}

// waitLiveZero polls r's live-connection count down to zero, bounded by
// ctx. A plain sync.WaitGroup is not safe here: its contract forbids a
// positive Add running concurrently with a Wait that has already observed
// the counter reach zero, and this package cannot guarantee no connection
// is still being accepted the instant Close is called.
func (r *Recorder) waitLiveZero(ctx context.Context) {
	if r.liveConns.Load() <= 0 {
		return
	}
	ticker := time.NewTicker(2 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if r.liveConns.Load() <= 0 {
				return
			}
		}
	}
}

// drainRemaining empties whatever is still buffered in r.finished, counting
// each as dropped, without waiting for the dispatch goroutine to do it.
// Called only after intake is closed, so nothing can arrive behind it.
func (r *Recorder) drainRemaining() int {
	n := 0
	for {
		select {
		case _, ok := <-r.finished:
			if !ok {
				return n
			}
			n++
			r.dropped.Add(1)
		default:
			return n
		}
	}
}
