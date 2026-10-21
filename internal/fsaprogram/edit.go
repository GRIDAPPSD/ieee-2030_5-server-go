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
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
)

// ErrVersionExhausted is returned when an edit would raise a version past
// 65535. Wrapping to 0 would reuse an identifier IEEE 2030.5 requires to be
// distinct per revision, so the edit is refused instead.
var ErrVersionExhausted = errors.New("fsaprogram: version exhausted")

// ErrHalfApplied is returned when an edit wrote the default control, could
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
// or EditWithDefault so none can skip the increment (the boot href migration
// included).
func Edit(ctx context.Context, programs ProgramStore, ref Ref, mutate func(*sep2.DERProgram) error) error {
	_, err := EditWithDefault(ctx, programs, nil, ref, "", mutate, nil)
	return err
}

// EditDefault applies mutate to the default control stored under scope. A
// changed default raises its own version and the owning program's, because the
// program's DefaultDERControlLink now names a different representation. Both
// versions are checked before either write, and a failed program write puts the
// default back.
func EditDefault(ctx context.Context, programs ProgramStore, defaults DefaultStore, ref Ref, scope string, mutate func(*sep2.DefaultDERControl) error) error {
	_, err := EditWithDefault(ctx, programs, defaults, ref, scope, nil, mutate)
	return err
}

// Changes says which representations an edit wrote. The program is written
// whenever anything changed, since its version is raised either way.
type Changes struct {
	Program bool
	Default bool
}

// EditWithDefault is one edit of a program and of the default control stored
// under scope; a nil mutate leaves that side alone. The program's version is
// raised once when either side changed, and the default's when it changed.
// Both versions are checked before either write, and a failed program write
// puts the default back.
func EditWithDefault(ctx context.Context, programs ProgramStore, defaults DefaultStore, ref Ref, scope string,
	mutateProgram func(*sep2.DERProgram) error, mutateDefault func(*sep2.DefaultDERControl) error) (Changes, error) {
	editMu.Lock()
	defer editMu.Unlock()
	return editLocked(ctx, programs, defaults, nil, ref, scope, mutateProgram, mutateDefault)
}

// DefaultRemover is the slice of the DefaultDERControl store a removal needs.
type DefaultRemover interface {
	DefaultStore
	Delete(ctx context.Context, parentID, id string) error
}

// EditClearingDefault is EditWithDefault with the default control stored under
// scope removed instead of mutated. The program stops linking to the default
// first, and the default is deleted after, so the link never dangles: a failed
// delete leaves an unlinked default, and the call is repeatable. A program
// with no default is edited alone, so clearing twice is not an error.
func EditClearingDefault(ctx context.Context, programs ProgramStore, defaults DefaultRemover, ref Ref, scope string,
	mutateProgram func(*sep2.DERProgram) error) (Changes, error) {
	editMu.Lock()
	defer editMu.Unlock()
	return editLocked(ctx, programs, defaults, defaults.Delete, ref, scope, mutateProgram, nil)
}

// editLocked is EditWithDefault under editMu. A non-nil remove deletes the
// stored default after the program is written, and unlinks it from the program.
func editLocked(ctx context.Context, programs ProgramStore, defaults DefaultStore,
	remove func(ctx context.Context, parentID, id string) error, ref Ref, scope string,
	mutateProgram func(*sep2.DERProgram) error, mutateDefault func(*sep2.DefaultDERControl) error) (Changes, error) {
	var dBefore, dAfter sep2.DefaultDERControl
	var err error
	dChanged := false
	if remove != nil {
		_, err := defaults.Get(ctx, scope, coresingleton.SingletonKey)
		switch {
		case err == nil:
			dChanged = true
		case errors.Is(err, store.ErrNotFound):
			remove = nil
		default:
			return Changes{}, fmt.Errorf("reading default control %s: %w", scope, err)
		}
	}
	if mutateDefault != nil {
		if dBefore, err = defaults.Get(ctx, scope, coresingleton.SingletonKey); err != nil {
			return Changes{}, fmt.Errorf("reading default control %s: %w", scope, err)
		}
		dAfter = dBefore.Copy()
		dAfter.Subscribable = cloneUint8(dAfter.Subscribable)
		if err := mutateDefault(&dAfter); err != nil {
			return Changes{}, err
		}
		dChanged = !reflect.DeepEqual(dBefore, dAfter)
		if !dChanged && mutateProgram == nil {
			return Changes{}, nil
		}
	}

	pBefore, err := programs.Get(ctx, ref.DeviceID, ref.ProgramID)
	if err != nil {
		return Changes{}, fmt.Errorf("reading program %s/%s: %w", ref.DeviceID, ref.ProgramID, err)
	}
	pAfter := pBefore.Copy()
	pAfter.Subscribable = cloneUint8(pAfter.Subscribable)
	if mutateProgram != nil {
		if err := mutateProgram(&pAfter); err != nil {
			return Changes{}, err
		}
	}
	if remove != nil {
		pAfter.DefaultDERControlLink = nil
	}
	if !dChanged && reflect.DeepEqual(pBefore, pAfter) {
		return Changes{}, nil
	}
	if dChanged && remove == nil {
		if dAfter.Version, err = NextVersion(dBefore.Version); err != nil {
			return Changes{}, err
		}
	}
	if pAfter.Version, err = NextVersion(pBefore.Version); err != nil {
		return Changes{}, err
	}

	if dChanged && remove == nil {
		if err := defaults.Update(ctx, scope, coresingleton.SingletonKey, dAfter); err != nil {
			return Changes{}, fmt.Errorf("writing default control %s: %w", scope, err)
		}
	}
	if err := programs.Update(ctx, ref.DeviceID, ref.ProgramID, pAfter); err != nil {
		err = fmt.Errorf("writing program %s/%s: %w", ref.DeviceID, ref.ProgramID, err)
		if !dChanged || remove != nil {
			return Changes{}, err
		}
		if rerr := defaults.Update(ctx, scope, coresingleton.SingletonKey, dBefore); rerr != nil {
			// %v, not %w: the causes must not match store sentinels.
			return Changes{}, fmt.Errorf("%w: default control %s is at the new content: %v; restoring it: %v", ErrHalfApplied, scope, err, rerr)
		}
		return Changes{}, err
	}
	if remove != nil {
		if err := remove(ctx, scope, coresingleton.SingletonKey); err != nil && !errors.Is(err, store.ErrNotFound) {
			return Changes{Program: true}, fmt.Errorf("removing default control %s: %w", scope, err)
		}
	}
	return Changes{Program: true, Default: dChanged}, nil
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
