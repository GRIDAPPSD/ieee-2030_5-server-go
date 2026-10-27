package dercontrol

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

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

// IssueAcross is IssueAcrossAll for one control, with no check, returning
// that control's result or its error unwrapped.
func (i *Issuer) IssueAcross(ctx context.Context, req CreateRequest, targets []Target, record RecordWriter) (AcrossResult, error) {
	res, err := i.IssueAcrossAll(ctx, []Across{{Req: req, Targets: targets, Record: record}}, nil)
	if items, ok := err.(AcrossItemErrors); ok && len(items) == 1 {
		return AcrossResult{}, items[0].Err
	}
	if err != nil {
		return AcrossResult{}, err
	}
	return res[0], nil
}

// Across is one control IssueAcrossAll writes: its request, the targets it
// is written to and the writer of its record. A request with no Start starts
// StartOffset seconds after the call's first creationTime, so controls of
// one call can start together or follow each other exactly. FleetTotal marks
// a control whose copies carry one fleet-wide target, counted once.
type Across struct {
	Req         CreateRequest
	Targets     []Target
	Record      RecordWriter
	StartOffset int64
	FleetTotal  bool
}

// AcrossCheck sees every control of an IssueAcrossAll call with its resolved
// interval, indexed by item, before any copy is planned, under every scope
// lock. An item refused before its interval was resolved has a zero
// control. An error refuses the call with nothing written.
type AcrossCheck func(controls []sep2.DERControl) error

// AcrossItemError is why one item of an IssueAcrossAll call could not be
// planned: a refusal, a commitment check's error, or a store read failure.
type AcrossItemError struct {
	Item int
	Err  error
}

func (e AcrossItemError) Error() string { return fmt.Sprintf("dercontrol: item %d: %v", e.Item, e.Err) }

func (e AcrossItemError) Unwrap() error { return e.Err }

// AcrossItemErrors holds every item of an IssueAcrossAll call that could not
// be planned, in item order. Nothing was written.
type AcrossItemErrors []AcrossItemError

func (e AcrossItemErrors) Error() string {
	parts := make([]string, len(e))
	for n, it := range e {
		parts[n] = it.Error()
	}
	return strings.Join(parts, "; ")
}

func (e AcrossItemErrors) Unwrap() []error {
	out := make([]error, len(e))
	for n, it := range e {
		out[n] = it
	}
	return out
}

// with is e and more in item order.
func (e AcrossItemErrors) with(more AcrossItemErrors) AcrossItemErrors {
	out := append(slices.Clone(e), more...)
	slices.SortStableFunc(out, func(a, b AcrossItemError) int { return a.Item - b.Item })
	return out
}

// ErrAcrossOverlap refuses two items of one call whose controls share a
// scope and overlap in time: neither would supersede the other.
var ErrAcrossOverlap = errors.New("dercontrol: two controls of one call overlap in a scope they share")

// IssueAcrossAll validates every item, then, holding every target's scope
// lock, resolves each item's interval, runs check, plans every copy of every
// item, and writes each item in order: its record, then one copy of its
// control to each target (the lifecycle record, the control and its
// supersede marks). The items get consecutive creationTimes, the first one
// more than the largest in any target scope, so each copy is the newest event
// in its scope when written.
//
// On a nil error every write took effect, and the results follow items.
// A refusal wrote nothing: AcrossItemErrors names every item that could not
// be planned, an error from check is returned as is, and both together are
// joined. On any other plain error every write was undone. When an undo
// fails, every other item is still undone: with one item the *UndoError is
// returned, and with several an *AcrossUndoError names each item that may
// remain. A target with no check, fleet key or reach is
// ErrUncheckedCommitment, and an item naming a grant is ErrAcrossGrant.
func (i *Issuer) IssueAcrossAll(ctx context.Context, items []Across, check AcrossCheck) ([]AcrossResult, error) {
	var res []AcrossResult
	err := i.acrossAll(ctx, items, check, func(planned []plannedAcross) error {
		var err error
		res, err = i.writeAcross(ctx, planned)
		return err
	})
	if err != nil {
		return nil, err
	}
	return res, nil
}

// PlanAcrossAll runs every check IssueAcrossAll would, under the same locks,
// and writes nothing. Its error is IssueAcrossAll's refusal.
func (i *Issuer) PlanAcrossAll(ctx context.Context, items []Across, check AcrossCheck) error {
	return i.acrossAll(ctx, items, check, nil)
}

