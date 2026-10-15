package fsaprogram

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/handlers/der"
	coresingleton "github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/handlers/singleton"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

// ErrCopyConflict is returned when a device already holds a program, under
// the template's id or with its mRID, that is not that template's copy.
// client-go keys programs by mRID, so two with one mRID on a device would
// collapse into one.
var ErrCopyConflict = errors.New("fsaprogram: device holds a conflicting program")

// CopyProgramStore is the slice of the DERProgram store a copy is written to.
type CopyProgramStore interface {
	Get(ctx context.Context, parentID, id string) (sep2.DERProgram, error)
	Create(ctx context.Context, parentID, id string, resource sep2.DERProgram) error
	Delete(ctx context.Context, parentID, id string) error
	List(ctx context.Context, parentID string, opts store.ListOptions) (store.ListResult[sep2.DERProgram], error)
}

// CopyDefaultStore is the slice of the DefaultDERControl store a copy's
// default control is written to.
type CopyDefaultStore interface {
	Get(ctx context.Context, parentID, id string) (sep2.DefaultDERControl, error)
	Create(ctx context.Context, parentID, id string, resource sep2.DefaultDERControl) error
	Delete(ctx context.Context, parentID, id string) error
}

// OriginStore is the slice of the program origin index a copy is recorded in.
type OriginStore interface {
	Get(ctx context.Context, deviceID, programID string) (memory.ProgramOrigin, error)
	Create(ctx context.Context, deviceID, programID string, o memory.ProgramOrigin) error
	Delete(ctx context.Context, deviceID, programID string) error
}

// Copies are the stores a template copy spans. Every write path uses Ensure,
// so the admin routes and the boot rebuild build byte-identical copies.
type Copies struct {
	Programs CopyProgramStore
	Defaults CopyDefaultStore
	Origins  OriginStore
}

// CopyProgram is the DERProgram a device assigned to fsaID serves for t. The
// issuer requires DERControlListLink to be the program href plus "/derc".
func CopyProgram(deviceID, fsaID string, t memory.ProgramTemplate) sep2.DERProgram {
	href := der.DERProgramHref(deviceID, fsaID, t.ID)
	version := t.Version
	p := sep2.DERProgram{
		MRID:                  t.MRID,
		Description:           t.Description,
		Version:               &version,
		DefaultDERControlLink: &sep2.Link{Href: href + "/dderc"},
		DERControlListLink:    &sep2.ListLink{Href: href + "/derc"},
		Primacy:               t.Primacy,
	}
	p.Href = href
	return p
}

// CopyDefault is the DefaultDERControl CopyProgram's DefaultDERControlLink
// names.
func CopyDefault(deviceID, fsaID string, t memory.ProgramTemplate) sep2.DefaultDERControl {
	base := t.DefaultControlBase.Copy()
	d := sep2.DefaultDERControl{MRID: t.DefaultMRID, DERControlBase: &base}
	d.Href = der.DERProgramHref(deviceID, fsaID, t.ID) + "/dderc"
	return d
}

// DefaultScope is the DefaultDERControl store key the protocol route reads a
// program's default control from.
func DefaultScope(deviceID, fsaID, programID string) string {
	return deviceID + "/" + fsaID + "/" + programID
}

// Check reports whether deviceID already holds the copy of t under fsaID
// (present), or ErrCopyConflict when another program holds its id or mRID.
// The origin is written first, so a program with no origin entry was never a
// copy, even when its content matches.
func (c Copies) Check(ctx context.Context, deviceID, fsaID string, t memory.ProgramTemplate) (present bool, err error) {
	origin, err := c.Origins.Get(ctx, deviceID, t.ID)
	switch {
	case err == nil:
		if origin.Kind != memory.ProgramOriginTemplate || origin.FSAID != fsaID || origin.TemplateID != t.ID {
			return false, fmt.Errorf("%w: program %s on device %s has origin %s", ErrCopyConflict, t.ID, deviceID, origin.Kind)
		}
		present = true
	case errors.Is(err, store.ErrNotFound):
		if _, err := c.Programs.Get(ctx, deviceID, t.ID); err == nil {
			return false, fmt.Errorf("%w: program id %s is taken on device %s", ErrCopyConflict, t.ID, deviceID)
		} else if !errors.Is(err, store.ErrNotFound) {
			return false, fmt.Errorf("reading program %s/%s: %w", deviceID, t.ID, err)
		}
	default:
		return false, fmt.Errorf("reading origin %s/%s: %w", deviceID, t.ID, err)
	}

	list, err := c.Programs.List(ctx, deviceID, store.ListOptions{Unbounded: true})
	if err != nil {
		return false, fmt.Errorf("listing programs of device %s: %w", deviceID, err)
	}
	ownHref := der.DERProgramHref(deviceID, fsaID, t.ID)
	for _, p := range list.Items {
		if p.Href != ownHref && strings.EqualFold(p.MRID, t.MRID) {
			return false, fmt.Errorf("%w: mRID %s is taken on device %s", ErrCopyConflict, t.MRID, deviceID)
		}
	}
	return present, nil
}

