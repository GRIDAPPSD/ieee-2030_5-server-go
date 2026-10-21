package dercontrol

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/derhref"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/interval"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/handlers/sep2time"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
)

// Issuer builds admin-issued DERControl events and keeps their lifecycle.
// The zero value is not usable: construct with NewIssuer.
type Issuer struct {
	programs   programStore
	controls   controlStore
	lifecycles lifecycleStore
	cfg        Config

	// scopeLocks serializes Issue and Cancel within one (edev, fsa, derp)
	// scope, so creationTime ordering and the supersede scan (both of which
	// read the scope's controls and then write) see a consistent snapshot.
	// A single mutex guards the map itself, never the per-scope work.
	//
	// Never evicted: an entry is added on first use and kept for the
	// Issuer's lifetime, so the map grows by one *sync.Mutex per distinct
	// scope ever issued to. That bound is the number of DERProgram scopes
	// an operator creates, not the number of controls issued, since
	// entries key on scope and nothing here creates or deletes programs.
	// Add eviction if a later admin route makes DERProgram scopes churn at
	// fleet scale (#563; safe eviction would need to prove no lock is
	// dropped while a concurrent Issue or Cancel still holds it).
	mu         sync.Mutex
	scopeLocks map[string]*sync.Mutex
}

// NewIssuer builds an Issuer, or returns an error if cfg's bounds are
// invalid (see Config.validate). cfg's zero StartLead, MinDuration and
// MaxDuration fields take the package defaults; a nil or zero cfg.PEN means
// every Issue call refuses with RefusalPENNotConfigured.
func NewIssuer(programs programStore, controls controlStore, lifecycles lifecycleStore, cfg Config) (*Issuer, error) {
	cfg = cfg.withDefaults()
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	if cfg.PEN != nil && *cfg.PEN == 0 {
		cfg.PEN = nil
	}
	return &Issuer{
		programs:   programs,
		controls:   controls,
		lifecycles: lifecycles,
		cfg:        cfg,
		scopeLocks: make(map[string]*sync.Mutex),
	}, nil
}

// undoTimeout bounds the whole undo sequence following one failed forward
// write, however many stores it touches: one budget per call, not one
// per write.
const undoTimeout = 5 * time.Second

// undoContext derives a context for an undo sequence from the caller's
// ctx: it keeps ctx's values but drops its cancellation, so a caller that
// already cancelled or timed out cannot defeat the rollback of a write it
// caused, and it carries its own bounded deadline so a hung backend
// cannot hold the scope lock during undo indefinitely.
func undoContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), undoTimeout)
}

// undoWriteOK reports whether an undo write (Delete or Update) restored
// the state it targeted. store.ErrNotFound counts as already done: the
// store contract forbids a failed write from returning it, so ErrNotFound
// here can only mean there was nothing left to undo.
func undoWriteOK(err error) bool {
	return err == nil || errors.Is(err, store.ErrNotFound)
}

// skipCompensation reports whether a compensating write against s, to undo
// s's OWN just-failed forward write to the SAME record, is unnecessary
// because s already implements selfRollingBack and confirms it: the
// forward write's failure already left the record exactly as it was
// before that write. See selfRollingBack's doc comment for why this must
// not be used for a compensating write that undoes a different, earlier,
// successful write.
func skipCompensation(s any) bool {
	r, ok := s.(selfRollingBack)
	return ok && r.RollsBackOnFailure()
}

// lockScope returns an unlock func for scopeKey, taken after this call
// returns. Callers must defer the returned func.
func (i *Issuer) lockScope(scopeKey string) func() {
	i.mu.Lock()
	l, ok := i.scopeLocks[scopeKey]
	if !ok {
		l = &sync.Mutex{}
		i.scopeLocks[scopeKey] = l
	}
	i.mu.Unlock()

	l.Lock()
	return l.Unlock
}

