package fsaprogram

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/derhref"
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
	Parents(ctx context.Context) ([]string, error)
	IDs(deviceID string) []string
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

// The parts of a copy, in the order Ensure writes them. Every removal runs
// in reverse and stops at its first failure, so the parts left stored are
// always a prefix of this order: an origin naming the template, which Check
// reads as the copy and Ensure completes.
const (
	PartOrigin  = "origin"
	PartDefault = "default"
	PartProgram = "program"
)

// PartError names the part of a template copy a removal could not delete.
// Every part written before it in Ensure's order is still stored.
type PartError struct {
	DeviceID   string
	TemplateID string
	Part       string
	Err        error
}

func (e *PartError) Error() string {
	return fmt.Sprintf("removing the %s of template %s's copy on device %s: %v", e.Part, e.TemplateID, e.DeviceID, e.Err)
}

func (e *PartError) Unwrap() error { return e.Err }

// deletePart deletes one part of a copy; a part already gone counts as
// deleted.
func (c Copies) deletePart(ctx context.Context, deviceID, fsaID, programID, part string) error {
	var err error
	switch part {
	case PartOrigin:
		err = c.Origins.Delete(ctx, deviceID, programID)
	case PartDefault:
		err = c.Defaults.Delete(ctx, DefaultScope(deviceID, fsaID, programID), coresingleton.SingletonKey)
	case PartProgram:
		err = c.Programs.Delete(ctx, deviceID, programID)
	default:
		err = fmt.Errorf("unknown copy part %q", part)
	}
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return &PartError{DeviceID: deviceID, TemplateID: programID, Part: part, Err: err}
	}
	return nil
}

// deleteParts deletes parts in reverse order and stops at the first failure.
func (c Copies) deleteParts(ctx context.Context, deviceID, fsaID, programID string, parts []string) error {
	for i := len(parts) - 1; i >= 0; i-- {
		if err := c.deletePart(ctx, deviceID, fsaID, programID, parts[i]); err != nil {
			return err
		}
	}
	return nil
}

// Ensure writes whatever part of t's copy on deviceID is missing: the origin,
// then the default control, then the program. A part already present is left
// as it is. On error, or when the caller later calls the returned undo, the
// parts this call wrote are deleted again; an undo that fails returns a
// *PartError and leaves a prefix of the copy for Ensure to complete.
func (c Copies) Ensure(ctx context.Context, deviceID, fsaID string, t memory.ProgramTemplate) (undo func(context.Context) error, err error) {
	return c.ensure(ctx, deviceID, fsaID, t, memory.ProgramOrigin{Kind: memory.ProgramOriginTemplate, FSAID: fsaID, TemplateID: t.ID})
}

// EnsureDevice writes the program t describes on deviceID alone, recorded as a
// device program. Unlike Ensure it never completes a partial write: an id or
// mRID the device already holds in any form is ErrCopyConflict. The undo
// contract is Ensure's.
func (c Copies) EnsureDevice(ctx context.Context, deviceID, fsaID string, t memory.ProgramTemplate) (undo func(context.Context) error, err error) {
	return c.ensure(ctx, deviceID, fsaID, t, memory.ProgramOrigin{Kind: memory.ProgramOriginDevice, FSAID: fsaID})
}

func (c Copies) ensure(ctx context.Context, deviceID, fsaID string, t memory.ProgramTemplate, origin memory.ProgramOrigin) (undo func(context.Context) error, err error) {
	if t.DefaultMRID == "" {
		return nil, fmt.Errorf("program template %s has no default control mRID", t.ID)
	}
	present, err := c.Check(ctx, deviceID, fsaID, t)
	if err != nil {
		return nil, err
	}
	if present && origin.Kind == memory.ProgramOriginDevice {
		return nil, fmt.Errorf("%w: program id %s is taken on device %s", ErrCopyConflict, t.ID, deviceID)
	}
	var written []string
	undo = func(ctx context.Context) error {
		return c.deleteParts(ctx, deviceID, fsaID, t.ID, written)
	}
	fail := func(err error) (func(context.Context) error, error) {
		return nil, errors.Join(err, undo(ctx))
	}

	scope := DefaultScope(deviceID, fsaID, t.ID)
	writes := []struct {
		part   string
		get    func() error
		create func() error
	}{
		{PartOrigin,
			func() error { _, err := c.Origins.Get(ctx, deviceID, t.ID); return err },
			func() error {
				return c.Origins.Create(ctx, deviceID, t.ID, origin)
			}},
		{PartDefault,
			func() error { _, err := c.Defaults.Get(ctx, scope, coresingleton.SingletonKey); return err },
			func() error {
				return c.Defaults.Create(ctx, scope, coresingleton.SingletonKey, CopyDefault(deviceID, fsaID, t))
			}},
		{PartProgram,
			func() error { _, err := c.Programs.Get(ctx, deviceID, t.ID); return err },
			func() error { return c.Programs.Create(ctx, deviceID, t.ID, CopyProgram(deviceID, fsaID, t)) }},
	}
	for _, w := range writes {
		switch err := w.get(); {
		case err == nil:
		case errors.Is(err, store.ErrNotFound):
			if err := w.create(); err != nil {
				return fail(fmt.Errorf("writing the %s of template %s's copy on device %s: %w", w.part, t.ID, deviceID, err))
			}
			written = append(written, w.part)
		default:
			return fail(fmt.Errorf("reading the %s of template %s's copy on device %s: %w", w.part, t.ID, deviceID, err))
		}
	}
	return undo, nil
}

