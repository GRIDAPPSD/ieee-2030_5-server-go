package devicelog

import (
	"context"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/identity"
	sep2lfdi "github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/lfdi"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/mrid"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
)

// ResponseStore decorates a Response store so each successful Create tells
// the request's Note which event the Response answered, the status it
// carried, and the device it names when that is not the caller. Every other
// method is the inner store's.
type ResponseStore struct {
	store.ScopedStore[sep2.Response]
	identity identity.Func
}

// NewResponseStore returns inner unchanged when identity is nil, so a server
// with device logging off mounts the plain store.
func NewResponseStore(inner store.ScopedStore[sep2.Response], identity identity.Func) store.ScopedStore[sep2.Response] {
	if identity == nil || store.IsAbsent(inner) {
		return inner
	}
	return &ResponseStore{ScopedStore: inner, identity: identity}
}

// Create stores the Response, then notes its facts. A failed Create notes
// nothing. The subject is client text, so it is recorded only in canonical
// mRID form; anything else is recorded as subjectInvalid, never as the text.
// Note.Set cannot fail for these names and value types, so its error is not
// acted on.
func (s *ResponseStore) Create(ctx context.Context, parentID, id string, rsp sep2.Response) error {
	if err := s.ScopedStore.Create(ctx, parentID, id, rsp); err != nil {
		return err
	}
	note := NoteFrom(ctx)
	if rsp.Subject != "" {
		if m, ok := mrid.Canonical(rsp.Subject); ok {
			_ = note.Set("subject", m)
		} else {
			_ = note.Set("subjectInvalid", true)
		}
	}
	if rsp.Status != nil {
		_ = note.Set("rspStatus", int(*rsp.Status))
	}
	if named, ok := sep2lfdi.Canonical(rsp.EndDeviceLFDI); ok {
		caller, _, haveCaller := s.identity(ctx)
		if actor, ok := sep2lfdi.Canonical(caller); haveCaller && ok && actor != named {
			_ = note.Set("target", named)
		}
	}
	return nil
}
