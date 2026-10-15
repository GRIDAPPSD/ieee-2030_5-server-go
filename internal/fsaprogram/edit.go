package fsaprogram

import (
	"context"
	"errors"
	"fmt"
	"math"
	"reflect"
	"sync"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	coresingleton "github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/handlers/singleton"
)

// ErrVersionExhausted is returned when an edit would raise a version past
// 65535. Wrapping to 0 would reuse an identifier IEEE 2030.5 requires to be
// distinct per revision, so the edit is refused instead.
var ErrVersionExhausted = errors.New("fsaprogram: version exhausted")

// ErrHalfApplied is returned when EditDefault wrote the default control, could
// not write the program, and could not put the default back. The two stores
// disagree; it deliberately does not match store.ErrNotFound or any other
// cause, so a caller cannot answer it as a clean refusal.
var ErrHalfApplied = errors.New("fsaprogram: edit half applied")

// Ref addresses a program in the DERProgram store.
type Ref struct {
	DeviceID  string
	ProgramID string
}

// ProgramStore is the slice of the DERProgram store Edit reads and writes.
type ProgramStore interface {
	Get(ctx context.Context, parentID, id string) (sep2.DERProgram, error)
	Update(ctx context.Context, parentID, id string, resource sep2.DERProgram) error
}

// DefaultStore is the slice of the DefaultDERControl store EditDefault reads
// and writes.
type DefaultStore interface {
	Get(ctx context.Context, parentID, id string) (sep2.DefaultDERControl, error)
	Update(ctx context.Context, parentID, id string, resource sep2.DefaultDERControl) error
}

// editMu serializes every edit: each one is a read-modify-write, and two
// racing edits would otherwise both write old+1.
var editMu sync.Mutex

// NextVersion returns the version after v, reading a missing version as 0.
func NextVersion(v *uint16) (*uint16, error) {
	var cur uint16
	if v != nil {
		cur = *v
	}
	if cur == math.MaxUint16 {
		return nil, ErrVersionExhausted
	}
	next := cur + 1
	return &next, nil
}

// Edit applies mutate to the program at ref and stores the result with
// its version raised by one. A mutate that leaves the program unchanged
// writes nothing and keeps the version. Every program edit goes through here
// so none can skip the increment (the boot href migration included).
func Edit(ctx context.Context, programs ProgramStore, ref Ref, mutate func(*sep2.DERProgram) error) error {
	editMu.Lock()
	defer editMu.Unlock()

	before, err := programs.Get(ctx, ref.DeviceID, ref.ProgramID)
	if err != nil {
		return fmt.Errorf("reading program %s/%s: %w", ref.DeviceID, ref.ProgramID, err)
	}
	after := before.Copy()
	after.Subscribable = cloneUint8(after.Subscribable)
	if err := mutate(&after); err != nil {
		return err
	}
	if reflect.DeepEqual(before, after) {
		return nil
	}
	if after.Version, err = NextVersion(before.Version); err != nil {
		return err
	}
	if err := programs.Update(ctx, ref.DeviceID, ref.ProgramID, after); err != nil {
		return fmt.Errorf("writing program %s/%s: %w", ref.DeviceID, ref.ProgramID, err)
	}
	return nil
}

// EditDefault applies mutate to the default control stored under scope. A
// changed default raises its own version and the owning program's, because the
// program's DefaultDERControlLink now names a different representation. Both
// versions are checked before either write, and a failed program write puts the
// default back.
func EditDefault(ctx context.Context, programs ProgramStore, defaults DefaultStore, ref Ref, scope string, mutate func(*sep2.DefaultDERControl) error) error {
	editMu.Lock()
	defer editMu.Unlock()

	before, err := defaults.Get(ctx, scope, coresingleton.SingletonKey)
	if err != nil {
		return fmt.Errorf("reading default control %s: %w", scope, err)
	}
	after := before.Copy()
	after.Subscribable = cloneUint8(after.Subscribable)
	if err := mutate(&after); err != nil {
		return err
	}
	if reflect.DeepEqual(before, after) {
		return nil
	}
	program, err := programs.Get(ctx, ref.DeviceID, ref.ProgramID)
	if err != nil {
		return fmt.Errorf("reading program %s/%s: %w", ref.DeviceID, ref.ProgramID, err)
	}
	if after.Version, err = NextVersion(before.Version); err != nil {
		return err
	}
	if program.Version, err = NextVersion(program.Version); err != nil {
		return err
	}
	if err := defaults.Update(ctx, scope, coresingleton.SingletonKey, after); err != nil {
		return fmt.Errorf("writing default control %s: %w", scope, err)
	}
	if err := programs.Update(ctx, ref.DeviceID, ref.ProgramID, program); err != nil {
		err = fmt.Errorf("writing program %s/%s: %w", ref.DeviceID, ref.ProgramID, err)
		if rerr := defaults.Update(ctx, scope, coresingleton.SingletonKey, before); rerr != nil {
			// %v, not %w: the causes must not match store sentinels.
			return fmt.Errorf("%w: default control %s is at the new content: %v; restoring it: %v", ErrHalfApplied, scope, err, rerr)
		}
		return err
	}
	return nil
}

// cloneUint8 copies the Subscribable attribute, which core's Copy shares, so a
// mutate that changes it in place cannot also change the "before" it is
// compared with.
func cloneUint8(v *uint8) *uint8 {
	if v == nil {
		return nil
	}
	c := *v
	return &c
}
