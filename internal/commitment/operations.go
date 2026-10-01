package commitment

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"time"
)

// ErrUndo marks a failed CancelGrant or Revise step whose rollback also
// failed: the stores may hold part of the change, and the caller must
// report an internal error rather than a refusal.
var ErrUndo = errors.New("commitment: undo failed; the stores may hold part of the change")

// ExecutionWriter performs the DER control writes of a grant's lifecycle.
type ExecutionWriter interface {
	// CancelExecution stops c. One already cancelled, superseded or ended
	// is done, not an error.
	CancelExecution(ctx context.Context, c Control, reason string) error
	// RelinkExecution moves c to grantMRID, changing nothing else.
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
// Delete removes it again when a later step fails.
type Replacement struct {
	Grant  Grant
	Create func(ctx context.Context) error
	Delete func(ctx context.Context) error
}

// undoTimeout bounds a rollback, which runs even when the caller's context
// is done: the forward writes it reverses were made under that context.
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
// under the fleet lock. build is called with the old grant once it is read
// live. A replacement whose window has duration zero is a denial: the old
// grant's executions are cancelled and nothing is checked. Otherwise the new
// window must be free on the fleet apart from the old grant, and every live
// execution must still fit; the first that does not, in start order then
// mRID, is the refusal's MRID, and nothing is written.
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
		return Grant{}, errors.New("commitment: a replacement needs Create and Delete")
	case g.MRID == "" || g.MRID == old.MRID:
		return Grant{}, fmt.Errorf("commitment: a replacement of %s needs its own mRID, got %q", old.MRID, g.MRID)
	case g.EndDeviceID != old.EndDeviceID:
		return Grant{}, fmt.Errorf("commitment: replacement %s is under EndDevice %q, the grant it replaces under %q", g.MRID, g.EndDeviceID, old.EndDeviceID)
	case g.CancelledAt != nil:
		return Grant{}, fmt.Errorf("commitment: replacement %s is already cancelled", g.MRID)
	}
	g.FleetKey = old.FleetKey
	return g, nil
}

// fitsRevision adds the executions to next one at a time. The rules only
// tighten as executions are added, so the first prefix that fails is the
// one ending in the execution that does not fit, and that execution is
// named whichever rule broke, the energy and power sums included.
func fitsRevision(next Grant, execs []Control) error {
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
	var relinked []Control
	undo := func(cause error) error {
		uctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), undoTimeout)
		defer cancel()
		var failed []error
		for _, c := range slices.Backward(relinked) {
			if err := w.Executions.RelinkExecution(uctx, c, old.MRID); err != nil {
				failed = append(failed, fmt.Errorf("relinking %s back to %s: %w", c.MRID, old.MRID, err))
			}
		}
		if err := rep.Delete(uctx); err != nil {
			failed = append(failed, fmt.Errorf("deleting revision %s: %w", rep.Grant.MRID, err))
		}
		if len(failed) > 0 {
			return fmt.Errorf("%w: %w: %w", ErrUndo, cause, errors.Join(failed...))
		}
		return cause
	}

	for _, c := range execs {
		if err := w.Executions.RelinkExecution(ctx, c, rep.Grant.MRID); err != nil {
			return undo(fmt.Errorf("commitment: relinking execution %s to %s: %w", c.MRID, rep.Grant.MRID, err))
		}
		relinked = append(relinked, c)
	}
	if err := w.Grants.MarkCancelled(ctx, old, reason, now); err != nil {
		return undo(fmt.Errorf("commitment: marking grant %s cancelled: %w", old.MRID, err))
	}
	return nil
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
		uctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), undoTimeout)
		defer cancel()
		if derr := rep.Delete(uctx); derr != nil {
			return fmt.Errorf("%w: %w: deleting denial %s: %w", ErrUndo, cause, rep.Grant.MRID, derr)
		}
		return cause
	}
	return nil
}
