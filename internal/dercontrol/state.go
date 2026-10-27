package dercontrol

import (
	"context"
	"errors"
	"fmt"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/derhref"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
)

// State is where a DERControl stands at one instant.
type State int

const (
	// Retired is a control that will never act again: it has no interval,
	// its interval has ended, or it is cancelled or superseded.
	Retired State = iota
	// Scheduled is a control that has not yet reached its effective start.
	Scheduled
	// InForce is a control whose interval contains the instant and that is
	// neither cancelled nor superseded.
	InForce
)

// ControlState is the one rule for whether c is in force at now (Unix
// seconds). haveLC says lc was read; without one, the control carries its
// own status, as a control this package did not issue does.
//
// The ended check comes first because DeriveStatus reports an ended
// 2018-edition control as Active forever, so its status cannot be trusted
// for this.
func ControlState(c sep2.DERControl, lc LifecycleRecord, haveLC bool, now int64) State {
	if c.Interval == nil || IntervalEnded(c.Interval, now) {
		return Retired
	}
	if haveLC {
		if lc.cancelled() || lc.supersededAsOf(now) {
			return Retired
		}
	} else if c.EventStatus != nil && c.EventStatus.CurrentStatus >= sep2.EventStatusCancelled {
		return Retired
	}
	if now < max(c.Interval.Start, c.CreationTime) {
		return Scheduled
	}
	return InForce
}

// lifecycleReader is the part of a lifecycle store ControlState's callers read.
type lifecycleReader interface {
	Get(ctx context.Context, parentID, id string) (LifecycleRecord, error)
}

// stateOf loads c's lifecycle record from scopeKey and classifies it. A
// missing record is a control this package did not issue; any other read
// error is returned, never folded into a state.
func stateOf(ctx context.Context, lifecycles lifecycleReader, scopeKey string, c sep2.DERControl, now int64) (State, error) {
	lc, err := lifecycles.Get(ctx, scopeKey, controlIDOf(c.Href))
	switch {
	case err == nil:
		return ControlState(c, lc, true, now), nil
	case errors.Is(err, store.ErrNotFound):
		return ControlState(c, LifecycleRecord{}, false, now), nil
	default:
		return Retired, fmt.Errorf("dercontrol: load lifecycle of %s: %w", c.Href, err)
	}
}

// InForceScopes reports whether any scope of end device edevID holds a
// control in force at now. A control sent to an FSA is stored as one copy per
// device under that device's own scope key, so it counts here with no FSA
// handling. A read error is returned, never reported as false.
func InForceScopes(ctx context.Context, controls store.ScopedReader[sep2.DERControl], lifecycles lifecycleReader, edevID string, now int64) (bool, error) {
	keys, err := controls.Parents(ctx)
	if err != nil {
		return false, fmt.Errorf("dercontrol: list control scopes: %w", err)
	}
	for _, key := range keys {
		edev, _, _, ok := derhref.SplitScopeKey(key)
		if !ok || edev != edevID {
			continue
		}
		list, err := controls.List(ctx, key, store.ListOptions{Unbounded: true})
		if err != nil {
			return false, fmt.Errorf("dercontrol: list scope %s: %w", key, err)
		}
		for _, c := range list.Items {
			st, err := stateOf(ctx, lifecycles, key, c, now)
			if err != nil {
				return false, err
			}
			if st == InForce {
				return true, nil
			}
		}
	}
	return false, nil
}
