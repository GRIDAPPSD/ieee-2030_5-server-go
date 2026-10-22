package devicelog

import (
	"context"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/logsafe"
	sep2lfdi "github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/lfdi"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
)

// KindLogEvent is the line kind for a LogEvent a device posted.
const KindLogEvent = "logevent"

// IdentityFunc reports the identity the auth layer put on a request context.
type IdentityFunc func(ctx context.Context) (lfdi, sfdi string, ok bool)

// LogEventStore decorates a LogEvent store so each successful Create also
// writes a logevent line to the creating device's log. Every other method is
// the inner store's.
type LogEventStore struct {
	store.ScopedStore[sep2.LogEvent]
	w        *Writer
	identity IdentityFunc
}

// NewLogEventStore returns inner unchanged when w or identity is nil, so a
// server with device logging off mounts the plain store.
func NewLogEventStore(inner store.ScopedStore[sep2.LogEvent], w *Writer, identity IdentityFunc) store.ScopedStore[sep2.LogEvent] {
	if w == nil || identity == nil || store.IsAbsent(inner) {
		return inner
	}
	return &LogEventStore{ScopedStore: inner, w: w, identity: identity}
}

// Create stores the event, then queues its line. The line is keyed by the
// identity on ctx, never by a path or body value. A failed Create writes
// nothing, and a dropped line never fails the request: Append counts it.
func (s *LogEventStore) Create(ctx context.Context, parentID, id string, ev sep2.LogEvent) error {
	if err := s.ScopedStore.Create(ctx, parentID, id, ev); err != nil {
		return err
	}
	lfdi, _, ok := s.identity(ctx)
	if !ok {
		return nil
	}
	actor, ok := sep2lfdi.Canonical(lfdi)
	if !ok {
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
	_ = s.w.Append(actor, Entry{Kind: KindLogEvent, Fields: fields})
	return nil
}
