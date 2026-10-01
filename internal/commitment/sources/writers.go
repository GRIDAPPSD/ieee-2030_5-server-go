package sources

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/commitment"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/dercontrol"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
)

// executionIssuer is the subset of *dercontrol.Issuer the writers use: its
// own scope lock and undo cover each write.
type executionIssuer interface {
	CancelLive(ctx context.Context, scope dercontrol.Scope, id, reason string) (bool, error)
	Relink(ctx context.Context, scope dercontrol.Scope, id, grantMRID string) (dercontrol.LifecycleRecord, error)
}

type lifecycleWriter interface {
	Get(ctx context.Context, parentID, id string) (dercontrol.LifecycleRecord, error)
	Create(ctx context.Context, parentID, id string, rec dercontrol.LifecycleRecord) error
	Update(ctx context.Context, parentID, id string, rec dercontrol.LifecycleRecord) error
}

// NewWriters returns the writers commitment.Ledger.CancelGrant and Revise
// use: executions through the DER control issuer, a grant's cancel mark in
// the response lifecycle store.
func NewWriters(issuer executionIssuer, responseLifecycles lifecycleWriter) commitment.Writers {
	return commitment.Writers{
		Executions: executionWriter{issuer: issuer},
		Grants:     grantWriter{lifecycles: responseLifecycles},
	}
}

type executionWriter struct {
	issuer executionIssuer
}

func (w executionWriter) CancelExecution(ctx context.Context, c commitment.Control, reason string) error {
	scope, err := controlScope(c)
	if err != nil {
		return err
	}
	_, err = w.issuer.CancelLive(ctx, scope, c.ID, reason)
	return err
}

func (w executionWriter) RelinkExecution(ctx context.Context, c commitment.Control, grantMRID string) error {
	scope, err := controlScope(c)
	if err != nil {
		return err
	}
	_, err = w.issuer.Relink(ctx, scope, c.ID, grantMRID)
	var refusal *dercontrol.RefusalError
	if errors.As(err, &refusal) || errors.Is(err, dercontrol.ErrNotExecution) || errors.Is(err, dercontrol.ErrRelinkNotStarted) {
		return commitment.NothingWritten(err)
	}
	return err
}

func controlScope(c commitment.Control) (dercontrol.Scope, error) {
	parts := strings.Split(c.Scope, "/")
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" || c.ID == "" {
		return dercontrol.Scope{}, fmt.Errorf("sources: control %s has scope %q and id %q, want edev/fsa/derp and an id", c.MRID, c.Scope, c.ID)
	}
	return dercontrol.Scope{EndDeviceID: parts[0], FSAID: parts[1], DERProgramID: parts[2]}, nil
}

type grantWriter struct {
	lifecycles lifecycleWriter
}

// MarkCancelled stores the cancel mark the grant source and the served
// EventStatus both read. A response has no lifecycle record until its
// first mark, so the mark is a Create; an existing record is updated, and
// one already cancelled is refused.
func (w grantWriter) MarkCancelled(ctx context.Context, g commitment.Grant, reason string, now int64) error {
	if g.EndDeviceID == "" || g.ID == "" {
		return fmt.Errorf("sources: grant %s has no store key", g.MRID)
	}
	rec, err := w.lifecycles.Get(ctx, g.EndDeviceID, g.ID)
	switch {
	case errors.Is(err, store.ErrNotFound):
		return w.lifecycles.Create(ctx, g.EndDeviceID, g.ID, dercontrol.LifecycleRecord{CancelledAt: &now, CancelReason: reason})
	case err != nil:
		return fmt.Errorf("sources: lifecycle of response %s/%s: %w", g.EndDeviceID, g.ID, err)
	case rec.CancelledAt != nil:
		return &commitment.ConflictError{Code: commitment.ConflictGrantNotLive, MRID: g.MRID}
	}
	rec.CancelledAt, rec.CancelReason = &now, reason
	return w.lifecycles.Update(ctx, g.EndDeviceID, g.ID, rec)
}

// RevisionStoredError reports that a response already sits at the id a
// revision would take. The usual cause is an earlier Revise whose undo
// failed (commitment.ErrUndo). It unwraps to store.ErrAlreadyExists.
type RevisionStoredError struct {
	EndDeviceID, ID string
}

func (e *RevisionStoredError) Error() string {
	return fmt.Sprintf("sources: a response is already stored at %s/%s, the id of the revision", e.EndDeviceID, e.ID)
}

func (e *RevisionStoredError) Unwrap() error { return store.ErrAlreadyExists }

type responseWriter interface {
	Create(ctx context.Context, parentID, id string, frp sep2.FlowReservationResponse) error
	Delete(ctx context.Context, parentID, id string) error
}

// NewReplacement wraps frp, about to be stored under edevID at the id its
// href names, as a revision for commitment.Ledger.Revise. Its fleet is left
// for the ledger, which takes the old grant's.
func NewReplacement(responses responseWriter, edevID string, frp sep2.FlowReservationResponse) (commitment.Replacement, error) {
	g, err := grantFields(edevID, frp)
	if err != nil {
		return commitment.Replacement{}, err
	}
	return commitment.Replacement{
		Grant: g,
		Create: func(ctx context.Context) error {
			err := responses.Create(ctx, edevID, g.ID, frp)
			if errors.Is(err, store.ErrAlreadyExists) {
				return &RevisionStoredError{EndDeviceID: edevID, ID: g.ID}
			}
			return err
		},
		Delete: func(ctx context.Context) error { return responses.Delete(ctx, edevID, g.ID) },
	}, nil
}