// Issue validates req, builds a conformant DERControl, resolves any
// supersede against controls already in the target scope, and stores both
// the control and its lifecycle record.
//
// On a nil error every write took effect. On a *RefusalError nothing was
// written. On any other plain error every write this call attempted has
// been undone: the stores are exactly as they were before the call. When
// the undo itself cannot finish, Issue returns a *UndoError instead,
// naming what may remain; that remainder is always a state a successful
// call could have passed through. Result is zero on every error;
// a partial result travels only inside *UndoError. Callers branch on the
// outcome with errors.As, checking *RefusalError before *UndoError. A
// request naming ExecutesGrant is refused with ErrUncheckedCommitment: only
// IssueInFleet stores a grant link.
func (i *Issuer) Issue(ctx context.Context, req CreateRequest) (Result, error) {
	if req.ExecutesGrant != "" {
		return Result{}, ErrUncheckedCommitment
	}
	return i.issue(ctx, req, Fleet{})
}

// Validate runs every check Issue makes that reads no store, and returns
// the *RefusalError Issue would. A caller that must take a lock before
// Issue runs these first, so a bad request is refused without waiting.
func (i *Issuer) Validate(req CreateRequest) error {
	_, err := i.validate(req, sep2time.Now().Unix())
	return err
}

// validate is Validate at now, returning the DERControlBase it built.
func (i *Issuer) validate(req CreateRequest, now int64) (*sep2.DERControlBase, error) {
	if i.cfg.PEN == nil {
		return nil, refuse(RefusalPENNotConfigured)
	}
	if _, _, _, ok := parseProgramHref(req.DERProgramHref); !ok {
		return nil, refuse(RefusalInvalidProgramHref)
	}
	if !utf8.ValidString(req.Description) || len(req.Description) > maxDescriptionOctets {
		return nil, refuse(RefusalInvalidDescription)
	}

	base, err := buildBase(req)
	if err != nil {
		return nil, err
	}

	if req.Start != nil {
		if *req.Start < now {
			return nil, refuse(RefusalStartInPast)
		}
		if *req.Start > now+int64(i.cfg.StartLead/time.Second) {
			return nil, refuse(RefusalStartTooFarAhead)
		}
	}
	durationSeconds := int64(req.DurationSeconds)
	if durationSeconds < int64(i.cfg.MinDuration/time.Second) || durationSeconds > int64(i.cfg.MaxDuration/time.Second) {
		return nil, refuse(RefusalDurationOutOfRange)
	}
	return base, nil
}

func (i *Issuer) issue(ctx context.Context, req CreateRequest, fleet Fleet) (Result, error) {
	now := sep2time.Now().Unix()
	base, err := i.validate(req, now)
	if err != nil {
		return Result{}, err
	}
	reqEdev, _, reqDerp, _ := parseProgramHref(req.DERProgramHref)
	scope, err := i.resolveScope(ctx, reqEdev, reqDerp)
	if err != nil {
		return Result{}, err
	}
	scopeKey := scope.Key()

	unlock := i.lockScope(scopeKey)
	defer unlock()

	// Retire may have removed the program after the read above, and a
	// control stored now would be one its check never saw.
	if _, err := i.loadProgram(ctx, reqEdev, reqDerp); err != nil {
		return Result{}, err
	}

	existing, err := i.controls.List(ctx, scopeKey, store.ListOptions{Unbounded: true})
	if err != nil {
		return Result{}, fmt.Errorf("dercontrol: list scope: %w", err)
	}
	creationTime := nextCreationTime(now, existing.Items)

	start := creationTime
	if req.Start != nil {
		start = *req.Start
	}

	mrid, err := newMRID(*i.cfg.PEN)
	if err != nil {
		return Result{}, fmt.Errorf("dercontrol: generate mRID: %w", err)
	}
	id := sortableID(start, creationTime, mrid)
	ctrl := newControl(base, mrid, req.Description, creationTime, start, req.DurationSeconds)
	ctrl.Href = scope.ControlHref(id)

	plan, err := i.planCopy(ctx, Target{Scope: scope, Fleet: fleet}, ctrl, existing.Items, req.ExecutesGrant)
	if err != nil {
		return Result{}, err
	}
	if err := ctx.Err(); err != nil {
		// Nothing has been written yet, so nothing needs undoing.
		return Result{}, err
	}

	written, err := i.writeCopies(ctx, []copyPlan{plan}, id)
	if err != nil {
		return Result{}, err
	}
	return Result{
		Scope:      scope,
		ID:         id,
		Href:       ctrl.Href,
		Control:    ctrl,
		Supersedes: written[0].marked,
	}, nil
}

