package store

import (
	"context"
	"errors"
)

// Device kinds a [DeviceProfile] may carry. An empty Kind means the operator
// has not said.
const (
	DeviceKindAggregator = "aggregator"
	DeviceKindDevice     = "device"
)

// ErrInvalidDeviceProfile means Put was given a profile it will not record: an
// empty or non-canonical LFDI, or a kind that is neither empty nor one of the
// Device* constants. It is a caller error, never a backend condition. The
// name's character rules belong to the admin plane, which owns the one
// validator; the store enforces only uniqueness.
var ErrInvalidDeviceProfile = errors.New("invalid device profile")

// DeviceProfile is the server-only admin metadata for one device: a
// human-readable name and a kind. It is keyed by LFDI, the device's
// identifier of record, and may name an LFDI with no EndDevice yet.
type DeviceProfile struct {
	LFDI string `json:"lfdi"`
	Name string `json:"name"`
	Kind string `json:"kind"`
}

// DeviceProfileReader is the read-only half of [DeviceProfileStore].
type DeviceProfileReader interface {
	// Get returns the profile of lfdi, or ErrNotFound when it has none.
	Get(ctx context.Context, lfdi string) (DeviceProfile, error)

	// List returns every profile sorted by LFDI, in a slice the caller owns.
	List(ctx context.Context) ([]DeviceProfile, error)
}

// DeviceProfileStore records a name and kind per device LFDI.
//
// Names are unique among non-empty names, compared ignoring ASCII case. LFDIs
// are exact: an implementation refuses one that is not canonical (non-empty,
// upper case, no surrounding space) rather than folding it.
type DeviceProfileStore interface {
	DeviceProfileReader

	// Put records p, replacing any earlier profile of p.LFDI. It returns
	// ErrAlreadyExists when another LFDI holds the same name, and
	// ErrInvalidDeviceProfile for a non-canonical LFDI or an unknown kind.
	Put(ctx context.Context, p DeviceProfile) error

	// Delete removes the profile of lfdi, or returns ErrNotFound.
	Delete(ctx context.Context, lfdi string) error
}
