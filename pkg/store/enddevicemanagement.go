package store

import (
	"context"
	"errors"
)

// ErrInvalidManagementPair means Assign was given a pair it will not record:
// an empty or non-canonical LFDI, or a device named as its own manager. It is
// a caller error, never a backend condition, so an admin surface answers it
// with 400.
var ErrInvalidManagementPair = errors.New("invalid EndDevice management pair")

// EndDeviceManagementStore records which LFDI manages which EndDevice.
//
// A pair (manager, managed) lets the manager reach the managed device's
// resources within the scope the protocol router delegates. Each managed LFDI
// has at most one manager, and management is not transitive.
//
// Pairs are keyed by LFDI, never by the URL index: the index follows the
// certificate, the LFDI is the device's identifier of record. Pairs are
// provisioning data and outlive the EndDevice records they name.
//
// Every comparison is exact. An implementation stores LFDIs as given and
// refuses input that is not in canonical form (non-empty, upper case, no
// surrounding space) rather than folding it, so a lookup with a non-canonical
// LFDI misses.
type EndDeviceManagementStore interface {
	// ManagerOf returns the LFDI managing managedLFDI, or ErrNotFound when
	// the device is unmanaged.
	ManagerOf(ctx context.Context, managedLFDI string) (string, error)

	// ManagedBy returns the LFDIs managerLFDI manages, sorted ascending, in a
	// slice the caller owns. No pairs is an empty result, not ErrNotFound.
	ManagedBy(ctx context.Context, managerLFDI string) ([]string, error)

	// Assign records managerLFDI as the manager of managedLFDI. Assigning the
	// same pair again succeeds. It returns ErrAlreadyExists when another
	// manager holds the device, and ErrInvalidManagementPair for an empty or
	// non-canonical LFDI or a device managing itself.
	Assign(ctx context.Context, managerLFDI, managedLFDI string) error

	// Unassign removes the manager of managedLFDI, or returns ErrNotFound
	// when the device is unmanaged.
	Unassign(ctx context.Context, managedLFDI string) error
}