// resolveScope loads the program at (edev, derp) and returns the scope its
// control list link names, refusing a program with no link or a link naming
// another device or program: neither is a link a device could ever have
// followed here.
func (i *Issuer) resolveScope(ctx context.Context, edev, derp string) (Scope, error) {
	program, err := i.loadProgram(ctx, edev, derp)
	if err != nil {
		return Scope{}, err
	}
	if program.DERControlListLink == nil {
		return Scope{}, refuse(RefusalNoControlListLink)
	}
	linkEdev, linkFsa, linkDerp, ok := parseControlListHref(program.DERControlListLink.Href)
	if !ok || linkEdev != edev || linkDerp != derp {
		return Scope{}, refuse(RefusalNoControlListLink)
	}
	return Scope{EndDeviceID: edev, FSAID: linkFsa, DERProgramID: derp}, nil
}

// loadProgram reads a program, mapping a missing one to its refusal.
func (i *Issuer) loadProgram(ctx context.Context, edev, derp string) (sep2.DERProgram, error) {
	program, err := i.programs.Get(ctx, edev, derp)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return sep2.DERProgram{}, refuse(RefusalProgramNotFound)
		}
		return sep2.DERProgram{}, fmt.Errorf("dercontrol: load program: %w", err)
	}
	return program, nil
}

// nextCreationTime is now, or one more than the largest creationTime among
// the controls of every scope the new control is written to, so it is the
// newest event in each.
func nextCreationTime(now int64, scopes ...[]sep2.DERControl) int64 {
	creationTime := now
	for _, controls := range scopes {
		for _, c := range controls {
			if c.CreationTime >= creationTime {
				creationTime = c.CreationTime + 1
			}
		}
	}
	return creationTime
}

// newControl is the control every copy of one request stores, before an href
// is set.
func newControl(base *sep2.DERControlBase, mrid, description string, creationTime, start int64, durationSeconds uint32) sep2.DERControl {
	ctrl := sep2.DERControl{DERControlBase: base}
	ctrl.MRID = mrid
	ctrl.Description = description
	ctrl.CreationTime = creationTime
	ctrl.Interval = &sep2.DateTimeInterval{Start: start, Duration: durationSeconds}
	return ctrl
}

// planCopy decides what writing ctrl to t's scope takes, before the first
// write: the controls it supersedes, found among existing, and the fleet's
// commitment check. A refusal leaves nothing to undo.
func (i *Issuer) planCopy(ctx context.Context, t Target, ctrl sep2.DERControl, existing []sep2.DERControl, grant string) (copyPlan, error) {
	candidates, err := i.computeSupersedes(ctx, t.Scope.Key(), existing, ctrl.DERControlBase, ctrl.Interval.Start, IntervalEnd(ctrl.Interval))
	if err != nil {
		return copyPlan{}, err
	}
	link := LifecycleRecord{GrantMRID: grant, FleetKey: t.Fleet.Key, Reach: t.Fleet.Reach}
	return i.finishPlan(ctx, t, ctrl, grant, candidates, link)
}

