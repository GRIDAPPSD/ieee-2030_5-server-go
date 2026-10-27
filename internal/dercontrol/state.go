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
// Cancel, supersede and the effective start come from DeriveStatus, so the
// state can never disagree with the status served on the wire.
func ControlState(c sep2.DERControl, lc LifecycleRecord, haveLC bool, now int64) State {
	if over(c, now) {
		return Retired
	}
	if !haveLC && c.EventStatus != nil && c.EventStatus.CurrentStatus >= sep2.EventStatusCancelled {
		return Retired
	}
	switch DeriveStatus(now, c.CreationTime, c.Interval.Start, lc).CurrentStatus {
	case sep2.EventStatusScheduled:
		return Scheduled
	case sep2.EventStatusActive:
		return InForce
	default:
		return Retired
	}
}

// over reports a control that can never act again at now from its interval
// alone: it has none, or it has ended. It is checked before any lifecycle
// read, and before DeriveStatus, which reports an ended 2018-edition control
// as Active forever.
func over(c sep2.DERControl, now int64) bool {
	return c.Interval == nil || IntervalEnded(c.Interval, now)
}

// lifecycleReader is the part of a lifecycle store ControlState's callers read.
type lifecycleReader interface {
	Get(ctx context.Context, parentID, id string) (LifecycleRecord, error)
}

// stateOf loads c's lifecycle record from scopeKey and classifies it. A
// control that is over is not read at all. A missing record is a control this
// package did not issue; any other read error is returned, never folded into
// a state.
func stateOf(ctx context.Context, lifecycles lifecycleReader, scopeKey string, c sep2.DERControl, now int64) (State, error) {
	if over(c, now) {
		return Retired, nil
	}
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
func InForceScopes(ctx context.Context, controls controlLister, lifecycles lifecycleReader, edevID string, now int64) (bool, error) {
	found := false
	err := eachDeviceState(ctx, controls, lifecycles, edevID, now, func(st State) bool {
		found = st == InForce
		return !found
	})
	if err != nil {
		return false, err
	}
	return found, nil
}

// LiveCount counts the controls in every scope of end device edevID that are
// not Retired at now: scheduled or in force, so neither ended, cancelled nor
// superseded. A read error is returned, never a partial count.
func LiveCount(ctx context.Context, controls controlLister, lifecycles lifecycleReader, edevID string, now int64) (int, error) {
	n := 0
	err := eachDeviceState(ctx, controls, lifecycles, edevID, now, func(st State) bool {
		if st != Retired {
			n++
		}
		return true
	})
	if err != nil {
		return 0, err
	}
	return n, nil
}

// controlLister is the part of a control store the device walks read.
type controlLister interface {
	Parents(ctx context.Context) ([]string, error)
	List(ctx context.Context, parentID string, opts store.ListOptions) (store.ListResult[sep2.DERControl], error)
}

// eachDeviceState calls fn with the state of every control in every scope of
// edevID, stopping when fn returns false.
func eachDeviceState(ctx context.Context, controls controlLister, lifecycles lifecycleReader, edevID string, now int64, fn func(State) bool) error {
	keys, err := controls.Parents(ctx)
	if err != nil {
		return fmt.Errorf("dercontrol: list control scopes: %w", err)
	}
	for _, key := range keys {
		edev, _, _, ok := derhref.SplitScopeKey(key)
		if !ok || edev != edevID {
			continue
		}
		list, err := controls.List(ctx, key, store.ListOptions{Unbounded: true})
		if err != nil {
			return fmt.Errorf("dercontrol: list scope %s: %w", key, err)
		}
		for _, c := range list.Items {
			st, err := stateOf(ctx, lifecycles, key, c, now)
			if err != nil {
				return err
			}
			if !fn(st) {
				return nil
			}
		}
	}
	return nil
}