// acrossAll plans items under their scope locks and, when every check
// passes, calls write with the plans while still holding the locks.
func (i *Issuer) acrossAll(ctx context.Context, items []Across, check AcrossCheck, write func([]plannedAcross) error) error {
	if len(items) == 0 {
		return errors.New("dercontrol: no control to issue")
	}
	now := sep2time.Now().Unix()
	prepared, refused := i.prepareAcross(items, now)

	var keys []string
	for _, p := range prepared {
		keys = append(keys, targetKeys(p.targets)...)
	}
	slices.Sort(keys)
	for _, k := range slices.Compact(keys) {
		defer i.lockScope(k)()
	}

	existing, scopeErrs, err := i.readScopes(ctx, prepared)
	if err != nil {
		return err
	}
	usable := prepared[:0:0]
	for _, p := range prepared {
		if err := firstScopeErr(p.targets, scopeErrs); err != nil {
			refused = append(refused, AcrossItemError{Item: p.item, Err: err})
			continue
		}
		usable = append(usable, p)
	}
	first := nextCreationTime(now, slices.Collect(maps.Values(existing))...)

	planned, resolveRefused := i.resolveAcross(usable, first, now)
	var checkErr error
	if check != nil {
		controls := make([]sep2.DERControl, len(items))
		for _, p := range planned {
			controls[p.item] = p.ctrl
		}
		checkErr = check(controls)
	}
	refused = refused.with(resolveRefused).with(i.planAcross(ctx, planned, usable, existing))
	switch {
	case checkErr != nil && len(refused) > 0:
		return errors.Join(checkErr, refused)
	case checkErr != nil:
		return checkErr
	case len(refused) > 0:
		return refused
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if write == nil {
		return nil
	}
	return write(planned)
}

// readScopes reads the controls of every scope the items name once, and the
// program check of each, keyed by scope.
func (i *Issuer) readScopes(ctx context.Context, prepared []preparedAcross) (map[string][]sep2.DERControl, map[string]error, error) {
	existing := map[string][]sep2.DERControl{}
	scopeErrs := map[string]error{}
	for _, p := range prepared {
		for _, t := range p.targets {
			key := t.Scope.Key()
			if _, read := existing[key]; read {
				continue
			}
			if _, failed := scopeErrs[key]; failed {
				continue
			}
			if err := i.checkProgram(ctx, t.Scope); err != nil {
				scopeErrs[key] = err
				continue
			}
			list, err := i.controls.List(ctx, key, store.ListOptions{Unbounded: true})
			if err != nil {
				return nil, nil, fmt.Errorf("dercontrol: list scope: %w", err)
			}
			existing[key] = list.Items
		}
	}
	return existing, scopeErrs, nil
}

func firstScopeErr(targets []Target, scopeErrs map[string]error) error {
	for _, t := range targets {
		if err := scopeErrs[t.Scope.Key()]; err != nil {
			return err
		}
	}
	return nil
}

// preparedAcross is one item after the checks that read no store.
type preparedAcross struct {
	item    int
	req     CreateRequest
	origin  string
	targets []Target
	base    *sep2.DERControlBase
	record  RecordWriter
	offset  int64
	total   bool
}

// prepareAcross runs every check that reads no store, on every item.
func (i *Issuer) prepareAcross(items []Across, now int64) ([]preparedAcross, AcrossItemErrors) {
	var refused AcrossItemErrors
	out := make([]preparedAcross, 0, len(items))
	for n, it := range items {
		p, err := i.prepareItem(it, now)
		if err != nil {
			refused = append(refused, AcrossItemError{Item: n, Err: err})
			continue
		}
		p.item = n
		out = append(out, p)
	}
	return out, refused
}

func (i *Issuer) prepareItem(it Across, now int64) (preparedAcross, error) {
	if it.Req.ExecutesGrant != "" {
		return preparedAcross{}, ErrAcrossGrant
	}
	origin, err := StoredOrigin(it.Req.Origin)
	if err != nil {
		return preparedAcross{}, err
	}
	targets, err := sortTargets(it.Targets)
	if err != nil {
		return preparedAcross{}, err
	}
	if it.StartOffset < 0 || (it.StartOffset > 0 && it.Req.Start != nil) {
		return preparedAcross{}, errors.New("dercontrol: a start offset must be non-negative and needs no start")
	}
	req := it.Req
	req.DERProgramHref = UnscopedProgramHref
	base, err := i.validate(req, now)
	if err != nil {
		return preparedAcross{}, err
	}
	return preparedAcross{req: req, origin: origin, targets: targets, base: base, record: it.Record, offset: it.StartOffset, total: it.FleetTotal}, nil
}

// plannedAcross is one item's control and copies, decided before any write.
type plannedAcross struct {
	item   int
	id     string
	ctrl   sep2.DERControl
	record RecordWriter
	copies []copyPlan
}

// resolveAcross gives each item its creationTime, start, mRID and id.
func (i *Issuer) resolveAcross(prepared []preparedAcross, first, now int64) ([]plannedAcross, AcrossItemErrors) {
	var refused AcrossItemErrors
	out := make([]plannedAcross, 0, len(prepared))
	for n, p := range prepared {
		creationTime := first + int64(n)
		start := first + p.offset
		if p.req.Start != nil {
			start = *p.req.Start
		}
		if start > now+int64(i.cfg.StartLead/time.Second) {
			refused = append(refused, AcrossItemError{Item: p.item, Err: refuse(RefusalStartTooFarAhead)})
			continue
		}
		mrid, err := newMRID(*i.cfg.PEN)
		if err != nil {
			refused = append(refused, AcrossItemError{Item: p.item, Err: fmt.Errorf("dercontrol: generate mRID: %w", err)})
			continue
		}
		ctrl := newControl(p.base, mrid, p.req.Description, creationTime, start, p.req.DurationSeconds)
		out = append(out, plannedAcross{item: p.item, id: sortableID(start, creationTime, mrid), ctrl: ctrl, record: p.record})
	}
	return out, refused
}

// planAcross plans each item's copies against its scopes as read, and
// names every item that could not be planned.
func (i *Issuer) planAcross(ctx context.Context, planned []plannedAcross, prepared []preparedAcross, existing map[string][]sep2.DERControl) AcrossItemErrors {
	var refused AcrossItemErrors
	byItem := make(map[int]preparedAcross, len(prepared))
	for _, p := range prepared {
		byItem[p.item] = p
	}
	inCall := map[string][]sep2.DERControl{}
	for n := range planned {
		if err := i.planItemCopies(ctx, &planned[n], byItem[planned[n].item], existing, inCall); err != nil {
			refused = append(refused, AcrossItemError{Item: planned[n].item, Err: err})
		}
	}
	return refused
}

// planItemCopies plans one item's copy in each of its scopes, refusing one
// that overlaps an earlier item of the call in a scope both reach.
func (i *Issuer) planItemCopies(ctx context.Context, plan *plannedAcross, p preparedAcross, existing, inCall map[string][]sep2.DERControl) error {
	start, end := plan.ctrl.Interval.Start, IntervalEnd(plan.ctrl.Interval)
	for _, t := range p.targets {
		key := t.Scope.Key()
		for _, c := range inCall[key] {
			if overlaps(c.Interval.Start, IntervalEnd(c.Interval), start, end) {
				return ErrAcrossOverlap
			}
		}
		cp, err := i.planCopy(ctx, t, copyOf(plan.ctrl, t.Scope, plan.id), existing[key], "")
		if err != nil {
			return err
		}
		cp.link.Origin = p.origin
		cp.link.FleetTotal = p.total
		plan.copies = append(plan.copies, cp)
	}
	for _, t := range p.targets {
		inCall[t.Scope.Key()] = append(inCall[t.Scope.Key()], plan.ctrl)
	}
	return nil
}

// AcrossUndoError is an IssueAcrossAll call of several items that failed
// and could not undo every write. Kept names each item that may remain, with
// the *UndoError saying what of it remains; every other item was undone.
type AcrossUndoError struct {
	Kept  AcrossItemErrors
	Cause error
}

func (e *AcrossUndoError) Error() string {
	return fmt.Sprintf("dercontrol: %d controls could not be undone after: %v", len(e.Kept), e.Cause)
}

// writtenItem is one item whose record and copies were written.
type writtenItem struct {
	item    int
	written []writtenCopy
	record  *recordUndo
}

// writeAcross writes each planned item in order. When one fails, it and
// every item written before it are undone, each attempted whatever the
// others do, and the error names any that remain.
func (i *Issuer) writeAcross(ctx context.Context, planned []plannedAcross) ([]AcrossResult, error) {
	var finished []writtenItem
	out := make([]AcrossResult, 0, len(planned))
	for _, p := range planned {
		undoRecord, err := p.record(ctx, p.id, p.ctrl)
		if err != nil {
			return nil, i.undoItems(ctx, len(planned), nil, nil, finished, fmt.Errorf("dercontrol: store record: %w", err))
		}
		cur := writtenItem{item: p.item, record: &recordUndo{id: p.id, mrid: p.ctrl.MRID, undo: undoRecord}}
		cur.written, err = i.writeCopies(ctx, p.copies, p.id)
		if err != nil {
			return nil, i.undoItems(ctx, len(planned), &cur, err, finished, err)
		}
		finished = append(finished, cur)
		res := AcrossResult{ID: p.id, Control: p.ctrl}
		for _, w := range cur.written {
			res.Copies = append(res.Copies, Result{Scope: w.plan.target.Scope, ID: p.id, Href: w.plan.ctrl.Href, Control: w.plan.ctrl, Supersedes: w.marked})
		}
		out = append(out, res)
	}
	return out, nil
}

// undoItems undoes cur, whose copy write failed with curErr, and then every
// finished item newest first. A cur whose own failed copy could not be undone
// keeps that copy and its record, and has its other copies removed.
func (i *Issuer) undoItems(ctx context.Context, items int, cur *writtenItem, curErr error, finished []writtenItem, cause error) error {
	var kept AcrossItemErrors
	if cur != nil {
		var undo *UndoError
		if errors.As(curErr, &undo) {
			// Best effort: the item is named as kept whether or not these go.
			_ = i.removeWritten(ctx, cur.written, curErr, nil)
			kept = append(kept, AcrossItemError{Item: cur.item, Err: curErr})
		} else if err := i.removeWritten(ctx, cur.written, cause, cur.record); err != nil {
			kept = append(kept, AcrossItemError{Item: cur.item, Err: err})
		}
	}
	for n := len(finished) - 1; n >= 0; n-- {
		if err := i.removeWritten(ctx, finished[n].written, cause, finished[n].record); err != nil {
			kept = append(kept, AcrossItemError{Item: finished[n].item, Err: err})
		}
	}
	switch {
	case len(kept) == 0:
		return cause
	case items == 1:
		return kept[0].Err
	}
	return &AcrossUndoError{Kept: kept.with(nil), Cause: cause}
}

// Replica is a control already recorded on an FSA, to be written to one more
// scope with the record's id, mRID, creationTime and interval.
type Replica struct {
	Target  Target
	ID      string
	Control sep2.DERControl
	// Origin is the record's stored origin, kept on the copy (#1155).
	Origin string
	// FleetTotal is the record's fleet total mark, kept on the copy.
	FleetTotal bool
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
	end := IntervalEnd(ctrl.Interval)

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
	link := LifecycleRecord{FleetKey: r.Target.Fleet.Key, Reach: r.Target.Fleet.Reach, Origin: r.Origin, FleetTotal: r.FleetTotal}
	if err := i.supersededByNewer(ctx, scopeKey, newer, ctrl, &link); err != nil {
		return copyPlan{}, false, err
	}
	plan, err := i.finishPlan(ctx, r.Target, ctrl, "", candidates, link)
	return plan, false, err
}

// supersededByNewer marks lc superseded by the earliest-starting event among
// newer whose function set equals ctrl's and whose interval overlaps it, the
// mark that event would have written had ctrl been stored first. A newer
// event that is cancelled, or has no lifecycle record, marks nothing, as in
// Issue.
func (i *Issuer) supersededByNewer(ctx context.Context, scopeKey string, newer []sep2.DERControl, ctrl sep2.DERControl, lc *LifecycleRecord) error {
	set := functionSetKey(ctrl.DERControlBase)
	start := ctrl.Interval.Start
	end := IntervalEnd(ctrl.Interval)
	for _, c := range newer {
		if !replacedBy(c, set, start, end) {
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
// then the marks. Every prefix of that sequence stays legal: an orphan
// lifecycle record is legal and unreachable, and once the control is stored
// every stored control has its lifecycle record for the rest of the call. An
// orphan left by a failed undo can carry a grant link, so anything counting
// commitments must start from stored controls, never from lifecycle records
// alone. On the first failure it undoes that copy's
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

// recordUndo removes the record IssueAcross wrote, naming it if it cannot.
type recordUndo struct {
	id, mrid string
	undo     func(context.Context) error
}

// undoAcross answers a fan-out or replication whose writes stopped at cause.
// A *UndoError cause keeps everything already written. Otherwise it is
// removeWritten.
func (i *Issuer) undoAcross(ctx context.Context, written []writtenCopy, cause error, record *recordUndo) error {
	var undo *UndoError
	if errors.As(cause, &undo) {
		return cause
	}
	if err := i.removeWritten(ctx, written, cause, record); err != nil {
		return err
	}
	return cause
}

// removeWritten removes the written copies newest first, and then the
// record; the first removal that fails is returned as a *UndoError and keeps
// what it has not reached. It returns nil when everything was removed.
func (i *Issuer) removeWritten(ctx context.Context, written []writtenCopy, cause error, record *recordUndo) error {
	uctx, cancel := undoContext(ctx)
	defer cancel()
	for n := len(written) - 1; n >= 0; n-- {
		if err := i.removeCopy(uctx, written[n], cause); err != nil {
			return err
		}
	}
	if record != nil {
		if err := record.undo(uctx); err != nil {
			return &UndoError{Step: UndoStepStoreRecord, ID: record.id, MRID: record.mrid, cause: cause, reverts: []error{err}}
		}
	}
	return nil
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
	unreverted, reverts := i.revertMarks(ctx, scopeKey, w.plan.candidates, false)
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
	got, err := i.resolveScope(ctx, scope.EndDeviceID, scope.DERProgramID)
	if err != nil {
		return err
	}
	if got != scope {
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
	c.Href = scope.ControlHref(id)
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