// finishPlan is the tail every plan shares: the fleet's commitment check,
// which runs after the supersede scan so it can discount the controls this
// one replaces, then the plan itself. A nil Check, which only a single-device
// Issue passes, skips the check.
func (i *Issuer) finishPlan(ctx context.Context, t Target, ctrl sep2.DERControl, grant string, candidates []supersedeCandidate, link LifecycleRecord) (copyPlan, error) {
	if t.Fleet.Check != nil {
		if err := t.Fleet.Check(ctx, newProposal(t.Scope, ctrl, grant, t.Fleet, candidates)); err != nil {
			return copyPlan{}, err
		}
	}
	return copyPlan{target: t, ctrl: ctrl, candidates: candidates, link: link}, nil
}

// IntervalEnd is the first second after the interval.
func IntervalEnd(iv *sep2.DateTimeInterval) int64 {
	return interval.End(iv.Start, iv.Duration)
}

// IntervalEnded reports whether the interval is over at now.
func IntervalEnded(iv *sep2.DateTimeInterval, now int64) bool {
	return now >= IntervalEnd(iv)
}

// overlaps reports whether [aStart, aEnd) and [bStart, bEnd) share a second;
// adjacent intervals do not.
func overlaps(aStart, aEnd, bStart, bEnd int64) bool {
	return aStart < bEnd && bStart < aEnd
}

// replacedBy reports whether older, an event with an interval, is replaced by
// a newer one with function set set over [start, end): only an equal function
// set supersedes, and a partial overlap is not marked (potentiallySuperseded
// is a later issue).
func replacedBy(older sep2.DERControl, set string, start, end int64) bool {
	if older.Interval == nil || functionSetKey(older.DERControlBase) != set {
		return false
	}
	return overlaps(older.Interval.Start, IntervalEnd(older.Interval), start, end)
}

// supersedeCandidate is an existing control eligible to be marked
// superseded by a new one, found by computeSupersedes before either store
// write Issue makes. before is the lifecycle record as read, so
// applySupersedes can restore it exactly if a later candidate's write
// fails.
type supersedeCandidate struct {
	id     string
	mrid   string
	before LifecycleRecord
}

// computeSupersedes finds every control among existing whose control set
// equals newBase's and whose interval overlaps [newStart, newEnd), without
// writing anything. Acceptance criterion 7.
//
// A control with no lifecycle record was not issued through this package
// (a boot-fixture or embedder control) and is skipped: this package derives
// no status for it, so it cannot be superseded by way of a status this
// package never asserts.
func (i *Issuer) computeSupersedes(ctx context.Context, scopeKey string, existing []sep2.DERControl, newBase *sep2.DERControlBase, newStart, newEnd int64) ([]supersedeCandidate, error) {
	newSet := functionSetKey(newBase)
	var candidates []supersedeCandidate
	for _, c := range existing {
		if c.Interval == nil {
			continue
		}
		id, ok := derhref.ControlID(c.Href)
		if !ok {
			continue
		}
		lc, err := i.lifecycles.Get(ctx, scopeKey, id)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				continue
			}
			return nil, fmt.Errorf("dercontrol: load lifecycle: %w", err)
		}
		if !lc.supersedeEligible(newStart) {
			continue
		}
		if !replacedBy(c, newSet, newStart, newEnd) {
			continue
		}
		candidates = append(candidates, supersedeCandidate{id: id, mrid: c.MRID, before: lc})
	}
	return candidates, nil
}

// applySupersedes marks every candidate superseded by newMRID at newStart,
// in order, under ctx (forward writes use the caller's context). On the
// first failure it stops and returns the mRIDs marked so far and the
// candidates from index 0 through the failing one, inclusive: the failing
// write may have taken effect despite its error, so the caller's undo
// must attempt to revert it too, not only the candidates marked before
// it.
func (i *Issuer) applySupersedes(ctx context.Context, scopeKey string, candidates []supersedeCandidate, newStart int64, newMRID string) (marked []string, attempted []supersedeCandidate, err error) {
	for n, cand := range candidates {
		lc := cand.before
		lc.SupersededAt = ptrInt64(newStart)
		lc.SupersededBy = newMRID
		if err := i.lifecycles.Update(ctx, scopeKey, cand.id, lc); err != nil {
			return marked, candidates[:n+1], err
		}
		marked = append(marked, cand.mrid)
	}
	return marked, nil, nil
}

