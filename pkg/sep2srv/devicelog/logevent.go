package devicelog

import (
	"context"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/logsafe"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/identity"
	sep2lfdi "github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/lfdi"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
)

// KindLogEvent is the line kind for a LogEvent a device posted.
const KindLogEvent = "logevent"

// KindRequest is the line kind for a request that passed identity and the ACL.
const KindRequest = "request"

// KindRefused is the line kind for a request the ownership gate refused
// with a 403.
const KindRefused = "refused"

// LogEventStore decorates a LogEvent store so each successful Create also
// writes a logevent line to the creating device's log. Every other method is
// the inner store's.
type LogEventStore struct {
	store.ScopedStore[sep2.LogEvent]
	w        *Writer
	identity identity.Func
	resolve  Resolver
}

// NewLogEventStore returns inner unchanged when w or identity is nil, so a
// server with device logging off mounts the plain store.
//
// resolve decides the line's file and target exactly as it does for a request
// line, through AppendResolved; nil files every line under its actor.
func NewLogEventStore(inner store.ScopedStore[sep2.LogEvent], w *Writer, identity identity.Func, resolve Resolver) store.ScopedStore[sep2.LogEvent] {
	if w == nil || identity == nil || store.IsAbsent(inner) {
		return inner
	}
	return &LogEventStore{ScopedStore: inner, w: w, identity: identity, resolve: resolve}
}

// Create stores the event, then queues its line. The line is keyed by the
// identity on ctx, never by a path or body value. A failed Create writes
// nothing. A line that cannot be written (no usable identity, writer closed,
// queue full) is counted in Dropped and logged, and never fails the request.
func (s *LogEventStore) Create(ctx context.Context, parentID, id string, ev sep2.LogEvent) error {
	if err := s.ScopedStore.Create(ctx, parentID, id, ev); err != nil {
		return err
	}
	lfdi, _, ok := s.identity(ctx)
	if !ok {
		s.w.Lost("unknown device", "no identity on the request, LogEvent line dropped")
		return nil
	}
	actor, ok := sep2lfdi.Canonical(lfdi)
	if !ok {
		s.w.Lost("unknown device", "identity LFDI is not canonical, LogEvent line dropped")
		return nil
	}
	fields := map[string]any{
		"edev":            parentID,
		"href":            ev.Href,
		"functionSet":     ev.FunctionSet,
		"logEventCode":    ev.LogEventCode,
		"logEventID":      ev.LogEventID,
		"logEventPEN":     ev.LogEventPEN,
		"profileID":       ev.ProfileID,
		"createdDateTime": ev.CreatedDateTime,
	}
	if ev.ExtendedData != nil {
		fields["extendedData"] = *ev.ExtendedData
	}
	if ev.Details != "" {
		fields["details"] = logsafe.Str(ev.Details)
	}
	// AppendResolved counts and logs every drop itself, so the error carries
	// nothing the request could act on.
	_ = s.w.AppendResolved(actor, parentID, s.resolve, Entry{Kind: KindLogEvent, Fields: fields})
	return nil
}

// Lost counts a line the caller could not hand to Append and logs the first
// occurrence of reason for who, like the writer's own drops.
func (w *Writer) Lost(who, reason string) {
	w.dropped.Add(1)
	w.note(who, reason, nil)
}
