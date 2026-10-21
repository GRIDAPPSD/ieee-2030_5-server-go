package fsaprogram

import (
	"context"
	"errors"
	"fmt"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
)

// OnExisting says what Assign does when the device is already assigned to the
// FSA. It has no zero value, so a caller has to choose.
type OnExisting int

const (
	// RefuseIfAssigned fails with store.ErrAlreadyExists: an operator asking
	// to assign a device twice has made a duplicate request.
	RefuseIfAssigned OnExisting = iota + 1
	// CompleteIfAssigned accepts the assignment and writes whatever else is
	// missing: the caller found the pair incomplete, so an existing assignment
	// is an earlier partial write.
	CompleteIfAssigned
)

// Stage names the write Assign was making when it failed. The values are the
// cause strings the device path logs.
type Stage string

const (
	StageAssign      Stage = "assign"
	StageScopedWrite Stage = "scope_materialize"
)

// AssignError is the failure of one Assign write. Err is the store's error.
type AssignError struct {
	Stage Stage
	Err   error
}

func (e *AssignError) Error() string { return fmt.Sprintf("%s: %v", e.Stage, e.Err) }
func (e *AssignError) Unwrap() error { return e.Err }

// Step undoes one write Assign made. Part names it for the log line written
// when the undo fails.
type Step struct {
	Part string
	Undo func(context.Context) error
}

// AssignmentStore is the slice of the admin FSA store Assign writes.
type AssignmentStore interface {
	AssignDevice(ctx context.Context, fsaID, deviceID string) error
	UnassignDevice(ctx context.Context, fsaID, deviceID string) error
}

// Assign records deviceID's assignment to fsaID and materializes fsa into the
// device's own FSA store. It returns the undo of every write it made, oldest
// first, and on failure the steps written before the failing one with an
// *AssignError. A nil devices skips the materialization.
//
// The assignment is the oldest write, so a caller that cannot undo it leaves
// a record the boot rebuild completes. A scoped record that already exists is
// left as it is under both OnExisting values.
func Assign(ctx context.Context, admin AssignmentStore, devices DeviceFSAWriter, deviceID, fsaID string, fsa sep2.FunctionSetAssignments, onExisting OnExisting) ([]Step, error) {
	var steps []Step
	switch err := admin.AssignDevice(ctx, fsaID, deviceID); {
	case err == nil:
		steps = append(steps, Step{Part: "assignment", Undo: func(ctx context.Context) error {
			return admin.UnassignDevice(ctx, fsaID, deviceID)
		}})
	case errors.Is(err, store.ErrAlreadyExists) && onExisting == CompleteIfAssigned:
	default:
		return steps, &AssignError{Stage: StageAssign, Err: err}
	}
	if devices == nil {
		return steps, nil
	}
	switch err := devices.Create(ctx, deviceID, fsaID, Scoped(deviceID, fsaID, fsa)); {
	case err == nil:
		steps = append(steps, Step{Part: "scoped_fsa", Undo: func(ctx context.Context) error {
			return devices.Delete(ctx, deviceID, fsaID)
		}})
	case errors.Is(err, store.ErrAlreadyExists):
	default:
		return steps, &AssignError{Stage: StageScopedWrite, Err: err}
	}
	return steps, nil
}
