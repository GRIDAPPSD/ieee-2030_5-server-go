package store

import (
	"context"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
)

// EndDeviceStore extends ResourceStore with device identity lookups.
type EndDeviceStore interface {
	ResourceStore[sep2.EndDevice]
	GetBySFDI(ctx context.Context, sfdi string) (sep2.EndDevice, error)
	GetByLFDI(ctx context.Context, lfdi string) (sep2.EndDevice, error)
}
