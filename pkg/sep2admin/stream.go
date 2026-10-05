package sep2admin

import (
	"context"
	"errors"
	"time"
)

// Stream lets a Panel serve a live feed as server-sent events under
// GET /api/ui/panels/{id}/stream?param=<text>. The plane checks the
// parameter against Param before Open runs, so Open never sees a value
// outside the declared length and charset.
type Stream struct {
	Param StreamParam
	Open  StreamOpenFunc
}

// StreamParam declares the stream's one text parameter. A request value
// must be 1 to MaxLen bytes, each of them a byte of Charset.
type StreamParam struct {
	// MaxLen is 1 to MaxStreamParamLen.
	MaxLen int
	// Charset lists every byte a value may hold. Each must be printable
	// ASCII (0x20 to 0x7E).
	Charset string
}

// StreamRequest is what Open is given: the validated parameter, and the
// last event ID the client saw (its Last-Event-ID), 0 for a fresh stream.
// Open replays what it still holds after After; the plane skips any event
// whose ID is not above the last one it accepted, so a replay that
// overlaps the live feed is never sent twice.
type StreamRequest struct {
	Param string
	After uint64
}

// StreamEventKind is an event's kind on the wire.
type StreamEventKind string

const (
	StreamMessage StreamEventKind = "message"
	StreamStatus  StreamEventKind = "status"
)

// StreamEvent is one event. ID is 1 or more and rises within a source, so
// a reconnecting client resumes from it. Text is valid UTF-8.
type StreamEvent struct {
	ID   uint64
	Time time.Time
	Kind StreamEventKind
	Text string
}

// StreamSendFunc queues one event and never blocks. False means the stream
// has ended (the reader left, fell too far behind, or an event was
// refused); the caller stops sending.
type StreamSendFunc func(StreamEvent) bool

// StreamOpenFunc starts a stream. It must return promptly, under the same
// timeout as View, and deliver events through send from its own goroutines
// until ctx ends. An error refuses the stream before any event is sent.
type StreamOpenFunc func(ctx context.Context, req StreamRequest, send StreamSendFunc) error

const (
	// MaxStreamParamLen caps StreamParam.MaxLen.
	MaxStreamParamLen = 1024
	// MaxStreamEventBytes caps one encoded event's data line.
	MaxStreamEventBytes = 64 << 10
)

var (
	// ErrInvalidStream is returned by Register for a Stream with a nil
	// Open, a MaxLen outside 1..MaxStreamParamLen, or a Charset that is
	// empty or holds a byte outside printable ASCII.
	ErrInvalidStream = errors.New("sep2admin: invalid panel Stream")

	// ErrInvalidStreamParam refuses a request value. Its text never
	// carries the value.
	ErrInvalidStreamParam = errors.New("sep2admin: invalid stream parameter")
)

func (s *Stream) validate() error {
	if s.Open == nil || s.Param.MaxLen < 1 || s.Param.MaxLen > MaxStreamParamLen || s.Param.Charset == "" {
		return ErrInvalidStream
	}
	for i := 0; i < len(s.Param.Charset); i++ {
		if c := s.Param.Charset[i]; c < 0x20 || c > 0x7E {
			return ErrInvalidStream
		}
	}
	return nil
}

// Validate refuses a value that is empty, longer than MaxLen bytes, or
// holds a byte outside Charset.
func (p StreamParam) Validate(v string) error {
	if len(v) < 1 || len(v) > p.MaxLen {
		return ErrInvalidStreamParam
	}
	var allowed [256]bool
	for i := 0; i < len(p.Charset); i++ {
		allowed[p.Charset[i]] = true
	}
	for i := 0; i < len(v); i++ {
		if !allowed[v[i]] {
			return ErrInvalidStreamParam
		}
	}
	return nil
}
