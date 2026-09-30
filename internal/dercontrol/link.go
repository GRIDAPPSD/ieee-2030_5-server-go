package dercontrol

import (
	"context"
	"errors"
	"fmt"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/commitment"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
)

// ErrUncheckedCommitment refuses a commitment write that no check bounds:
// a grant link through Issue, or IssueInFleet without a check, a fleet key
// or a reach. Nothing is written.
var ErrUncheckedCommitment = errors.New("dercontrol: commitment write needs a check, a fleet key and a reach")

// ErrNotExecution refuses a Relink of a plain dispatch, or to an empty
// grant: relinking moves an execution between grants and never creates or
// removes a link. Nothing is written.
var ErrNotExecution = errors.New("dercontrol: relink applies only to a control carrying out a grant")

// Proposal is the control Issue is about to store, as a commitment check
// sees it (GRIDAPPSD/ieee-2030_5-server-go#714).
type Proposal struct {
	Scope     Scope
	FleetKey  string
	Window    commitment.Window
	GrantMRID string // "" for a plain dispatch
	TargetW   *sep2.ActivePower
	Reach     int

	// Supersedes holds the mRIDs Issue will mark superseded at
	// Window.Start, so the check can clip them instead of counting a
	// control against the one replacing it.
	Supersedes []string
}

// Check decides whether a proposal fits the commitments already made. Issue
// calls it with the scope lock held, after the supersede scan and before
// the first write; a non-nil error is returned unchanged and nothing is
// written.
type Check func(ctx context.Context, p Proposal) error

// Fleet is what a caller holding the fleet's commitment lock supplies to
// IssueInFleet. Key and Reach are stored on the lifecycle record.
type Fleet struct {
	Key   string
	Reach int
	Check Check
}

// IssueInFleet is Issue for a caller that holds fleet's commitment lock for
// the whole call: fleet.Check runs before any write, and the grant link,
// fleet key and reach are stored in the lifecycle record Issue creates, so
// the control and its link land in one write and share every undo path.
func (i *Issuer) IssueInFleet(ctx context.Context, req CreateRequest, fleet Fleet) (Result, error) {
	if fleet.Check == nil || fleet.Key == "" || fleet.Reach < 1 {
		return Result{}, ErrUncheckedCommitment
	}
	return i.issue(ctx, req, fleet)
}

func newProposal(scope Scope, ctrl sep2.DERControl, grant string, fleet Fleet, candidates []supersedeCandidate) Proposal {
	p := Proposal{
		Scope:     scope,
		FleetKey:  fleet.Key,
		Window:    commitment.Window{Start: ctrl.Interval.Start, Duration: ctrl.Interval.Duration},
		GrantMRID: grant,
		Reach:     fleet.Reach,
	}
	if ctrl.DERControlBase.OpModTargetW != nil {
		v := *ctrl.DERControlBase.OpModTargetW
		p.TargetW = &v
	}
	for _, c := range candidates {
		p.Supersedes = append(p.Supersedes, c.mrid)
	}
	return p
}

// Relink moves the execution at (scope, id) to grantMRID, changing no other
// field of its lifecycle record. It is for a caller holding the fleet's
// commitment lock that has already checked the execution against the new
// grant. On a *RefusalError or a plain error the stored record equals what
// Relink read, and a plain error returns that record (zero when the read
// itself failed). A failed restore is a *UndoError.
func (i *Issuer) Relink(ctx context.Context, scope Scope, id, grantMRID string) (LifecycleRecord, error) {
	scopeKey := scopeKeyOf(scope)
	unlock := i.lockScope(scopeKey)
	defer unlock()

	ctrl, err := i.controls.Get(ctx, scopeKey, id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return LifecycleRecord{}, refuse(RefusalControlNotFound)
		}
		return LifecycleRecord{}, fmt.Errorf("dercontrol: load control: %w", err)
	}
	before, err := i.lifecycles.Get(ctx, scopeKey, id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return LifecycleRecord{}, refuse(RefusalControlNotFound)
		}
		return LifecycleRecord{}, fmt.Errorf("dercontrol: load lifecycle: %w", err)
	}
	if before.GrantMRID == "" || grantMRID == "" {
		return before, ErrNotExecution
	}
	if err := ctx.Err(); err != nil {
		return before, err
	}

	lc := before
	lc.GrantMRID = grantMRID
	if err := i.lifecycles.Update(ctx, scopeKey, id, lc); err != nil {
		return i.restoreLifecycle(ctx, scope, id, ctrl.MRID, before, UndoStepRelink, err)
	}
	return lc, nil
}

// CancelLive cancels the control at (scope, id) and reports true, or
// reports false with a nil error when it is already cancelled, superseded
// or ended: cancelling a grant's executions needs each one stopped, not
// each one stopped by this call. Every other error is Cancel's.
func (i *Issuer) CancelLive(ctx context.Context, scope Scope, id, reason string) (bool, error) {
	_, err := i.Cancel(ctx, scope, id, reason)
	var refusal *RefusalError
	if errors.As(err, &refusal) {
		switch refusal.Code {
		case RefusalAlreadyCancelled, RefusalAlreadySuperseded, RefusalEnded:
			return false, nil
		}
	}
	return err == nil, err
}
