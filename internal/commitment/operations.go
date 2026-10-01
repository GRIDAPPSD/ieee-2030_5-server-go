package commitment

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"time"
)

// ErrUndo marks a failed Revise step whose rollback also failed: the stores
// may hold part of the revision, and the caller must report an internal
// error rather than a refusal. CancelGrant never undoes and never returns
// it. A plain error from either may still follow partial progress (some
// executions cancelled under a live grant); calling again finishes it.
var ErrUndo = errors.New("commitment: undo failed; the stores may hold part of the change")

// ErrNothingWritten matches an ExecutionWriter error built with NothingWritten.
// Revise acts on it and never returns it: a caller reads Revise's error for
// the cause and for ErrUndo, never for this.
var ErrNothingWritten = errors.New("commitment: the write was refused and changed nothing")

// NothingWrittenError is a write error from a step that stopped before
// changing anything, so Revise needs no undo for it.
type NothingWrittenError struct{ Err error }

// NothingWritten marks err as a write that changed nothing.
func NothingWritten(err error) error { return &NothingWrittenError{Err: err} }

func (e *NothingWrittenError) Error() string        { return e.Err.Error() }
func (e *NothingWrittenError) Unwrap() error        { return e.Err }
func (e *NothingWrittenError) Is(target error) bool { return target == ErrNothingWritten }

// unmarked drops the NothingWritten mark, so the sentinel cannot reach a
// caller inside an error that also reports ErrUndo.
func unmarked(err error) error {
	var nw *NothingWrittenError
	if errors.As(err, &nw) {
		return nw.Err
	}
	return err
}

// ErrBadReplacement refuses a Revise whose replacement is not a revision of
// the old response. It is the caller's error, never a conflict.
var ErrBadReplacement = errors.New("commitment: not a revision of the grant")

// ExecutionWriter performs the DER control writes of a grant's lifecycle.
type ExecutionWriter interface {
	// CancelExecution stops c. One already cancelled, superseded or ended
	// is done, not an error.
	CancelExecution(ctx context.Context, c Control, reason string) error
	// RelinkExecution moves c to grantMRID, changing nothing else. A failure
	// that wrote nothing is returned through NothingWritten.
	RelinkExecution(ctx context.Context, c Control, grantMRID string) error
}

// GrantWriter records a grant's cancellation.
type GrantWriter interface {
	MarkCancelled(ctx context.Context, g Grant, reason string, now int64) error
}

// Writers is what CancelGrant and Revise write through. The ledger itself
// reads only; the writers belong to the stores' owners.
type Writers struct {
	Executions ExecutionWriter
	Grants     GrantWriter
}

func (w Writers) valid() error {
	if w.Executions == nil || w.Grants == nil {
		return errors.New("commitment: both writers are required")
	}
	return nil
}

// Replacement is the response a revision stores in place of the old one.
// Grant is the new response as the ledger checks it; Create stores it and
// Delete removes it again when a later step fails. Create must be atomic:
// one that fails must have stored nothing, since it is not undone.
type Replacement struct {
	Grant  Grant
	Create func(ctx context.Context) error
	Delete func(ctx context.Context) error
}

// undoTimeout bounds each step of a rollback, which runs even when the
// caller's context is done: the forward writes it reverses were made under
// that context.
const undoTimeout = 5 * time.Second

// CancelGrant cancels grantMRID and every execution of it, executions
// first, so a failure part way leaves live executions of a live grant and
// never live executions of a cancelled one. Cancelled executions are not
// restored on a later failure; calling again finishes the job. An absent
// grant wraps ErrNoGrant; a cancelled one is ConflictGrantNotLive.
func (l *Ledger) CancelGrant(ctx context.Context, w Writers, grantMRID, reason string, now int64) error {
	if err := w.valid(); err != nil {
		return err
	}
	fleet, err := l.fleetOfGrant(ctx, grantMRID)
	if err != nil {
		return err
	}
	return l.Within(ctx, []string{fleet}, func(View) error {
		g, err := l.liveGrant(ctx, grantMRID, fleet)
		if err != nil {
			return err
		}
		execs, err := l.liveExecutions(ctx, g.MRID)
		if err != nil {
			return err
		}
		if err := cancelExecutions(ctx, w.Executions, execs, reason); err != nil {
			return err
		}
		if err := w.Grants.MarkCancelled(ctx, g, reason, now); err != nil {
			return fmt.Errorf("commitment: marking grant %s cancelled: %w", g.MRID, err)
		}
		return nil
	})
}

