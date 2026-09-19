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

// AsEndDeviceReader narrows an [EndDeviceStore] to an [EndDeviceReader] by
// wrapping, following the same rule as [AsReader]: a plain assignment
// leaves the write methods reachable through the wrapped value's unchanged
// dynamic type.
func AsEndDeviceReader(s EndDeviceReader) EndDeviceReader {
	return endDeviceReaderOnly{
		resourceReaderOnly: resourceReaderOnly[sep2.EndDevice]{reader: s},
		reader:             s,
	}
}

// endDeviceReaderOnly forwards only the [EndDeviceReader] methods of the
// reader it wraps, which may in fact satisfy the wider [EndDeviceStore].
type endDeviceReaderOnly struct {
	resourceReaderOnly[sep2.EndDevice]
	reader EndDeviceReader
}

func (v endDeviceReaderOnly) GetBySFDI(ctx context.Context, sfdi string) (sep2.EndDevice, error) {
	return v.reader.GetBySFDI(ctx, sfdi)
}

func (v endDeviceReaderOnly) GetByLFDI(ctx context.Context, lfdi string) (sep2.EndDevice, error) {
	return v.reader.GetByLFDI(ctx, lfdi)
}
