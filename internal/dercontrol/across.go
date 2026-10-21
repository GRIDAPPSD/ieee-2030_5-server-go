package dercontrol

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/derhref"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/handlers/sep2time"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
)

// #972: one control on an FSA's program template, written to every device's
// copy of the template with one mRID, one id and one creationTime.

// ErrAcrossGrant refuses a fan-out that names a grant: a grant is carried
// out by one device's control, checked against that grant alone.
var ErrAcrossGrant = errors.New("dercontrol: a control sent to several devices cannot carry out a grant")

// UndoStepStoreRecord names the write of an FSA control's record.
const UndoStepStoreRecord UndoStep = "store record"

// Target is one scope a control is written to, with the fleet whose
// commitment check bounds it there.
type Target struct {
	Scope Scope
	Fleet Fleet
}

// AcrossResult is what IssueAcross returns on success: the control as its
// record holds it, without an href, and one Result per target in scope key
// order.
type AcrossResult struct {
	ID      string
	Control sep2.DERControl
	Copies  []Result
}

// RecordWriter stores the record of a control IssueAcross is about to write
// to every target, and returns how to remove it. A failed write must leave no
// record.
type RecordWriter func(ctx context.Context, id string, ctrl sep2.DERControl) (undo func(context.Context) error, err error)

// acrossPlaceholderHref lets validate run its href-free checks for a
// fan-out, whose scopes come from the targets rather than the request.
const acrossPlaceholderHref = "/edev/0/fsa/0/derp/0"

// IssueAcross validates req, then, holding every target's scope lock, writes
// record and then one copy of the control to each target: its lifecycle
// record, the control and its supersede marks, as Issue writes them. The
// creationTime is one more than the largest in any target scope, so the copy
// is the newest event in each.
//
// On a nil error every write took effect. On a *RefusalError, or a check's
// error, nothing was written. On any other plain error every write was
// undone. A *UndoError names a copy, or the record, that could not be
// removed; the copies written before it and the record are kept with it, so
// the remainder is a prefix of a successful call. A target with no check,
// fleet key or reach is ErrUncheckedCommitment, and req.ExecutesGrant is
// ErrAcrossGrant.
func (i *Issuer) IssueAcross(ctx context.Context, req CreateRequest, targets []Target, record RecordWriter) (AcrossResult, error) {
	if req.ExecutesGrant != "" {
		return AcrossResult{}, ErrAcrossGrant
	}
	targets, err := sortTargets(targets)
	if err != nil {
		return AcrossResult{}, err
	}
	now := sep2time.Now().Unix()
	req.DERProgramHref = acrossPlaceholderHref
	base, err := i.validate(req, now)
	if err != nil {
		return AcrossResult{}, err
	}
	durationSeconds := int64(req.DurationSeconds)

	for _, k := range targetKeys(targets) {
		defer i.lockScope(k)()
	}

	existing := make([][]sep2.DERControl, len(targets))
	creationTime := now
	for n, t := range targets {
		if err := i.checkProgram(ctx, t.Scope); err != nil {
			return AcrossResult{}, err
		}
		list, err := i.controls.List(ctx, t.Scope.Key(), store.ListOptions{Unbounded: true})
		if err != nil {
			return AcrossResult{}, fmt.Errorf("dercontrol: list scope: %w", err)
		}
		existing[n] = list.Items
		for _, c := range list.Items {
			if c.CreationTime >= creationTime {
				creationTime = c.CreationTime + 1
			}
		}
	}

	start := creationTime
	if req.Start != nil {
		start = *req.Start
	}
	mrid, err := newMRID(*i.cfg.PEN)
	if err != nil {
		return AcrossResult{}, fmt.Errorf("dercontrol: generate mRID: %w", err)
	}
	id := sortableID(start, creationTime, mrid)
	recordCtrl := sep2.DERControl{DERControlBase: base}
	recordCtrl.MRID = mrid
	recordCtrl.Description = req.Description
	recordCtrl.CreationTime = creationTime
	recordCtrl.Interval = &sep2.DateTimeInterval{Start: start, Duration: req.DurationSeconds}

	plans := make([]copyPlan, len(targets))
	for n, t := range targets {
		ctrl := copyOf(recordCtrl, t.Scope, id)
		candidates, err := i.computeSupersedes(ctx, t.Scope.Key(), existing[n], base, start, start+durationSeconds)
		if err != nil {
			return AcrossResult{}, err
		}
		if err := t.Fleet.Check(ctx, newProposal(t.Scope, ctrl, "", t.Fleet, candidates)); err != nil {
			return AcrossResult{}, err
		}
		plans[n] = copyPlan{target: t, ctrl: ctrl, candidates: candidates, link: LifecycleRecord{FleetKey: t.Fleet.Key, Reach: t.Fleet.Reach}}
	}

	if err := ctx.Err(); err != nil {
		return AcrossResult{}, err
	}
	undoRecord, err := record(ctx, id, recordCtrl)
	if err != nil {
		return AcrossResult{}, fmt.Errorf("dercontrol: store record: %w", err)
	}

	res := AcrossResult{ID: id, Control: recordCtrl}
	written, err := i.writeCopies(ctx, plans, id)
	if err != nil {
		return AcrossResult{}, i.undoAcross(ctx, written, err, undoRecord)
	}
	for _, w := range written {
		res.Copies = append(res.Copies, Result{Scope: w.plan.target.Scope, ID: id, Href: w.plan.ctrl.Href, Control: w.plan.ctrl, Supersedes: w.marked})
	}
	return res, nil
}

