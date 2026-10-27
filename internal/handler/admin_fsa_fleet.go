package handler

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/fsaprogram"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/mrid"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

// #1154: one server-managed FSA per aggregator.
//
// CSIP 6.1.7 gives an aggregator's own EndDevice no FunctionSetAssignments, so
// a control reaches an aggregator through the devices it manages. The fleet
// FSA is the one FSA whose assigned devices are exactly those devices, so one
// control on its program reaches the whole fleet. The admin routes cannot
// delete the FSA or change its assignment; FleetFSA is the only writer of
// either.

// fleetProgramPrimacy is the primacy of a fleet program. The server has no
// configured default program primacy, so this is the lowest value of the
// range 2030.5 leaves to the implementer; the program edit route changes it
// like any other program's.
const fleetProgramPrimacy = userPrimacyLow

// fleetProgramDescription is the description of a fleet FSA's one program.
const fleetProgramDescription = "Fleet control"

// ErrFleetUnwired is returned by FleetFSA when a store it reads is not wired.
var ErrFleetUnwired = errors.New("fleet FSA: management pairs, device lookup or program copies unwired")

// FleetDeviceLookup resolves a managed device's LFDI to its EndDevice record.
type FleetDeviceLookup interface {
	GetByLFDI(ctx context.Context, lfdi string) (sep2.EndDevice, error)
}

// serverManagedRefused answers a delete or an assignment change on a
// server-managed FSA, and reports whether it did. It is the one place that
// decides an FSA is the server's.
func (h *AdminFSAHandler) serverManagedRefused(w http.ResponseWriter, fsaID string) bool {
	if h.AdminFSAs.FleetAggregator(fsaID) == "" {
		return false
	}
	refuseProgram(w, "fsa_server_managed", "this fsa is the server-managed fleet of an aggregator; it cannot be deleted and its device assignment follows the aggregator's managed devices")
	return true
}

// FleetFSA returns the id of aggregatorLFDI's fleet FSA, creating it with one
// program template on first use, and reconciles its assignment to the devices
// the aggregator manages that have an EndDevice record. The aggregator's own
// EndDevice is never assigned. r supplies the context and the log fields of
// the call; the reconcile holds the program lock the admin assign route takes.
func (h *AdminFSAHandler) FleetFSA(r *http.Request, aggregatorLFDI string) (string, error) {
	if h.Managers == nil || h.FleetDevices == nil || h.Copies == nil {
		return "", ErrFleetUnwired
	}
	if aggregatorLFDI == "" {
		return "", errors.New("fleet FSA: aggregator LFDI required")
	}
	h.programMu.Lock()
	defer h.programMu.Unlock()

	fsaID, err := h.ensureFleetFSA(r, aggregatorLFDI)
	if err != nil {
		return "", err
	}
	if err := h.reconcileFleet(r, fsaID, aggregatorLFDI); err != nil {
		return "", err
	}
	return fsaID, nil
}