// undoLifecycleCreateFailure undoes a failed lifecycle Create. Unless the
// store already rolled its own failure back (skipCompensation), the write
// may have taken effect despite the error, so it is always deleted; a
// store.ErrNotFound from that delete means it never took effect.
func (i *Issuer) undoLifecycleCreateFailure(ctx context.Context, scopeKey, id string, cause error) (Result, error) {
	if !skipCompensation(i.lifecycles) {
		uctx, cancel := undoContext(ctx)
		defer cancel()
		if derr := i.lifecycles.Delete(uctx, scopeKey, id); !undoWriteOK(derr) {
			return Result{}, &UndoError{Step: UndoStepStoreLifecycle, LifecycleKept: true, ID: id, cause: cause, reverts: []error{derr}}
		}
	}
	return Result{}, fmt.Errorf("dercontrol: store lifecycle: %w", cause)
}

// undoControlCreateFailure undoes a failed control Create. The control
// Delete compensates that SAME failed write (skipCompensation applies).
// The lifecycle record was created successfully in an earlier step, so
// deleting it is a genuine revert of a successful write, not skippable,
// unless deleting the control itself fails: the lifecycle record must not
// be removed while the control it backs might still be stored.
func (i *Issuer) undoControlCreateFailure(ctx context.Context, scopeKey, id string, cause error) (Result, error) {
	uctx, cancel := undoContext(ctx)
	defer cancel()
	if !skipCompensation(i.controls) {
		if derr := i.controls.Delete(uctx, scopeKey, id); !undoWriteOK(derr) {
			return Result{}, &UndoError{Step: UndoStepStoreControl, ControlKept: true, LifecycleKept: true, ID: id, cause: cause, reverts: []error{derr}}
		}
	}
	if derr := i.lifecycles.Delete(uctx, scopeKey, id); !undoWriteOK(derr) {
		return Result{}, &UndoError{Step: UndoStepStoreControl, LifecycleKept: true, ID: id, cause: cause, reverts: []error{derr}}
	}
	return Result{}, fmt.Errorf("dercontrol: store control: %w", cause)
}

// undoMarkFailure undoes a failed supersede mark: every candidate
// applySupersedes attempted in this call, the failing one included, is
// restored to its exact prior record, attempting every one, before the
// new control and its lifecycle record are deleted. A restore that fails
// keeps the new control and its record regardless of what follows,
// because deleting them would leave a stored mark naming a control that
// no longer exists.
func (i *Issuer) undoMarkFailure(ctx context.Context, scopeKey, id string, attempted []supersedeCandidate, cause error) (Result, error) {
	uctx, cancel := undoContext(ctx)
	defer cancel()

	// applySupersedes's contract puts the candidate whose OWN mark-write
	// failed (the write that is `cause`) last in attempted; every earlier
	// candidate's mark-write succeeded and its revert is a genuine action.
	// For the last one, when the store self-rolls back, its own failure
	// already restored cand.before, so the redundant re-write is skipped: a
	// second, unrelated failure there cannot be misreported as "not
	// reverted" when it is already known to be.
	unrevertedIDs, reverts := i.revertMarks(uctx, scopeKey, attempted, skipCompensation(i.lifecycles))
	if len(unrevertedIDs) > 0 {
		return Result{}, &UndoError{Step: UndoStepMarkSuperseded, ControlKept: true, LifecycleKept: true, ID: id, UnrevertedIDs: unrevertedIDs, cause: cause, reverts: reverts}
	}

	if derr := i.controls.Delete(uctx, scopeKey, id); !undoWriteOK(derr) {
		return Result{}, &UndoError{Step: UndoStepMarkSuperseded, ControlKept: true, LifecycleKept: true, ID: id, cause: cause, reverts: []error{derr}}
	}
	if derr := i.lifecycles.Delete(uctx, scopeKey, id); !undoWriteOK(derr) {
		return Result{}, &UndoError{Step: UndoStepMarkSuperseded, LifecycleKept: true, ID: id, cause: cause, reverts: []error{derr}}
	}
	return Result{}, fmt.Errorf("dercontrol: mark superseded: %w", cause)
}

