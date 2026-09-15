package dercontrol

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
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

func scopeKeyOf(s Scope) string {
	return s.EndDeviceID + "/" + s.FSAID + "/" + s.DERProgramID
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
// outcome with errors.As, checking *RefusalError before *UndoError.
func (i *Issuer) Issue(ctx context.Context, req CreateRequest) (Result, error) {
	if i.cfg.PEN == nil {
		return Result{}, refuse(RefusalPENNotConfigured)
	}

	reqEdev, _, reqDerp, ok := parseProgramHref(req.DERProgramHref)
	if !ok {
		return Result{}, refuse(RefusalInvalidProgramHref)
	}

	base, err := buildBase(req)
	if err != nil {
		return Result{}, err
	}

	now := sep2time.Now().Unix()

	if req.Start != nil {
		if *req.Start < now {
			return Result{}, refuse(RefusalStartInPast)
		}
		if *req.Start > now+int64(i.cfg.StartLead/time.Second) {
			return Result{}, refuse(RefusalStartTooFarAhead)
		}
	}
	durationSeconds := int64(req.DurationSeconds)
	if durationSeconds < int64(i.cfg.MinDuration/time.Second) || durationSeconds > int64(i.cfg.MaxDuration/time.Second) {
		return Result{}, refuse(RefusalDurationOutOfRange)
	}

	program, err := i.programs.Get(ctx, reqEdev, reqDerp)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return Result{}, refuse(RefusalProgramNotFound)
		}
		return Result{}, fmt.Errorf("dercontrol: load program: %w", err)
	}
	if program.DERControlListLink == nil {
		return Result{}, refuse(RefusalNoControlListLink)
	}
	linkEdev, linkFsa, linkDerp, ok := parseControlListHref(program.DERControlListLink.Href)
	if !ok || linkEdev != reqEdev || linkDerp != reqDerp {
		// The link is absent in shape, or names a different device or
		// program than the one we just loaded it from: acceptance
		// criterion 1 requires refusing both, since neither is a link a
		// device could ever have followed here.
		return Result{}, refuse(RefusalNoControlListLink)
	}
	scope := Scope{EndDeviceID: reqEdev, FSAID: linkFsa, DERProgramID: reqDerp}
	scopeKey := scopeKeyOf(scope)

	unlock := i.lockScope(scopeKey)
	defer unlock()

	existing, err := i.controls.List(ctx, scopeKey, store.ListOptions{Unbounded: true})
	if err != nil {
		return Result{}, fmt.Errorf("dercontrol: list scope: %w", err)
	}

	creationTime := now
	for _, c := range existing.Items {
		if c.CreationTime >= creationTime {
			creationTime = c.CreationTime + 1
		}
	}

	start := creationTime
	if req.Start != nil {
		start = *req.Start
	}

	mrid, err := newMRID(*i.cfg.PEN)
	if err != nil {
		return Result{}, fmt.Errorf("dercontrol: generate mRID: %w", err)
	}
	id := sortableID(start, creationTime, mrid)
	href := "/edev/" + scope.EndDeviceID + "/fsa/" + scope.FSAID + "/derp/" + scope.DERProgramID + "/derc/" + id

	ctrl := sep2.DERControl{DERControlBase: base}
	ctrl.Href = href
	ctrl.MRID = mrid
	ctrl.CreationTime = creationTime
	ctrl.Interval = &sep2.DateTimeInterval{Start: start, Duration: uint32(req.DurationSeconds)}

	// Candidates are computed before either store is written, so the marks
	// applySupersedes writes always name an mRID that is already stored: no
	// write below can leave a mark referring to a control this call failed
	// to create.
	candidates, err := i.computeSupersedes(ctx, scopeKey, existing.Items, base, start, start+durationSeconds)
	if err != nil {
		return Result{}, err
	}

	if err := ctx.Err(); err != nil {
		// Nothing has been written yet, so nothing needs undoing.
		return Result{}, err
	}

	// Forward order: lifecycle record, then control, then marks. Every
	// prefix of this sequence stays legal: an orphan lifecycle record is
	// legal and unreachable, and once the control is stored, every
	// stored control has its lifecycle record for the rest of the call.
	if err := i.lifecycles.Create(ctx, scopeKey, id, LifecycleRecord{}); err != nil {
		return i.undoLifecycleCreateFailure(ctx, scopeKey, id, err)
	}
	if err := i.controls.Create(ctx, scopeKey, id, ctrl); err != nil {
		return i.undoControlCreateFailure(ctx, scopeKey, id, err)
	}

	supersedes, attempted, err := i.applySupersedes(ctx, scopeKey, candidates, start, mrid)
	if err != nil {
		return i.undoMarkFailure(ctx, scopeKey, id, attempted, err)
	}

	return Result{
		Scope:      scope,
		ID:         id,
		Href:       href,
		Control:    ctrl,
		Supersedes: supersedes,
	}, nil
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
	newShape := controlShape(newBase)
	var candidates []supersedeCandidate
	for _, c := range existing {
		if c.Interval == nil {
			continue
		}
		id, ok := idFromHref(c.Href)
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
		if controlShape(c.DERControlBase) != newShape {
			continue // disjoint control sets never supersede (2018 rule t)
		}
		cEnd := c.Interval.Start + int64(c.Interval.Duration)
		if !(c.Interval.Start < newEnd && newStart < cEnd) {
			continue // no overlap; adjacent (cEnd == newStart) excluded by strict '<'
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

// undoLifecycleCreateFailure undoes a failed lifecycle Create. The write
// may have taken effect despite the error, so it is always deleted; a
// store.ErrNotFound from that delete means it never took effect.
func (i *Issuer) undoLifecycleCreateFailure(ctx context.Context, scopeKey, id string, cause error) (Result, error) {
	uctx, cancel := undoContext(ctx)
	defer cancel()
	if derr := i.lifecycles.Delete(uctx, scopeKey, id); !undoWriteOK(derr) {
		return Result{}, &UndoError{Step: UndoStepStoreLifecycle, LifecycleKept: true, ID: id, cause: cause, reverts: []error{derr}}
	}
	return Result{}, fmt.Errorf("dercontrol: store lifecycle: %w", cause)
}

// undoControlCreateFailure undoes a failed control Create. The lifecycle
// record already exists at this point, so it is deleted too, unless
// deleting the control itself fails: the lifecycle record must not be
// removed while the control it backs might still be stored.
func (i *Issuer) undoControlCreateFailure(ctx context.Context, scopeKey, id string, cause error) (Result, error) {
	uctx, cancel := undoContext(ctx)
	defer cancel()
	if derr := i.controls.Delete(uctx, scopeKey, id); !undoWriteOK(derr) {
		return Result{}, &UndoError{Step: UndoStepStoreControl, ControlKept: true, LifecycleKept: true, ID: id, cause: cause, reverts: []error{derr}}
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

	var unrevertedIDs []string
	var reverts []error
	for _, cand := range attempted {
		if rerr := i.lifecycles.Update(uctx, scopeKey, cand.id, cand.before); !undoWriteOK(rerr) {
			unrevertedIDs = append(unrevertedIDs, cand.id)
			reverts = append(reverts, rerr)
		}
	}
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

// Cancel records cancellation of the control at (scope, id). It refuses a
// control that is unknown, already cancelled, already superseded, or whose
// interval has ended (acceptance criterion 8).
//
// On a nil error CancelledAt and CancelReason are stored. On a
// *RefusalError nothing was written. On any other plain error the record
// equals what Cancel read: the write's effect has been undone. When the
// restore itself fails, Cancel returns a *UndoError naming the control;
// the record may or may not carry the cancellation.
func (i *Issuer) Cancel(ctx context.Context, scope Scope, id string, reason string) (LifecycleRecord, error) {
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

	now := sep2time.Now().Unix()
	if before.cancelled() {
		return LifecycleRecord{}, refuse(RefusalAlreadyCancelled)
	}
	if before.supersededAsOf(now) {
		return LifecycleRecord{}, refuse(RefusalAlreadySuperseded)
	}
	if ctrl.Interval == nil {
		return LifecycleRecord{}, fmt.Errorf("dercontrol: control has no interval")
	}
	if end := ctrl.Interval.Start + int64(ctrl.Interval.Duration); now >= end {
		return LifecycleRecord{}, refuse(RefusalEnded)
	}

	if err := ctx.Err(); err != nil {
		// Nothing has been written yet, so nothing needs undoing.
		return LifecycleRecord{}, err
	}

	lc := before
	lc.CancelledAt = ptrInt64(now)
	lc.CancelReason = reason
	if err := i.lifecycles.Update(ctx, scopeKey, id, lc); err != nil {
		return i.undoCancelFailure(ctx, scopeKey, id, before, err)
	}
	return lc, nil
}

// undoCancelFailure restores the record Cancel read before its Update
// failed.
func (i *Issuer) undoCancelFailure(ctx context.Context, scopeKey, id string, before LifecycleRecord, cause error) (LifecycleRecord, error) {
	uctx, cancel := undoContext(ctx)
	defer cancel()
	if rerr := i.lifecycles.Update(uctx, scopeKey, id, before); !undoWriteOK(rerr) {
		return LifecycleRecord{}, &UndoError{Step: UndoStepCancel, ID: id, cause: cause, reverts: []error{rerr}}
	}
	return LifecycleRecord{}, fmt.Errorf("dercontrol: cancel: %w", cause)
}

// buildBase maps a request's type and value to the closed set of
// DERControlBase shapes IEEE 2030.5-2018 defines for them (acceptance
// criterion 5). It never reads or sets randomizeStart, randomizeDuration,
// EventStatus, replyTo, responseRequired, mRID or creationTime, because
// CreateRequest carries none of them.
func buildBase(req CreateRequest) (*sep2.DERControlBase, error) {
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
	default:
		return nil, refuse(RefusalUnknownType)
	}
}

// controlShape reduces a DERControlBase to the closed set of shapes v1's
// four request types produce, for the supersede scan's control-set equality
// test. A switch over the four shapes is enough because only controls this
// package created ever carry a lifecycle record, and buildBase produces
// only these four.
func controlShape(b *sep2.DERControlBase) string {
	if b == nil {
		return ""
	}
	switch {
	case b.OpModConnect != nil || b.OpModEnergize != nil:
		return "connect_energize"
	case b.OpModMaxLimW != nil:
		return "max_lim_w"
	case b.OpModFixedPFInjectW != nil:
		return "fixed_pf_inject_w"
	default:
		return "other"
	}
}

// idFromHref recovers the store id this package assigned, from the tail of
// an href it built ("/edev/.../derc/<id>"). A control this package did not
// create may have any other href shape; ok is false for those, which is
// the caller's signal to treat it as not admin-issued.
func idFromHref(href string) (string, bool) {
	const marker = "/derc/"
	idx := strings.LastIndex(href, marker)
	if idx < 0 {
		return "", false
	}
	id := href[idx+len(marker):]
	if id == "" {
		return "", false
	}
	return id, true
}

func ptrInt64(v int64) *int64 { return &v }
