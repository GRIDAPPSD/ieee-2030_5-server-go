package sep2capture

import "time"

// Mark classifies how an exchange ended. A parser or protocol error never
// suppresses capture: every exchange gets exactly one Mark.
type Mark int

const (
	// MarkHandled is the ordinary case: the handler ran and produced the
	// stored response, whatever its own bytes look like.
	MarkHandled Mark = iota
	// MarkRejectedBeforeHandler is a request net/http's own parser refused
	// before any handler saw it; Response holds net/http's own error reply.
	MarkRejectedBeforeHandler
	// MarkNoResponse is a request the client abandoned (EOF or reset)
	// before any response was written and before a handler ran.
	MarkNoResponse
	// MarkIncomplete is an exchange cut short by a read or write deadline.
	MarkIncomplete
	// MarkConnectionError is an exchange cut short by a transport or TLS
	// error mid-stream, distinct from an ordinary client disconnect.
	MarkConnectionError
)

// String names a Mark for logs and tests.
func (m Mark) String() string {
	switch m {
	case MarkHandled:
		return "handled"
	case MarkRejectedBeforeHandler:
		return "rejected before handler"
	case MarkNoResponse:
		return "no response"
	case MarkIncomplete:
		return "incomplete"
	case MarkConnectionError:
		return "connection error"
	default:
		return "unknown"
	}
}

// Direction holds one side (inbound or outbound) of an exchange: the bytes
// exactly as they crossed the wire, up to PerDirectionCap, and the true
// length so a capped exchange is never mistaken for a small one.
type Direction struct {
	Bytes     []byte
	TrueLen   int64
	Truncated bool
}

// Exchange is everything read and written on one connection between two
// ConnState boundaries. Request and Response are the bytes as the wire
// carried them: no reframing, no normalisation.
type Exchange struct {
	ID          uint64
	ConnID      uint64
	ClientLFDI  string
	ClientSFDI  string
	Started     time.Time
	Ended       time.Time
	Request     Direction
	Response    Direction
	Mark        Mark
	Error       string // set for MarkNoResponse, MarkIncomplete, MarkConnectionError
	HandlerRuns int    // >1 flags a pipelined exchange
}

// Sink receives each exchange once it closes. Attach hands exchanges to the
// sink off the connection's own goroutine (see recorder.go), so a slow or
// erroring Sink never delays or breaks the client's read or write: the
// hand-off to Sink.Record is non-blocking, and a panic inside Record is
// recovered. Under sustained backpressure (Record too slow, or panicking
// repeatedly) exchanges are dropped rather than queued without bound or
// allowed to stall the connection; Dropped reports how many. PR 3
// implements Sink with the segment log; MemorySink below is this PR's
// implementation.
type Sink interface {
	Record(Exchange)
}