// Revise replaces the live grant oldMRID with the response build returns,
// under the fleet lock. A denial, a response with no interval and a
// cancelled one are not live grants and are refused grant_not_live. build is
// called with the old grant once it is read live, with the fleet lock held:
// it must not call the ledger, whose lock is not reentrant. The replacement
// must share the old response's subject and have a strictly later
// creationTime (IEEE 2030.5-2023 10.2.2.3 d and e). A replacement whose window has duration zero is a denial: the old
// grant's executions are cancelled and nothing is checked. Otherwise the new
// window must be free on the fleet apart from the old grant, the new grant
// must be executable, and every live execution must still fit; the first
// that does not, in start order then mRID, is the refusal's MRID, and
// nothing is written.
//
// The writes run in an order whose every prefix is legal: create the new
// response, relink each execution, mark the old grant cancelled. A failed
// step undoes the ones before it in reverse; a failed undo wraps ErrUndo.
func (l *Ledger) Revise(ctx context.Context, w Writers, oldMRID, reason string, now int64, build func(old Grant) (Replacement, error)) error {
	if err := w.valid(); err != nil {
		return err
	}
	if build == nil {
		return errors.New("commitment: Revise needs a build function")
	}
	fleet, err := l.fleetOfGrant(ctx, oldMRID)
	if err != nil {
		return err
	}
	return l.Within(ctx, []string{fleet}, func(v View) error {
		old, err := l.liveGrant(ctx, oldMRID, fleet)
		if err != nil {
			return err
		}
		if old.Window == nil || old.Window.Duration == 0 {
			return &ConflictError{Code: ConflictGrantNotLive, MRID: old.MRID}
		}
		rep, err := build(old)
		if err != nil {
			return fmt.Errorf("commitment: building the revision of %s: %w", old.MRID, err)
		}
		next, err := replacementGrant(old, rep)
		if err != nil {
			return err
		}
		execs, err := l.liveExecutions(ctx, old.MRID)
		if err != nil {
			return err
		}

		if next.Window != nil && next.Window.Duration == 0 {
			return reviseToDenial(ctx, w, old, rep, execs, reason, now)
		}
		if err := v.CheckGrant(ctx, fleet, next.Window, old.MRID); err != nil {
			return err
		}
		if err := fitsRevision(next, execs); err != nil {
			return err
		}
		return reviseWrites(ctx, w, old, rep, execs, reason, now)
	})
}

// fleetOfGrant reads the grant's fleet before any lock is taken; the grant
// is read again under the lock, since it may change in between.
func (l *Ledger) fleetOfGrant(ctx context.Context, mrid string) (string, error) {
	g, err := l.grants.Grant(ctx, mrid)
	if err != nil {
		return "", fmt.Errorf("commitment: reading grant %s: %w", mrid, err)
	}
	return g.FleetKey, nil
}

func (l *Ledger) liveGrant(ctx context.Context, mrid, fleet string) (Grant, error) {
	g, err := l.grants.Grant(ctx, mrid)
	if err != nil {
		return Grant{}, fmt.Errorf("commitment: reading grant %s: %w", mrid, err)
	}
	if g.FleetKey != fleet {
		return Grant{}, fmt.Errorf("commitment: grant %s moved from fleet %s to %s during the call", mrid, fleet, g.FleetKey)
	}
	if g.CancelledAt != nil {
		return Grant{}, &ConflictError{Code: ConflictGrantNotLive, MRID: mrid}
	}
	return g, nil
}

// liveExecutions returns the grant's uncancelled executions in start order,
// then mRID, so "the first execution" means the same one on every call
// whatever order the source lists them in.
func (l *Ledger) liveExecutions(ctx context.Context, grantMRID string) ([]Control, error) {
	all, err := l.controls.ExecutionsOf(ctx, grantMRID)
	if err != nil {
		return nil, fmt.Errorf("commitment: reading executions of grant %s: %w", grantMRID, err)
	}
	live := slices.DeleteFunc(slices.Clone(all), func(c Control) bool { return c.Cancelled })
	slices.SortFunc(live, func(a, b Control) int {
		return cmp.Or(cmp.Compare(a.Window.Start, b.Window.Start), cmp.Compare(a.MRID, b.MRID))
	})
	return live, nil
}

