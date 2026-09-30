package commitment

import (
	"context"
	"errors"
	"fmt"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
)

// Resolver turns an EndDevice id into the fleet key everything else in this
// package keys on.
type Resolver struct {
	Devices interface {
		Get(ctx context.Context, id string) (sep2.EndDevice, error)
	}
	Managers store.EndDeviceManagementReader
}

// FleetOf returns the fleet key of EndDevice id: the LFDI of its manager
// when it has one, otherwise its own LFDI. A reservation is posted on the
// aggregator's own EndDevice, which has no manager, so it resolves to the
// aggregator's LFDI; a control stored under a managed device resolves
// through ManagerOf to the same LFDI. An EndDevice with an empty LFDI is an
// error, not a fleet of "" (fail closed): an empty string would otherwise
// silently group every misconfigured device into one fleet.
func (r Resolver) FleetOf(ctx context.Context, endDeviceID string) (string, error) {
	dev, err := r.Devices.Get(ctx, endDeviceID)
	if err != nil {
		return "", err
	}
	if dev.LFDI == "" {
		return "", fmt.Errorf("commitment: EndDevice %s has no LFDI", endDeviceID)
	}
	manager, err := r.Managers.ManagerOf(ctx, dev.LFDI)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return dev.LFDI, nil
		}
		return "", err
	}
	return manager, nil
}