// revertMarks restores every candidate to its prior lifecycle record,
// attempting all of them, and returns the ids it could not restore with the
// errors. skipLast leaves the final candidate alone.
func (i *Issuer) revertMarks(ctx context.Context, scopeKey string, candidates []supersedeCandidate, skipLast bool) (unreverted []string, reverts []error) {
	for n, cand := range candidates {
		if skipLast && n == len(candidates)-1 {
			continue
		}
		if err := i.lifecycles.Update(ctx, scopeKey, cand.id, cand.before); !undoWriteOK(err) {
			unreverted = append(unreverted, cand.id)
			reverts = append(reverts, err)
		}
	}
	return unreverted, reverts
}

// Cancel records cancellation of the control at (scope, id). It refuses a
// control that is unknown, already cancelled, already superseded, or whose
// interval has ended (acceptance criterion 8).
//
// On a nil error CancelledAt and CancelReason are stored. On a
// *RefusalError nothing was written. On any other plain error the stored
// record equals what Cancel read, and that record is returned (zero when
// the read itself failed). When the restore itself fails, Cancel returns a
// *UndoError naming the control; the record may or may not carry the
// cancellation.
func (i *Issuer) Cancel(ctx context.Context, scope Scope, id string, reason string) (LifecycleRecord, error) {
	scopeKey := scope.Key()

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

	now := sep2time.Now().Unix()
	if before.cancelled() {
		return LifecycleRecord{}, refuse(RefusalAlreadyCancelled)
	}
	if before.supersededAsOf(now) {
		return LifecycleRecord{}, refuse(RefusalAlreadySuperseded)
	}
	if ctrl.Interval == nil {
		return before, fmt.Errorf("dercontrol: control has no interval")
	}
	if IntervalEnded(ctrl.Interval, now) {
		return LifecycleRecord{}, refuse(RefusalEnded)
	}

	if err := ctx.Err(); err != nil {
		// Nothing has been written yet, so nothing needs undoing.
		return before, err
	}

	lc := before
	lc.CancelledAt = ptrInt64(now)
	lc.CancelReason = reason
	if err := i.lifecycles.Update(ctx, scopeKey, id, lc); err != nil {
		return i.restoreLifecycle(ctx, scope, id, ctrl.MRID, before, UndoStepCancel, err)
	}
	return lc, nil
}

// restoreLifecycle undoes a failed Update of the record at (scope, id),
// the one write Cancel and Relink make. On a plain error the store holds
// before again, and before is returned. When the store already rolled its
// own failure back (skipCompensation) the compensating write is skipped. A
// failed restore is a *UndoError naming the control, whose record may or
// may not carry the write.
func (i *Issuer) restoreLifecycle(ctx context.Context, scope Scope, id, mrid string, before LifecycleRecord, step UndoStep, cause error) (LifecycleRecord, error) {
	if !skipCompensation(i.lifecycles) {
		uctx, cancel := undoContext(ctx)
		defer cancel()
		if rerr := i.lifecycles.Update(uctx, scope.Key(), id, before); !undoWriteOK(rerr) {
			return LifecycleRecord{}, &UndoError{Step: step, ControlKept: true, LifecycleKept: true, ID: id, Scope: scope, MRID: mrid, cause: cause, reverts: []error{rerr}}
		}
	}
	return before, fmt.Errorf("dercontrol: %s: %w", step, cause)
}

// ControlBase returns the DERControlBase Issue would build from req's Type
// and value, or the *RefusalError it would refuse them with. It reads no
// other field, so a program's default control is checked the same way.
func ControlBase(req CreateRequest) (*sep2.DERControlBase, error) {
	return buildBase(req)
}

