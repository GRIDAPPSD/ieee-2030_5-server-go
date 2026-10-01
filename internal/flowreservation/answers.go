package flowreservation

import (
	"context"
	"errors"
	"fmt"
	"log"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
)

// Attribution kinds: who made a change.
const (
	// KindOperator is an admin route's write. The server cannot name a person:
	// Principal is the mTLS client certificate's fingerprint when one admitted
	// the request, otherwise PrincipalAdminKey.
	KindOperator = "operator"
	// KindDeadlineFallback is the queue's own answer once the hold expires.
	KindDeadlineFallback = "deadline_fallback"
	// KindClient is the aggregator withdrawing its own request.
	KindClient = "client"
	// KindRecovery is the startup pass finishing a change a restart cut short.
	KindRecovery = "recovery"
)

// Attribution names who made a change, and when (Unix seconds).
type Attribution struct {
	Kind string `json:"kind"`
	// Admission is the admin admission path ("mtls", "bearer", "cookie",
	// "ticket") for KindOperator, and empty otherwise.
	Admission string `json:"admission,omitempty"`
	// Principal is "cert:<64 hex SHA-256>" or PrincipalAdminKey for
	// KindOperator, and empty otherwise.
	Principal string `json:"principal,omitempty"`
	At        int64  `json:"at"`
}

// PrincipalAdminKey is the principal of every admin admission but mTLS:
// Bearer, cookie session and ticket all rest on the one admin key.
const PrincipalAdminKey = "admin-key"

// Actions an AnswerRecord names.
const (
	ActionAnswer = "answer"
	ActionRevise = "revise"
)

// AnswerRecord is who created one response and, once it is cancelled, who
// cancelled it. It is keyed like the response.
type AnswerRecord struct {
	Action      string       `json:"action"`
	By          Attribution  `json:"by"`
	CancelledBy *Attribution `json:"cancelledBy,omitempty"`
}

// Copy implements store.Copier.
func (r AnswerRecord) Copy() AnswerRecord {
	if r.CancelledBy != nil {
		c := *r.CancelledBy
		r.CancelledBy = &c
	}
	return r
}

// AnswerStore is the store answer records live in.
type AnswerStore interface {
	Get(ctx context.Context, parentID, id string) (AnswerRecord, error)
	Create(ctx context.Context, parentID, id string, rec AnswerRecord) error
	Update(ctx context.Context, parentID, id string, rec AnswerRecord) error
	Delete(ctx context.Context, parentID, id string) error
}

// Answers records attribution in an AnswerStore. A nil *Answers records
// nothing and reads nothing, so every response reads as unrecorded.
type Answers struct {
	store AnswerStore
}

// NewAnswers returns an Answers over s, or nil when s is nil.
func NewAnswers(s AnswerStore) *Answers {
	if s == nil {
		return nil
	}
	return &Answers{store: s}
}

// AttributionsOf returns who answered and who cancelled the response stored
// under (edevID, responseID); nil for either means nothing was recorded.
func (a *Answers) AttributionsOf(ctx context.Context, edevID, responseID string) (answeredBy, cancelledBy *Attribution, err error) {
	if a == nil {
		return nil, nil, nil
	}
	rec, err := a.store.Get(ctx, edevID, responseID)
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, fmt.Errorf("flowreservation: answer record %s/%s: %w", edevID, responseID, err)
	}
	if rec.By.Kind != "" {
		by := rec.By
		answeredBy = &by
	}
	return answeredBy, rec.CancelledBy, nil
}

// intend writes rec as the record of a response about to be created under
// (edevID, id), and returns an undo that puts back what the key held before.
// It runs immediately before the response's own Create, under the same
// locks, so a response never exists without its record. A record with no
// attribution kind writes nothing.
func (a *Answers) intend(ctx context.Context, edevID, id string, rec AnswerRecord) (undo func(context.Context) error, err error) {
	noop := func(context.Context) error { return nil }
	if a == nil || rec.By.Kind == "" {
		return noop, nil
	}
	prev, err := a.store.Get(ctx, edevID, id)
	switch {
	case errors.Is(err, store.ErrNotFound):
		if err := a.store.Create(ctx, edevID, id, rec); err != nil {
			return nil, fmt.Errorf("flowreservation: record answer %s/%s: %w", edevID, id, err)
		}
		return func(ctx context.Context) error { return a.store.Delete(ctx, edevID, id) }, nil
	case err != nil:
		return nil, fmt.Errorf("flowreservation: read answer record %s/%s: %w", edevID, id, err)
	}
	// A record left by a response that no longer exists (a crash after its
	// intent, or a revision rolled back at startup) is replaced.
	if err := a.store.Update(ctx, edevID, id, rec); err != nil {
		return nil, fmt.Errorf("flowreservation: record answer %s/%s: %w", edevID, id, err)
	}
	return func(ctx context.Context) error { return a.store.Update(ctx, edevID, id, prev) }, nil
}

// RecordCancel records by as the canceller of the response under (edevID,
// id). It runs only after the cancel succeeded, so a refused cancel records
// nothing. A response with no record gets one carrying only the cancel.
func (a *Answers) RecordCancel(ctx context.Context, edevID, id string, by Attribution) error {
	if a == nil || by.Kind == "" {
		return nil
	}
	rec, err := a.store.Get(ctx, edevID, id)
	switch {
	case errors.Is(err, store.ErrNotFound):
		err = a.store.Create(ctx, edevID, id, AnswerRecord{CancelledBy: &by})
	case err == nil:
		rec.CancelledBy = &by
		err = a.store.Update(ctx, edevID, id, rec)
	}
	if err != nil {
		return fmt.Errorf("flowreservation: record cancel of %s/%s: %w", edevID, id, err)
	}
	return nil
}

// errTakeBack marks a failed undo of an answer record after its response's
// create failed: the record may now name a response it did not create.
var errTakeBack = errors.New("flowreservation: take back answer record")

// recorded wraps a response's create and delete so its record is written
// just before the create and put back as it was if the create fails or the
// response is deleted again (a revision's rollback). A caller that never
// deletes passes a nil del and drops wrappedDelete.
func (a *Answers) recorded(edevID, id string, rec AnswerRecord, create, del func(context.Context) error) (wrappedCreate, wrappedDelete func(context.Context) error) {
	undo := func(context.Context) error { return nil }
	wrappedCreate = func(ctx context.Context) error {
		u, err := a.intend(ctx, edevID, id, rec)
		if err != nil {
			return err
		}
		undo = u
		if err := create(ctx); err != nil {
			if uerr := undo(context.WithoutCancel(ctx)); uerr != nil {
				return errors.Join(err, fmt.Errorf("%w %s/%s: %w", errTakeBack, edevID, id, uerr))
			}
			return err
		}
		return nil
	}
	// The response is gone once del succeeds, so a record that cannot be put
	// back is only logged: it names no response, and the next response under
	// id replaces it.
	wrappedDelete = func(ctx context.Context) error {
		if err := del(ctx); err != nil {
			return err
		}
		if err := undo(context.WithoutCancel(ctx)); err != nil {
			log.Printf("flowreservation: answer record %s/%s outlives its deleted response: %v", edevID, id, err)
		}
		return nil
	}
	return wrappedCreate, wrappedDelete
}