// ensureFleetFSA returns the aggregator's fleet FSA id after creating the FSA,
// or its program template, when either is missing. The caller holds programMu.
func (h *AdminFSAHandler) ensureFleetFSA(r *http.Request, aggregatorLFDI string) (string, error) {
	ctx := r.Context()
	fsaID, ok := h.AdminFSAs.FleetFSA(aggregatorLFDI)
	if !ok {
		id, err := mrid.New(rand.Read, h.PEN)
		if err != nil {
			return "", fmt.Errorf("fleet FSA: mint: %w", err)
		}
		desc := "fleet " + aggregatorLFDI
		if len(desc) > maxDescriptionOctets {
			desc = desc[:maxDescriptionOctets]
		}
		if err := h.AdminFSAs.CreateFleet(ctx, id, newAdminFSA(id, desc), aggregatorLFDI); err != nil {
			return "", fmt.Errorf("fleet FSA: create: %w", err)
		}
		fsaID = id
	}
	if len(h.AdminFSAs.Templates(ctx, fsaID)) > 0 {
		return fsaID, nil
	}

	base := sep2.DERControlBase{}
	tmpl := memory.ProgramTemplate{Description: fleetProgramDescription, Primacy: fleetProgramPrimacy, DefaultControlBase: &base}
	if err := h.mintTemplateIDs(&tmpl); err != nil {
		return "", fmt.Errorf("fleet FSA: mint template: %w", err)
	}
	if err := h.AdminFSAs.CreateTemplate(ctx, fsaID, tmpl); err != nil {
		return "", fmt.Errorf("fleet FSA: create template: %w", err)
	}
	// A template added to an FSA that already has devices is copied to each.
	steps := []rollbackStep{{template: tmpl.ID, part: "template", undo: func(ctx context.Context) error {
		return h.AdminFSAs.DeleteTemplate(ctx, fsaID, tmpl.ID)
	}}}
	devices := h.AdminFSAs.Devices(ctx, fsaID)
	for _, dev := range devices {
		undo, err := h.Copies.Ensure(ctx, dev, fsaID, tmpl)
		if err != nil {
			if !h.rolledBack(r, err, steps, &incompleteLog{"admin: fleet program template left partly applied", "fleet_template_incomplete",
				[]any{"fsa", fsaID, "template", tmpl.ID, "error", err}}) {
				return "", fmt.Errorf("fleet FSA: copy template to %s, undo incomplete: %w", dev, err)
			}
			return "", fmt.Errorf("fleet FSA: copy template to %s: %w", dev, err)
		}
		steps = append(steps, rollbackStep{device: dev, template: tmpl.ID, part: "copy", undo: undo})
	}
	for _, dev := range devices {
		h.notify(ctx, programListHref(dev, fsaID))
	}
	return fsaID, nil
}

// reconcileFleet makes fsaID's assigned devices equal the EndDevice records of
// the aggregator's managed devices, excluding the aggregator's own. A managed
// LFDI with no record is skipped, since it has nothing to assign. The caller
// holds programMu. It stops at the first failed write and names it; the next
// call converges from whatever was written.
func (h *AdminFSAHandler) reconcileFleet(r *http.Request, fsaID, aggregatorLFDI string) error {
	ctx := r.Context()
	managed, err := h.Managers.ManagedBy(ctx, aggregatorLFDI)
	if err != nil {
		return fmt.Errorf("fleet FSA: ManagedBy(%q): %w", aggregatorLFDI, err)
	}
	ownID, err := h.fleetDeviceID(ctx, aggregatorLFDI)
	if err != nil {
		return err
	}
	var want []string
	for _, lfdi := range managed {
		if lfdi == aggregatorLFDI {
			continue
		}
		id, err := h.fleetDeviceID(ctx, lfdi)
		if err != nil {
			return err
		}
		if id != "" && id != ownID && !slices.Contains(want, id) {
			want = append(want, id)
		}
	}
	slices.Sort(want)

	for _, dev := range h.AdminFSAs.Devices(ctx, fsaID) {
		if slices.Contains(want, dev) {
			continue
		}
		if fault := h.unassignLocked(r, dev, fsaID); fault != nil {
			return fmt.Errorf("fleet FSA: unassign %s: %w", dev, fault.err)
		}
	}
	fsa, err := h.AdminFSAs.Get(ctx, fsaID)
	if err != nil {
		return fmt.Errorf("fleet FSA: read %s: %w", fsaID, err)
	}
	have := h.AdminFSAs.Devices(ctx, fsaID)
	for _, dev := range want {
		if slices.Contains(have, dev) {
			continue
		}
		if fault := h.assignLocked(r, dev, fsaID, fsa, fsaprogram.CompleteIfAssigned); fault != nil {
			return fmt.Errorf("fleet FSA: assign %s: %w", dev, fault.err)
		}
	}
	return nil
}

// fleetDeviceID returns the store id of the EndDevice with lfdi, or "" when
// there is no such record. Any other lookup failure is returned, so a store
// that cannot answer never reads as an empty fleet.
func (h *AdminFSAHandler) fleetDeviceID(ctx context.Context, lfdi string) (string, error) {
	dev, err := h.FleetDevices.GetByLFDI(ctx, lfdi)
	if errors.Is(err, store.ErrNotFound) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("fleet FSA: GetByLFDI(%q): %w", lfdi, err)
	}
	id, ok := strings.CutPrefix(dev.Href, store.DeviceHref(""))
	if !ok || id == "" {
		return "", fmt.Errorf("fleet FSA: device %q has href %q, not /edev/{id}", lfdi, dev.Href)
	}
	return id, nil
}