// Replica is a control already recorded on an FSA, to be written to one more
// scope with the record's id, mRID, creationTime and interval.
type Replica struct {
	Target  Target
	ID      string
	Control sep2.DERControl
}

// Replicate writes each replica that its scope does not already hold, in
// order, holding every scope lock: a device assigned to an FSA after its
// controls were issued, or a copy a restart found missing. A replica marks
// the older controls of its scope it supersedes, and is itself marked
// superseded by a newer one already there. The results list the replicas
// written. The error contract is IssueAcross's, without a record; a stored
// replica is never rewritten.
func (i *Issuer) Replicate(ctx context.Context, replicas []Replica) ([]Result, error) {
	for _, r := range replicas {
		if r.Target.Fleet.Check == nil || r.Target.Fleet.Key == "" || r.Target.Fleet.Reach < 1 {
			return nil, ErrUncheckedCommitment
		}
		if r.Control.Interval == nil || r.Control.DERControlBase == nil {
			return nil, fmt.Errorf("dercontrol: replica %s has no interval or control base", r.ID)
		}
	}
	keys := make([]string, 0, len(replicas))
	for _, r := range replicas {
		keys = append(keys, r.Target.Scope.Key())
	}
	slices.Sort(keys)
	for _, k := range slices.Compact(keys) {
		defer i.lockScope(k)()
	}

	var written []writtenCopy
	for _, r := range replicas {
		plan, present, err := i.planReplica(ctx, r)
		if err == nil && !present {
			var w []writtenCopy
			w, err = i.writeCopies(ctx, []copyPlan{plan}, r.ID)
			written = append(written, w...)
		}
		if err != nil {
			return nil, i.undoAcross(ctx, written, err, nil)
		}
	}
	out := make([]Result, 0, len(written))
	for _, w := range written {
		out = append(out, Result{Scope: w.plan.target.Scope, ID: w.id, Href: w.plan.ctrl.Href, Control: w.plan.ctrl, Supersedes: w.marked})
	}
	return out, nil
}

// planReplica reads r's scope and decides what writing r there takes, or
// reports that the scope already holds it.
func (i *Issuer) planReplica(ctx context.Context, r Replica) (copyPlan, bool, error) {
	scope := r.Target.Scope
	scopeKey := scope.Key()
	if err := i.checkProgram(ctx, scope); err != nil {
		return copyPlan{}, false, err
	}
	switch _, err := i.controls.Get(ctx, scopeKey, r.ID); {
	case err == nil:
		return copyPlan{}, true, nil
	case !errors.Is(err, store.ErrNotFound):
		return copyPlan{}, false, fmt.Errorf("dercontrol: load control: %w", err)
	}
	list, err := i.controls.List(ctx, scopeKey, store.ListOptions{Unbounded: true})
	if err != nil {
		return copyPlan{}, false, fmt.Errorf("dercontrol: list scope: %w", err)
	}
	ctrl := copyOf(r.Control, scope, r.ID)
	start := ctrl.Interval.Start
	end := start + int64(ctrl.Interval.Duration)

	// Only an older event is superseded by this one; a newer event already
	// in the scope supersedes it instead, from that event's start.
	var older, newer []sep2.DERControl
	for _, c := range list.Items {
		switch {
		case c.CreationTime < ctrl.CreationTime:
			older = append(older, c)
		case c.CreationTime > ctrl.CreationTime:
			newer = append(newer, c)
		}
	}
	candidates, err := i.computeSupersedes(ctx, scopeKey, older, ctrl.DERControlBase, start, end)
	if err != nil {
		return copyPlan{}, false, err
	}
	link := LifecycleRecord{FleetKey: r.Target.Fleet.Key, Reach: r.Target.Fleet.Reach}
	if err := i.supersededByNewer(ctx, scopeKey, newer, ctrl, &link); err != nil {
		return copyPlan{}, false, err
	}
	if err := r.Target.Fleet.Check(ctx, newProposal(scope, ctrl, "", r.Target.Fleet, candidates)); err != nil {
		return copyPlan{}, false, err
	}
	return copyPlan{target: r.Target, ctrl: ctrl, candidates: candidates, link: link}, false, nil
}