// replacementGrant checks that rep is a revision of old: a new mRID under
// the same EndDevice, live, with both writes. It returns the grant with the
// old grant's fleet, which a response under the same EndDevice shares.
func replacementGrant(old Grant, rep Replacement) (Grant, error) {
	g := rep.Grant
	switch {
	case rep.Create == nil || rep.Delete == nil:
		return Grant{}, fmt.Errorf("%w: a replacement needs Create and Delete", ErrBadReplacement)
	case g.MRID == "" || g.MRID == old.MRID:
		return Grant{}, fmt.Errorf("%w: a replacement of %s needs its own mRID, got %q", ErrBadReplacement, old.MRID, g.MRID)
	case g.EndDeviceID != old.EndDeviceID:
		return Grant{}, fmt.Errorf("%w: replacement %s is under EndDevice %q, the grant it replaces under %q", ErrBadReplacement, g.MRID, g.EndDeviceID, old.EndDeviceID)
	case g.Subject == "" || g.Subject != old.Subject:
		return Grant{}, fmt.Errorf("%w: replacement %s has subject %q, the grant it replaces %q", ErrBadReplacement, g.MRID, g.Subject, old.Subject)
	case g.CreationTime <= old.CreationTime:
		return Grant{}, fmt.Errorf("%w: replacement %s was created at %d, not after %d", ErrBadReplacement, g.MRID, g.CreationTime, old.CreationTime)
	case g.CancelledAt != nil:
		return Grant{}, fmt.Errorf("%w: replacement %s is already cancelled", ErrBadReplacement, g.MRID)
	}
	g.FleetKey = old.FleetKey
	return g, nil
}