// Ensure writes whatever part of t's copy on deviceID is missing: the origin,
// then the default control, then the program. A part already present is left
// as it is. On error, or when the caller later calls the returned undo,
// exactly the parts this call wrote are deleted again.
func (c Copies) Ensure(ctx context.Context, deviceID, fsaID string, t memory.ProgramTemplate) (undo func(context.Context) error, err error) {
	if t.DefaultMRID == "" {
		return nil, fmt.Errorf("program template %s has no default control mRID", t.ID)
	}
	if _, err := c.Check(ctx, deviceID, fsaID, t); err != nil {
		return nil, err
	}
	var undos []func(context.Context) error
	undo = func(ctx context.Context) error {
		var errs []error
		for i := len(undos) - 1; i >= 0; i-- {
			if err := undos[i](ctx); err != nil && !errors.Is(err, store.ErrNotFound) {
				errs = append(errs, err)
			}
		}
		return errors.Join(errs...)
	}
	fail := func(err error) (func(context.Context) error, error) {
		if uerr := undo(ctx); uerr != nil {
			return nil, fmt.Errorf("%w; undoing the copy: %v", err, uerr)
		}
		return nil, err
	}

	if _, err := c.Origins.Get(ctx, deviceID, t.ID); errors.Is(err, store.ErrNotFound) {
		origin := memory.ProgramOrigin{Kind: memory.ProgramOriginTemplate, FSAID: fsaID, TemplateID: t.ID}
		if err := c.Origins.Create(ctx, deviceID, t.ID, origin); err != nil {
			return fail(fmt.Errorf("recording origin %s/%s: %w", deviceID, t.ID, err))
		}
		undos = append(undos, func(ctx context.Context) error { return c.Origins.Delete(ctx, deviceID, t.ID) })
	} else if err != nil {
		return fail(fmt.Errorf("reading origin %s/%s: %w", deviceID, t.ID, err))
	}

	scope := DefaultScope(deviceID, fsaID, t.ID)
	if _, err := c.Defaults.Get(ctx, scope, coresingleton.SingletonKey); errors.Is(err, store.ErrNotFound) {
		if err := c.Defaults.Create(ctx, scope, coresingleton.SingletonKey, CopyDefault(deviceID, fsaID, t)); err != nil {
			return fail(fmt.Errorf("writing default control %s: %w", scope, err))
		}
		undos = append(undos, func(ctx context.Context) error { return c.Defaults.Delete(ctx, scope, coresingleton.SingletonKey) })
	} else if err != nil {
		return fail(fmt.Errorf("reading default control %s: %w", scope, err))
	}

	if _, err := c.Programs.Get(ctx, deviceID, t.ID); errors.Is(err, store.ErrNotFound) {
		if err := c.Programs.Create(ctx, deviceID, t.ID, CopyProgram(deviceID, fsaID, t)); err != nil {
			return fail(fmt.Errorf("writing program %s/%s: %w", deviceID, t.ID, err))
		}
		undos = append(undos, func(ctx context.Context) error { return c.Programs.Delete(ctx, deviceID, t.ID) })
	} else if err != nil {
		return fail(fmt.Errorf("reading program %s/%s: %w", deviceID, t.ID, err))
	}
	return undo, nil
}

// ReconcileCopies writes every missing part of every template copy for every
// recorded assignment. A copy that conflicts with a fixture or device program
// is logged and skipped, so one collision does not stop the boot; any other
// store error is returned.
func ReconcileCopies(ctx context.Context, admin *memory.AdminFSAStore, copies Copies, logf func(format string, args ...any)) error {
	for _, fsaID := range admin.IDs(ctx) {
		tmpls := admin.Templates(ctx, fsaID)
		if len(tmpls) == 0 {
			continue
		}
		for _, deviceID := range admin.Devices(ctx, fsaID) {
			for _, t := range tmpls {
				_, err := copies.Ensure(ctx, deviceID, fsaID, t)
				if errors.Is(err, ErrCopyConflict) {
					logf("rebuild program template %q of FSA %q for device %q skipped: %v", t.ID, fsaID, deviceID, err)
					continue
				}
				if err != nil {
					return fmt.Errorf("rebuild program template %q of FSA %q for device %q: %w", t.ID, fsaID, deviceID, err)
				}
			}
		}
	}
	return nil
}