// Remove deletes t's copy from deviceID: the program, then the default
// control, then the origin. A program under t's id that is not t's copy (a
// fixture or device program) is left alone and removed is false. A failure
// stops the removal with a *PartError, leaving a prefix Ensure can complete.
func (c Copies) Remove(ctx context.Context, deviceID, fsaID string, t memory.ProgramTemplate) (removed bool, err error) {
	origin, err := c.Origins.Get(ctx, deviceID, t.ID)
	if errors.Is(err, store.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("reading the origin of template %s's copy on device %s: %w", t.ID, deviceID, err)
	}
	if origin.Kind != memory.ProgramOriginTemplate || origin.FSAID != fsaID || origin.TemplateID != t.ID {
		return false, nil
	}
	if err := c.deleteParts(ctx, deviceID, fsaID, t.ID, []string{PartOrigin, PartDefault, PartProgram}); err != nil {
		return false, err
	}
	return true, nil
}

// DevicePrograms returns the ids of the device programs recorded under fsaID
// on deviceID. A recorded origin whose program is gone is not listed: it has
// nothing to protect from an unassign.
func (c Copies) DevicePrograms(ctx context.Context, deviceID, fsaID string) ([]string, error) {
	list, err := c.Programs.List(ctx, deviceID, store.ListOptions{Unbounded: true})
	if err != nil {
		return nil, fmt.Errorf("listing programs of device %s: %w", deviceID, err)
	}
	var ids []string
	for _, p := range list.Items {
		_, _, id, ok := derhref.Program(p.Href)
		if !ok {
			continue
		}
		origin, err := c.Origins.Get(ctx, deviceID, id)
		if errors.Is(err, store.ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("reading origin %s/%s: %w", deviceID, id, err)
		}
		if origin.Kind == memory.ProgramOriginDevice && origin.FSAID == fsaID {
			ids = append(ids, id)
		}
	}
	slices.Sort(ids)
	return ids, nil
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
	return copies.removeDeviceProgramPartials(ctx, logf)
}

// removeDeviceProgramPartials deletes what a failed device-program create
// left behind: a device origin whose program is gone. Every removal deletes
// the origin last, so any partial undo leaves the origin to find it by. A
// device origin with its program is a whole program and stays.
func (c Copies) removeDeviceProgramPartials(ctx context.Context, logf func(format string, args ...any)) error {
	devices, err := c.Origins.Parents(ctx)
	if err != nil {
		return fmt.Errorf("listing devices with program origins: %w", err)
	}
	for _, deviceID := range devices {
		for _, programID := range c.Origins.IDs(deviceID) {
			origin, err := c.Origins.Get(ctx, deviceID, programID)
			if errors.Is(err, store.ErrNotFound) || (err == nil && origin.Kind != memory.ProgramOriginDevice) {
				continue
			}
			if err != nil {
				return fmt.Errorf("reading origin %s/%s: %w", deviceID, programID, err)
			}
			if _, err := c.Programs.Get(ctx, deviceID, programID); err == nil {
				continue
			} else if !errors.Is(err, store.ErrNotFound) {
				return fmt.Errorf("reading program %s/%s: %w", deviceID, programID, err)
			}
			if err := c.deleteParts(ctx, deviceID, origin.FSAID, programID, []string{PartOrigin, PartDefault, PartProgram}); err != nil {
				return fmt.Errorf("removing the partial device program %s on device %s: %w", programID, deviceID, err)
			}
			logf("removed the partial device program %q of device %q left by a failed create", programID, deviceID)
		}
	}
	return nil
}
