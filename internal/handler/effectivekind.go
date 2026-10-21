package handler

import (
	"context"
	"errors"
	"fmt"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
)

// kindGate owns every rule that ties a device kind to management pairs, for
// pair create and rekey, the profile PUT and POST /api/devices. It gates what
// the admin plane lets an operator write; protocol access stays decided by
// pairs alone. A nil profiles or managers handle contributes nothing, and
// each rule runs whenever the handle it needs is set.
type kindGate struct {
	profiles store.DeviceProfileReader
	managers store.EndDeviceManagementReader
}

// effectiveKind is the one answer to "is this LFDI an aggregator or a device":
// the stored profile kind when set, else aggregator when the LFDI manages any
// device, else device. The fallback keeps pairs written before profiles
// existed working, their managers reading as aggregators. stored reports
// whether the answer is the operator's kind rather than the derived one.
// Fleets (HandleListFleets) deliberately count only LFDIs that manage a
// device, so an aggregator with no devices lists no fleet; that is not drift.
func (g kindGate) effectiveKind(ctx context.Context, lfdi string) (kind string, stored bool, err error) {
	if !store.IsAbsent(g.profiles) {
		p, err := g.profiles.Get(ctx, lfdi)
		switch {
		case err == nil:
			if p.Kind != "" {
				return p.Kind, true, nil
			}
		case !errors.Is(err, store.ErrNotFound):
			return "", false, fmt.Errorf("device profile lookup: %w", err)
		}
	}
	if !store.IsAbsent(g.managers) {
		managed, err := g.managers.ManagedBy(ctx, lfdi)
		if err != nil {
			return "", false, fmt.Errorf("management lookup: %w", err)
		}
		if len(managed) > 0 {
			return store.DeviceKindAggregator, false, nil
		}
	}
	return store.DeviceKindDevice, false, nil
}

// managerRefusal returns the 409 message when lfdi may not manage others, or
// "". A manager stored as a device is refused. A derived device (no stored
// kind, no pairs) is only the absence of information, so a first pair for an
// LFDI nobody has classified is allowed; refusing it would make every new
// aggregator need a profile first. A manager that is itself managed is
// refused too, so the order the pairs are made in cannot build a three-level
// fleet.
func (g kindGate) managerRefusal(ctx context.Context, lfdi string) (string, error) {
	k, stored, err := g.effectiveKind(ctx, lfdi)
	if err != nil {
		return "", err
	}
	if k == store.DeviceKindDevice && stored {
		return "manager " + lfdi + " is a device and cannot manage others", nil
	}
	if store.IsAbsent(g.managers) {
		return "", nil
	}
	parent, err := g.managers.ManagerOf(ctx, lfdi)
	switch {
	case errors.Is(err, store.ErrNotFound):
		return "", nil
	case err != nil:
		return "", fmt.Errorf("management lookup: %w", err)
	}
	return "manager " + lfdi + " is managed by " + parent + " and cannot manage others", nil
}

// managedRefusal returns the 409 message when lfdi may not be managed, or "".
// An aggregator cannot be managed: it would appear in two fleets.
func (g kindGate) managedRefusal(ctx context.Context, lfdi string) (string, error) {
	k, _, err := g.effectiveKind(ctx, lfdi)
	if err != nil || k != store.DeviceKindAggregator {
		return "", err
	}
	return "managed " + lfdi + " is an aggregator and cannot be managed", nil
}

// profileKindRefusal returns the 409 message when recording kind for lfdi
// contradicts its pairs, or "". A managed device cannot become an aggregator
// and a manager cannot become a device. An empty kind is always allowed: the
// derived kind then follows the pairs.
func (g kindGate) profileKindRefusal(ctx context.Context, lfdi, kind string) (string, error) {
	if store.IsAbsent(g.managers) {
		return "", nil
	}
	switch kind {
	case store.DeviceKindAggregator:
		manager, err := g.managers.ManagerOf(ctx, lfdi)
		switch {
		case errors.Is(err, store.ErrNotFound):
			return "", nil
		case err != nil:
			return "", fmt.Errorf("management lookup: %w", err)
		}
		return "device is managed by " + manager, nil
	case store.DeviceKindDevice:
		managed, err := g.managers.ManagedBy(ctx, lfdi)
		if err != nil {
			return "", fmt.Errorf("management lookup: %w", err)
		}
		if len(managed) > 0 {
			return "device manages " + managed[0], nil
		}
	}
	return "", nil
}
