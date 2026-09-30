package commitment

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
)

// ErrNoLFDI is FleetOf's error for an EndDevice with no LFDI: its fleet
// cannot be known.
var ErrNoLFDI = errors.New("commitment: EndDevice has no LFDI")

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
		return "", fmt.Errorf("commitment: resolving fleet of EndDevice %s: %w", endDeviceID, err)
	}
	if dev.LFDI == "" {
		return "", fmt.Errorf("%w: EndDevice %s", ErrNoLFDI, endDeviceID)
	}
	// hexBinary allows either case; the management store keys on the
	// canonical upper-case form, so an exact lookup of a lower-case LFDI
	// would miss its manager and fail open.
	lfdi := strings.ToUpper(dev.LFDI)
	manager, err := r.Managers.ManagerOf(ctx, lfdi)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return lfdi, nil
		}
		return "", fmt.Errorf("commitment: looking up the manager of EndDevice %s (LFDI %s): %w", endDeviceID, lfdi, err)
	}
	return strings.ToUpper(manager), nil
}