// buildBase maps a request's type and value, or its function set, to a
// DERControlBase (acceptance
// criterion 5). It never reads or sets randomizeStart, randomizeDuration,
// EventStatus, replyTo, responseRequired, mRID or creationTime, because
// CreateRequest carries none of them.
func buildBase(req CreateRequest) (*sep2.DERControlBase, error) {
	if req.Functions != nil {
		if req.Type != "" {
			return nil, refuse(RefusalTypeAndFunctions)
		}
		if req.MaxLimW != nil || req.PowerFactor != nil || req.TargetW != nil {
			return nil, refuse(RefusalUnexpectedValue)
		}
		return baseFromFunctions(req.Functions)
	}
	if req.Type != TargetW && req.TargetW != nil {
		return nil, refuse(RefusalUnexpectedValue)
	}
	switch req.Type {
	case Connect:
		if req.MaxLimW != nil || req.PowerFactor != nil {
			return nil, refuse(RefusalUnexpectedValue)
		}
		t := true
		return &sep2.DERControlBase{OpModConnect: &t, OpModEnergize: &t}, nil
	case Disconnect:
		if req.MaxLimW != nil || req.PowerFactor != nil {
			return nil, refuse(RefusalUnexpectedValue)
		}
		f := false
		return &sep2.DERControlBase{OpModConnect: &f, OpModEnergize: &f}, nil
	case MaxLimW:
		if req.PowerFactor != nil {
			return nil, refuse(RefusalUnexpectedValue)
		}
		if req.MaxLimW == nil {
			return nil, refuse(RefusalMissingValue)
		}
		if *req.MaxLimW > 10000 {
			return nil, refuse(RefusalValueOutOfRange)
		}
		v := sep2.PerCent(*req.MaxLimW)
		return &sep2.DERControlBase{OpModMaxLimW: &v}, nil
	case FixedPFInjectW:
		if req.MaxLimW != nil {
			return nil, refuse(RefusalUnexpectedValue)
		}
		if req.PowerFactor == nil {
			return nil, refuse(RefusalMissingValue)
		}
		if req.PowerFactor.Excitation == nil {
			// A missing excitation must be refused like a missing
			// displacement, not silently taken as false (over-excited):
			// the two directions inject and absorb reactive power.
			return nil, refuse(RefusalMissingValue)
		}
		if req.PowerFactor.Displacement < 1 || req.PowerFactor.Displacement > 1000 {
			return nil, refuse(RefusalValueOutOfRange)
		}
		return &sep2.DERControlBase{OpModFixedPFInjectW: &sep2.FixedPowerFactor{
			Displacement: req.PowerFactor.Displacement,
			Excitation:   *req.PowerFactor.Excitation,
			Multiplier:   -3,
		}}, nil
	case TargetW:
		if req.MaxLimW != nil || req.PowerFactor != nil {
			return nil, refuse(RefusalUnexpectedValue)
		}
		if req.TargetW == nil {
			return nil, refuse(RefusalMissingValue)
		}
		// Value is bounded by its int16 type; the multiplier is an Int8 the
		// standard limits to -9..9 (PowerOfTenMultiplierType).
		if req.TargetW.Multiplier < -9 || req.TargetW.Multiplier > 9 {
			return nil, refuse(RefusalValueOutOfRange)
		}
		v := *req.TargetW
		return &sep2.DERControlBase{OpModTargetW: &v}, nil
	default:
		return nil, refuse(RefusalUnknownType)
	}
}

// nameUndo returns a pass-through that records scope and mrid on an
// *UndoError, leaving any other error untouched.
func nameUndo(scope Scope, mrid string) func(Result, error) (Result, error) {
	return func(res Result, err error) (Result, error) {
		var undo *UndoError
		if errors.As(err, &undo) {
			undo.Scope = scope
			undo.MRID = mrid
		}
		return res, err
	}
}

func ptrInt64(v int64) *int64 { return &v }