// supersededByNewer marks lc superseded by the earliest-starting event among
// newer whose function set equals ctrl's and whose interval overlaps it, the
// mark that event would have written had ctrl been stored first. A newer
// event that is cancelled, or has no lifecycle record, marks nothing, as in
// Issue.
func (i *Issuer) supersededByNewer(ctx context.Context, scopeKey string, newer []sep2.DERControl, ctrl sep2.DERControl, lc *LifecycleRecord) error {
	set := functionSetKey(ctrl.DERControlBase)
	start := ctrl.Interval.Start
	end := start + int64(ctrl.Interval.Duration)
	for _, c := range newer {
		if c.Interval == nil || functionSetKey(c.DERControlBase) != set {
			continue
		}
		cEnd := c.Interval.Start + int64(c.Interval.Duration)
		if !(c.Interval.Start < end && start < cEnd) {
			continue
		}
		id, ok := derhref.ControlID(c.Href)
		if !ok {
			continue
		}
		clc, err := i.lifecycles.Get(ctx, scopeKey, id)
		if errors.Is(err, store.ErrNotFound) {
			continue
		}
		if err != nil {
			return fmt.Errorf("dercontrol: load lifecycle: %w", err)
		}
		if clc.cancelled() {
			continue
		}
		if lc.SupersededAt == nil || c.Interval.Start < *lc.SupersededAt {
			lc.SupersededAt = ptrInt64(c.Interval.Start)
			lc.SupersededBy = c.MRID
		}
	}
	return nil
}

// copyPlan is one copy's writes, decided before the first of them.
type copyPlan struct {
	target     Target
	ctrl       sep2.DERControl
	candidates []supersedeCandidate
	link       LifecycleRecord
}

// writtenCopy is a copy whose every write took effect.
type writtenCopy struct {
	plan   copyPlan
	id     string
	marked []string
}

// writeCopies writes each plan in order: the lifecycle record, the control,
// then the marks, as Issue does. On the first failure it undoes that copy's
// own writes and returns the copies written before it with the error: a
// plain error when the failed copy left nothing, a *UndoError when it did.
func (i *Issuer) writeCopies(ctx context.Context, plans []copyPlan, id string) ([]writtenCopy, error) {
	var written []writtenCopy
	for _, p := range plans {
		scope := p.target.Scope
		scopeKey := scope.Key()
		named := nameUndo(scope, p.ctrl.MRID)
		if err := i.lifecycles.Create(ctx, scopeKey, id, p.link); err != nil {
			_, err = named(i.undoLifecycleCreateFailure(ctx, scopeKey, id, err))
			return written, err
		}
		if err := i.controls.Create(ctx, scopeKey, id, p.ctrl); err != nil {
			_, err = named(i.undoControlCreateFailure(ctx, scopeKey, id, err))
			return written, err
		}
		marked, attempted, err := i.applySupersedes(ctx, scopeKey, p.candidates, p.ctrl.Interval.Start, p.ctrl.MRID)
		if err != nil {
			_, err = named(i.undoMarkFailure(ctx, scopeKey, id, attempted, err))
			return written, err
		}
		written = append(written, writtenCopy{plan: p, id: id, marked: marked})
	}
	return written, nil
}

