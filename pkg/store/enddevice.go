package store

import (
	"context"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
)

// EndDeviceReader is the read-only half of [EndDeviceStore]: [ResourceReader]
// plus the identity lookups, and nothing that writes. This is the handle a
// telemetry consumer or an administrative read surface should hold.
type EndDeviceReader interface {
	ResourceReader[sep2.EndDevice]
	GetBySFDI(ctx context.Context, sfdi string) (sep2.EndDevice, error)
	GetByLFDI(ctx context.Context, lfdi string) (sep2.EndDevice, error)
}

// EndDeviceStore extends ResourceStore with device identity lookups.
type EndDeviceStore interface {
	EndDeviceReader
	ResourceStore[sep2.EndDevice]
}
