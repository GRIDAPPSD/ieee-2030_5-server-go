package handler

import (
	"context"
	"errors"
	"fmt"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
)

// effectiveKind is the one answer to "is this LFDI an aggregator or a device":
// the stored profile kind when set, else aggregator when the LFDI manages
// any device, else device. The fallback keeps pairs written before profiles
// existed working, their managers reading as aggregators. It only gates what
// the admin plane lets an operator write; protocol access stays decided by
// pairs alone.
//
// stored reports whether the answer is the operator's stored kind rather
// than the derived one. A nil profiles or managers handle contributes
// nothing, so a profile that is not stored falls through to the derived
// answer.
func effectiveKind(ctx context.Context, profiles store.DeviceProfileReader, managers store.EndDeviceManagementReader, lfdi string) (kind string, stored bool, err error) {
	if !store.IsAbsent(profiles) {
		p, err := profiles.Get(ctx, lfdi)
		switch {
		case err == nil:
			if p.Kind != "" {
				return p.Kind, true, nil
			}
		case !errors.Is(err, store.ErrNotFound):
			return "", false, fmt.Errorf("device profile lookup: %w", err)
		}
	}
	if !store.IsAbsent(managers) {
		managed, err := managers.ManagedBy(ctx, lfdi)
		if err != nil {
			return "", false, fmt.Errorf("management lookup: %w", err)
		}
		if len(managed) > 0 {
			return store.DeviceKindAggregator, false, nil
		}
	}
	return store.DeviceKindDevice, false, nil
}

// managerKindRefusal returns the 409 message when lfdi, as a manager, is
// stored as a device, or "". A derived device (no stored kind, no pairs) is
// only the absence of information, so a first pair for an LFDI nobody has
// classified is allowed; refusing it would make every new aggregator need a
// profile before its first pair. An absent profile store turns the gate off.
func managerKindRefusal(ctx context.Context, profiles store.DeviceProfileReader, managers store.EndDeviceManagementReader, lfdi string) (string, error) {
	if store.IsAbsent(profiles) {
		return "", nil
	}
	k, stored, err := effectiveKind(ctx, profiles, managers, lfdi)
	if err != nil || k != store.DeviceKindDevice || !stored {
		return "", err
	}
	return "manager " + lfdi + " is a device and cannot manage others", nil
}

// managedKindRefusal returns the 409 message when lfdi, as a managed device,
// is an aggregator, or "". It keeps fleets two-level: an aggregator managed
// by another would appear in two fleets.
func managedKindRefusal(ctx context.Context, profiles store.DeviceProfileReader, managers store.EndDeviceManagementReader, lfdi string) (string, error) {
	if store.IsAbsent(profiles) {
		return "", nil
	}
	k, _, err := effectiveKind(ctx, profiles, managers, lfdi)
	if err != nil || k != store.DeviceKindAggregator {
		return "", err
	}
	return "managed " + lfdi + " is an aggregator and cannot be managed", nil
}