// fitsRevision adds the executions to next one at a time. The rules only
// tighten as executions are added, so the first prefix that fails is the
// one ending in the execution that does not fit, and that execution is
// named whichever rule broke, the energy and power sums included. With no
// executions, next is still checked, so a revision cannot leave a live grant
// that no control could carry out.
func fitsRevision(next Grant, execs []Control) error {
	if len(execs) == 0 {
		return FitsGrant(next, nil, nil)
	}
	for i, c := range execs {
		err := FitsGrant(next, execs[:i+1], nil)
		var conflict *ConflictError
		if errors.As(err, &conflict) {
			return &ConflictError{Code: conflict.Code, MRID: c.MRID}
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func cancelExecutions(ctx context.Context, w ExecutionWriter, execs []Control, reason string) error {
	for _, c := range execs {
		if err := w.CancelExecution(ctx, c, reason); err != nil {
			return fmt.Errorf("commitment: cancelling execution %s: %w", c.MRID, err)
		}
	}
	return nil
}

func reviseWrites(ctx context.Context, w Writers, old Grant, rep Replacement, execs []Control, reason string, now int64) error {
	if err := rep.Create(ctx); err != nil {
		return fmt.Errorf("commitment: storing revision %s: %w", rep.Grant.MRID, err)
	}
	// touched includes an execution whose relink failed, unless that was a
	// refusal that wrote nothing: a store whose own rollback failed may have
	// kept the new link, and relinking a record that never moved back to the
	// old grant is harmless.
	var touched []Control
	undo := func(cause error) error {
		var failed []error
		relinkFailed := false
		for _, c := range slices.Backward(touched) {
			if err := undoStep(ctx, func(uctx context.Context) error { return w.Executions.RelinkExecution(uctx, c, old.MRID) }); err != nil {
				relinkFailed = true
				failed = append(failed, fmt.Errorf("relinking %s back to %s: %w", c.MRID, old.MRID, unmarked(err)))
			}
		}
		// An execution that may still name the revision keeps it stored:
		// deleting it would leave the control pointing at a grant no walk
		// of the request's responses can reach.
		if !relinkFailed {
			if err := undoStep(ctx, rep.Delete); err != nil {
				failed = append(failed, fmt.Errorf("deleting revision %s: %w", rep.Grant.MRID, err))
			}
		}
		if len(failed) > 0 {
			return fmt.Errorf("%w: %w: %w", ErrUndo, cause, errors.Join(failed...))
		}
		return cause
	}

	for _, c := range execs {
		err := w.Executions.RelinkExecution(ctx, c, rep.Grant.MRID)
		if err == nil || !errors.Is(err, ErrNothingWritten) {
			touched = append(touched, c)
		}
		if err != nil {
			return undo(fmt.Errorf("commitment: relinking execution %s to %s: %w", c.MRID, rep.Grant.MRID, unmarked(err)))
		}
	}
	if err := w.Grants.MarkCancelled(ctx, old, reason, now); err != nil {
		return undo(fmt.Errorf("commitment: marking grant %s cancelled: %w", old.MRID, err))
	}
	return nil
}

// undoStep runs one rollback write with its own deadline and without the
// caller's cancellation, so one slow step cannot starve the next.
func undoStep(ctx context.Context, step func(context.Context) error) error {
	uctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), undoTimeout)
	defer cancel()
	return step(uctx)
}

// reviseToDenial cancels the executions first, as CancelGrant does, then
// stores the denial and marks the old grant. Only the denial is undone on a
// failure: a cancelled execution of a live grant is legal and a retry
// finishes it.
func reviseToDenial(ctx context.Context, w Writers, old Grant, rep Replacement, execs []Control, reason string, now int64) error {
	if err := cancelExecutions(ctx, w.Executions, execs, reason); err != nil {
		return err
	}
	if err := rep.Create(ctx); err != nil {
		return fmt.Errorf("commitment: storing denial %s: %w", rep.Grant.MRID, err)
	}
	if err := w.Grants.MarkCancelled(ctx, old, reason, now); err != nil {
		cause := fmt.Errorf("commitment: marking grant %s cancelled: %w", old.MRID, err)
		if derr := undoStep(ctx, rep.Delete); derr != nil {
			return fmt.Errorf("%w: %w: deleting denial %s: %w", ErrUndo, cause, rep.Grant.MRID, derr)
		}
		return cause
	}
	return nil
}

// CompleteRevision finishes a Revise that stopped between its writes, so the
// request's chain holds the live responses oldMRIDs (oldest first) and the
// newer tipMRID. It runs under the fleet lock of the tip. A tip that is a
// denial rolls forward without a check, as Revise does: the old grants'
// executions are cancelled and the old grants marked cancelled. Any other
// tip is checked as Revise checked it, with every live execution of the old
// grants and of the tip added; when they fit, executions still on an older
// grant move to the tip and the older grants are marked cancelled
// (rolledForward is true). When one does not fit, executions on the tip move
// back to the newest older grant and deleteTip removes the tip.
//
// Each order of writes is the one Revise uses, so a failure part way leaves
// a state a second call finishes. A grant that is already cancelled, the
// tip included, is refused as not live.
func (l *Ledger) CompleteRevision(ctx context.Context, w Writers, oldMRIDs []string, tipMRID string, now int64, deleteTip func(context.Context) error) (rolledForward bool, err error) {
	if err := w.valid(); err != nil {
		return false, err
	}
	if len(oldMRIDs) == 0 || tipMRID == "" || deleteTip == nil {
		return false, errors.New("commitment: CompleteRevision needs the older grants, the tip and a way to delete it")
	}
	fleet, err := l.fleetOfGrant(ctx, tipMRID)
	if err != nil {
		return false, err
	}
	err = l.Within(ctx, []string{fleet}, func(View) error {
		tip, err := l.liveGrant(ctx, tipMRID, fleet)
		if err != nil {
			return err
		}
		olds := make([]Grant, 0, len(oldMRIDs))
		var onOld []Control
		for _, mrid := range oldMRIDs {
			g, err := l.liveGrant(ctx, mrid, fleet)
			if err != nil {
				return err
			}
			execs, err := l.liveExecutions(ctx, g.MRID)
			if err != nil {
				return err
			}
			olds = append(olds, g)
			onOld = append(onOld, execs...)
		}
		onTip, err := l.liveExecutions(ctx, tip.MRID)
		if err != nil {
			return err
		}

		if tip.Window != nil && tip.Window.Duration == 0 {
			if err := cancelExecutions(ctx, w.Executions, onOld, "revision completed after restart"); err != nil {
				return err
			}
			rolledForward = true
			return markAllCancelled(ctx, w, olds, "revision completed after restart", now)
		}

		all := slices.Concat(onOld, onTip)
		slices.SortFunc(all, func(a, b Control) int {
			return cmp.Or(cmp.Compare(a.Window.Start, b.Window.Start), cmp.Compare(a.MRID, b.MRID))
		})
		fitErr := fitsRevision(tip, all)
		var conflict *ConflictError
		if fitErr != nil && !errors.As(fitErr, &conflict) {
			return fmt.Errorf("commitment: checking revision %s: %w", tip.MRID, fitErr)
		}

		if fitErr == nil {
			for _, c := range onOld {
				if err := w.Executions.RelinkExecution(ctx, c, tip.MRID); err != nil {
					return fmt.Errorf("commitment: relinking execution %s to %s: %w", c.MRID, tip.MRID, unmarked(err))
				}
			}
			rolledForward = true
			return markAllCancelled(ctx, w, olds, "revision completed after restart", now)
		}

		newest := olds[len(olds)-1]
		for _, c := range onTip {
			if err := w.Executions.RelinkExecution(ctx, c, newest.MRID); err != nil {
				return fmt.Errorf("commitment: relinking execution %s back to %s: %w", c.MRID, newest.MRID, unmarked(err))
			}
		}
		if err := deleteTip(ctx); err != nil {
			return fmt.Errorf("commitment: deleting revision %s: %w", tip.MRID, err)
		}
		return nil
	})
	return rolledForward && err == nil, err
}

func markAllCancelled(ctx context.Context, w Writers, grants []Grant, reason string, now int64) error {
	for _, g := range grants {
		if err := w.Grants.MarkCancelled(ctx, g, reason, now); err != nil {
			return fmt.Errorf("commitment: marking grant %s cancelled: %w", g.MRID, err)
		}
	}
	return nil
}
