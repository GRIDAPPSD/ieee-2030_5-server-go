package devicelog

import (
	"context"
	"errors"
	"sync"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/logsafe"
)

// maxNoteValue bounds a string a note carries, since a decorator may pass
// text a client chose.
const maxNoteValue = 256

// ErrNoteField is returned by Note.Set for a name outside the closed set.
var ErrNoteField = errors.New("devicelog: field name not allowed in a request note")

// noteFields is the closed set of names Note.Set accepts. It never holds a
// name the writer or the request line sets itself (seq, t, kind, actor,
// method, path, status, reason, ...), so a decorator cannot overwrite one.
var noteFields = map[string]struct{}{
	"subject": {}, "subjectInvalid": {}, "rspStatus": {}, "target": {},
	"mrids": {}, "mridsMore": {},
	"mup": {}, "mupMRID": {}, "mupMRIDInvalid": {},
	"mmr": {}, "readings": {}, "mmrids": {}, "uoms": {},
}

// Note carries the facts inner layers hand to the request observer for one
// request: the ownership gate's refusal reason and the fields a store
// decorator records. The observer creates one per request with WithNote and
// reads it after the handler returns.
type Note struct {
	mu     sync.Mutex
	reason string
	fields map[string]any
}

type noteKey struct{}

// WithNote returns ctx carrying a new Note, and the Note.
func WithNote(ctx context.Context) (context.Context, *Note) {
	n := &Note{}
	return context.WithValue(ctx, noteKey{}, n), n
}

// NoteFrom returns the Note in ctx, or nil when no observer put one there.
// Every method is a no-op on a nil Note, so a gate or decorator built without
// an observer behaves as before.
func NoteFrom(ctx context.Context) *Note {
	n, _ := ctx.Value(noteKey{}).(*Note)
	return n
}

// Refuse records the fixed reason a gate refused the request.
func (n *Note) Refuse(reason string) {
	if n == nil {
		return
	}
	n.mu.Lock()
	n.reason = reason
	n.mu.Unlock()
}

// Reason returns the refusal reason, "" when none was recorded.
func (n *Note) Reason() string {
	if n == nil {
		return ""
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.reason
}

// Set records a field for the request's line. It refuses a name outside the
// closed set with ErrNoteField. A string value is capped at maxNoteValue
// octets.
func (n *Note) Set(key string, value any) error {
	if _, ok := noteFields[key]; !ok {
		return ErrNoteField
	}
	if n == nil {
		return nil
	}
	if s, ok := value.(string); ok {
		value = logsafe.TruncateOctets(s, maxNoteValue)
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.fields == nil {
		n.fields = map[string]any{}
	}
	n.fields[key] = value
	return nil
}

// Fields returns a copy of the recorded fields.
func (n *Note) Fields() map[string]any {
	if n == nil {
		return nil
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	out := make(map[string]any, len(n.fields))
	for k, v := range n.fields {
		out[k] = v
	}
	return out
}