// undoAcross answers a fan-out or replication whose writes stopped at cause.
// A *UndoError cause keeps everything already written. Otherwise the written
// copies are removed newest first, and then the record; the first removal
// that fails is returned as a *UndoError and keeps what it has not reached.
func (i *Issuer) undoAcross(ctx context.Context, written []writtenCopy, cause error, undoRecord func(context.Context) error) error {
	var undo *UndoError
	if errors.As(cause, &undo) {
		return cause
	}
	uctx, cancel := undoContext(ctx)
	defer cancel()
	for n := len(written) - 1; n >= 0; n-- {
		if err := i.removeCopy(uctx, written[n], cause); err != nil {
			return err
		}
	}
	if undoRecord != nil {
		if err := undoRecord(uctx); err != nil {
			return &UndoError{Step: UndoStepStoreRecord, cause: cause, reverts: []error{err}}
		}
	}
	return cause
}

// removeCopy reverts one fully written copy, every candidate of which was
// marked: its marks, then the control,
// then its lifecycle record, keeping the control while a mark may still name
// it.
func (i *Issuer) removeCopy(ctx context.Context, w writtenCopy, cause error) error {
	scope := w.plan.target.Scope
	scopeKey := scope.Key()
	fail := func(controlKept bool, unreverted []string, reverts []error) error {
		return &UndoError{Step: UndoStepStoreControl, ControlKept: controlKept, LifecycleKept: true, ID: w.id,
			Scope: scope, MRID: w.plan.ctrl.MRID, UnrevertedIDs: unreverted, cause: cause, reverts: reverts}
	}
	var unreverted []string
	var reverts []error
	for _, cand := range w.plan.candidates {
		if err := i.lifecycles.Update(ctx, scopeKey, cand.id, cand.before); !undoWriteOK(err) {
			unreverted = append(unreverted, cand.id)
			reverts = append(reverts, err)
		}
	}
	if len(unreverted) > 0 {
		return fail(true, unreverted, reverts)
	}
	if err := i.controls.Delete(ctx, scopeKey, w.id); !undoWriteOK(err) {
		return fail(true, nil, []error{err})
	}
	if err := i.lifecycles.Delete(ctx, scopeKey, w.id); !undoWriteOK(err) {
		return fail(false, nil, []error{err})
	}
	return nil
}

// checkProgram refuses a scope whose program is gone or whose control list
// link names another scope, the checks Issue makes before writing.
func (i *Issuer) checkProgram(ctx context.Context, scope Scope) error {
	program, err := i.programs.Get(ctx, scope.EndDeviceID, scope.DERProgramID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return refuse(RefusalProgramNotFound)
		}
		return fmt.Errorf("dercontrol: load program: %w", err)
	}
	if program.DERControlListLink == nil {
		return refuse(RefusalNoControlListLink)
	}
	edev, fsa, derp, ok := parseControlListHref(program.DERControlListLink.Href)
	if !ok || edev != scope.EndDeviceID || fsa != scope.FSAID || derp != scope.DERProgramID {
		return refuse(RefusalNoControlListLink)
	}
	return nil
}

// copyOf is ctrl as stored in scope under id.
func copyOf(ctrl sep2.DERControl, scope Scope, id string) sep2.DERControl {
	c := ctrl
	if ctrl.DERControlBase != nil {
		b := ctrl.DERControlBase.Copy()
		c.DERControlBase = &b
	}
	if ctrl.Interval != nil {
		iv := *ctrl.Interval
		c.Interval = &iv
	}
	c.Href = scope.ControlListHref() + "/" + id
	return c
}

// sortTargets returns targets in scope key order, refusing a duplicate scope
// and a target no commitment check bounds.
func sortTargets(targets []Target) ([]Target, error) {
	out := slices.Clone(targets)
	slices.SortFunc(out, func(a, b Target) int { return strings.Compare(a.Scope.Key(), b.Scope.Key()) })
	for n, t := range out {
		if t.Fleet.Check == nil || t.Fleet.Key == "" || t.Fleet.Reach < 1 {
			return nil, ErrUncheckedCommitment
		}
		if n > 0 && out[n-1].Scope == t.Scope {
			return nil, fmt.Errorf("dercontrol: scope %s named twice", t.Scope.Key())
		}
	}
	return out, nil
}

func targetKeys(targets []Target) []string {
	keys := make([]string, 0, len(targets))
	for _, t := range targets {
		keys = append(keys, t.Scope.Key())
	}
	return keys
}
