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
	// to create (Wren HIGH-1, Tess T3).
	candidates, err := i.computeSupersedes(ctx, scopeKey, existing.Items, base, start, start+durationSeconds)
	if err != nil {
		return Result{}, err
	}

	if err := i.controls.Create(ctx, scopeKey, id, ctrl); err != nil {
		return Result{}, fmt.Errorf("dercontrol: store control: %w", err)
	}
	if err := i.lifecycles.Create(ctx, scopeKey, id, LifecycleRecord{}); err != nil {
		// The control must never be left without its lifecycle record
		// (Wren HIGH-2): undo the store it already committed.
		if derr := i.controls.Delete(ctx, scopeKey, id); derr != nil {
			return Result{}, fmt.Errorf("dercontrol: store lifecycle: %w (rollback: delete control failed: %w)", err, derr)
		}
		return Result{}, fmt.Errorf("dercontrol: store lifecycle: %w", err)
	}

	supersedes, err := i.applySupersedes(ctx, scopeKey, candidates, start, mrid)
	if err != nil {
		return Result{}, err
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
// in order. If a candidate's Update fails, every candidate already marked
// in this call is restored to its pre-call state before the error is
// returned, so a partial failure here never leaves some candidates marked
// by a control whose Issue call is about to report failure to its caller.
// It returns the mRIDs it marked.
func (i *Issuer) applySupersedes(ctx context.Context, scopeKey string, candidates []supersedeCandidate, newStart int64, newMRID string) ([]string, error) {
	var supersedes []string
	for n, cand := range candidates {
		lc := cand.before
		lc.SupersededAt = ptrInt64(newStart)
		lc.SupersededBy = newMRID
		if err := i.lifecycles.Update(ctx, scopeKey, cand.id, lc); err != nil {
			for _, applied := range candidates[:n] {
				if rerr := i.lifecycles.Update(ctx, scopeKey, applied.id, applied.before); rerr != nil {
					return nil, fmt.Errorf("dercontrol: mark superseded: %w (revert of %s failed: %w)", err, applied.id, rerr)
				}
			}
			return nil, fmt.Errorf("dercontrol: mark superseded: %w", err)
		}
		supersedes = append(supersedes, cand.mrid)
	}
	return supersedes, nil
}

// Cancel records cancellation of the control at (scope, id). It refuses a
// control that is unknown, already cancelled, already superseded, or whose
// interval has ended (acceptance criterion 8).
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
	lc, err := i.lifecycles.Get(ctx, scopeKey, id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return LifecycleRecord{}, refuse(RefusalControlNotFound)
		}
		return LifecycleRecord{}, fmt.Errorf("dercontrol: load lifecycle: %w", err)
	}

	now := sep2time.Now().Unix()
	if lc.cancelled() {
		return LifecycleRecord{}, refuse(RefusalAlreadyCancelled)
	}
	if lc.supersededAsOf(now) {
		return LifecycleRecord{}, refuse(RefusalAlreadySuperseded)
	}
	if ctrl.Interval == nil {
		return LifecycleRecord{}, fmt.Errorf("dercontrol: control has no interval")
	}
	if end := ctrl.Interval.Start + int64(ctrl.Interval.Duration); now >= end {
		return LifecycleRecord{}, refuse(RefusalEnded)
	}

	lc.CancelledAt = ptrInt64(now)
	lc.CancelReason = reason
	if err := i.lifecycles.Update(ctx, scopeKey, id, lc); err != nil {
		return LifecycleRecord{}, fmt.Errorf("dercontrol: update lifecycle: %w", err)
	}
	return lc, nil
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
		if req.PowerFactor.Displacement < 1 || req.PowerFactor.Displacement > 1000 {
			return nil, refuse(RefusalValueOutOfRange)
		}
		return &sep2.DERControlBase{OpModFixedPFInjectW: &sep2.FixedPowerFactor{
			Displacement: req.PowerFactor.Displacement,
			Excitation:   req.PowerFactor.Excitation,
			Multiplier:   -3,
		}}, nil
	default:
		return nil, refuse(RefusalUnknownType)
	}
}

// controlShape reduces a DERControlBase to the closed set of shapes v1's
// four request types produce, for supersede's control-set equality test
// (design section 5: "the set of non-nil DERControlBase fields is equal").
// A switch over the four shapes is enough because only controls this
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
