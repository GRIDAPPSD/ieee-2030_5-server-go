package memory

import (
	"errors"
	"fmt"
)

// ErrAdminFSAInUse is returned when an admin FSA cannot be deleted because
// it still has programs attached or devices assigned. The caller must
// detach/unlink first; the store never silently cascades.
var ErrAdminFSAInUse = errors.New("admin FSA still has programs or devices attached")

// ErrAdminFSAHasTemplates is returned when an admin FSA cannot be deleted
// because program templates still live on it. It matches ErrAdminFSAInUse
// too, so a caller that only knows the older error still refuses.
var ErrAdminFSAHasTemplates = fmt.Errorf("%w: program templates remain", ErrAdminFSAInUse)

// ErrInvalidProgramTemplate is returned for a template the store will not
// hold: an empty id or mRID, or an update that changes the mRID.
var ErrInvalidProgramTemplate = errors.New("invalid program template")

// ErrAdminFSADeviceOnly is returned when a device is assigned to a
// device-only FSA bound to a different device.
var ErrAdminFSADeviceOnly = errors.New("admin FSA is bound to one device")

// ErrInvalidDeviceOnly is returned for a device-only binding the store will
// not hold, such as one with no device.
var ErrInvalidDeviceOnly = errors.New("invalid device-only FSA")

// ErrInvalidFleet is returned for a fleet binding the store will not record.
var ErrInvalidFleet = errors.New("invalid fleet FSA")
