package flowreservation

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/commitment"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/dercontrol"
)

// ErrNothingToRevise is returned when the request has no response yet: an
// unanswered request is answered, not revised.
var ErrNothingToRevise = errors.New("flowreservation: request has no response to revise")

// ErrIncompleteDeps is returned when ReviseDeps lacks a store or Replace.
var ErrIncompleteDeps = errors.New("flowreservation: ReviseDeps needs FRQ, FRP and Replace")

// ReplacementFunc wraps a response about to be stored as a
// commitment.Replacement. internal/commitment/sources.NewReplacement over
// the response store is the production one; it lives there because that
// package imports this one.
type ReplacementFunc func(edevID string, frp sep2.FlowReservationResponse) (commitment.Replacement, error)

// ReviseDeps is what Revise reads and writes.
type ReviseDeps struct {
	FRQ     FRQReader
	FRP     FRPReader
	Ledger  *commitment.Ledger
	Writers commitment.Writers
	Replace ReplacementFunc
	// PEN is embedded in the new response's mRID, as for Queue.
	PEN *uint32
	// Answers records who made the revision; nil records nothing.
	Answers *Answers
}

// Revise changes the answer to a request by cancel-and-create: it builds the
// response decision implies, stores it under the next revision id with the
// same subject, moves the old grant's DER controls onto it, and cancels the
// old grant, all through commitment.Ledger.Revise. The old response is
// never edited; its Cancelled status is derived from its lifecycle record.
// Kind Deny revises to a denial. A revision refused by the ledger (a
// cancelled or denied tip, a window the fleet holds, a control that no
// longer fits) stores nothing.
//
// by is recorded as the new response's author, written under the fleet lock
// just before the response and taken back with it. Recording the old
// grant's canceller is the caller's, once Revise has succeeded.
//
// The new creationTime is max(now, old+1): creationTime is whole seconds and
// must strictly increase along a chain (10.2.2.3 e), so a revision in the
// same second as its predecessor is not the client's error.
func Revise(ctx context.Context, deps ReviseDeps, edevID, frqID string, decision Decision, reason string, by Attribution, now time.Time) (sep2.FlowReservationResponse, error) {
	if deps.Ledger == nil {
		return sep2.FlowReservationResponse{}, commitment.ErrNoLedger
	}
	if deps.FRQ == nil || deps.FRP == nil || deps.Replace == nil {
		return sep2.FlowReservationResponse{}, ErrIncompleteDeps
	}
	chain, err := ChainOf(ctx, deps.FRP, edevID, frqID)
	if err != nil {
		return sep2.FlowReservationResponse{}, err
	}
	if len(chain) == 0 {
		return sep2.FlowReservationResponse{}, ErrNothingToRevise
	}
	tip := chain[len(chain)-1]

	frq, err := deps.FRQ.Get(ctx, edevID, frqID)
	if err != nil {
		return sep2.FlowReservationResponse{}, fmt.Errorf("flowreservation: get FlowReservationRequest %s/%s: %w", edevID, frqID, err)
	}
	frp, err := answerFor(frq, decision)
	if err != nil {
		return sep2.FlowReservationResponse{}, err
	}
	mrid, err := newFRPMRID(deps.PEN)
	if err != nil {
		return sep2.FlowReservationResponse{}, fmt.Errorf("flowreservation: mint FlowReservationResponse mRID: %w", err)
	}
	tipID, ok := ResponseID(edevID, tip.Href)
	if !ok {
		return sep2.FlowReservationResponse{}, fmt.Errorf("flowreservation: response href %q has no store id", tip.Href)
	}
	frp.MRID = mrid
	frp.Subject = tip.Subject
	newID := RevisionID(tipID)
	frp.Href = responseHref(edevID, newID)

	var stored sep2.FlowReservationResponse
	err = deps.Ledger.Revise(ctx, deps.Writers, tip.MRID, reason, now.Unix(), func(old commitment.Grant) (commitment.Replacement, error) {
		frp.CreationTime = max(now.Unix(), old.CreationTime+1)
		var start int64
		if frp.Interval != nil {
			start = frp.Interval.Start
		}
		es := deriveEventStatus(start, frp.CreationTime, now.Unix(), dercontrol.LifecycleRecord{})
		frp.EventStatus = &es
		stored = frp
		rep, err := deps.Replace(edevID, frp)
		if err != nil {
			return rep, err
		}
		if rep.Create != nil && rep.Delete != nil {
			rep.Create, rep.Delete = deps.Answers.recorded(edevID, newID, AnswerRecord{Action: ActionRevise, By: by}, rep.Create, rep.Delete)
		}
		return rep, nil
	})
	if err != nil {
		return sep2.FlowReservationResponse{}, err
	}
	return stored, nil
}
