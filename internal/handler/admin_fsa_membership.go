package handler

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strings"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/commitment"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/dercontrol"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/fsaprogram"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/logsafe"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/handlers/der"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/handlers/sep2time"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

// membershipFault is a refused or failed device assignment or unassignment:
// err is the cause and answer writes the response the admin route gives. A
// caller that is not a route reads err and ignores answer.
type membershipFault struct {
	err    error
	answer func(w http.ResponseWriter)
}

func statusFault(err error, status int, message string) *membershipFault {
	return &membershipFault{err: err, answer: func(w http.ResponseWriter) { writeError(w, status, message) }}
}

func jsonFault(err error, status int, body any) *membershipFault {
	return &membershipFault{err: err, answer: func(w http.ResponseWriter) { writeJSON(w, status, body) }}
}

// failedFault logs a program write failure and answers the logged 500.
func (h *AdminFSAHandler) failedFault(r *http.Request, cause string, err error) *membershipFault {
	h.logTemplateFailed(r, cause, err)
	return statusFault(err, http.StatusInternalServerError, templateInternalError)
}

// incompleteFault answers an assignment whose rollback stopped partway.
func (h *AdminFSAHandler) incompleteFault(err error, deviceID, fsaID string) *membershipFault {
	return &membershipFault{err: err, answer: func(w http.ResponseWriter) { h.assignIncomplete(w, deviceID, fsaID) }}
}

// copyRefusedFault is the one answer to a copy that conflicts with a device's
// own program (409) or fails (500).
func (h *AdminFSAHandler) copyRefusedFault(r *http.Request, err error) *membershipFault {
	if errors.Is(err, fsaprogram.ErrCopyConflict) {
		return statusFault(err, http.StatusConflict, "a device assigned to this fsa already holds a program with this id or mRID")
	}
	return h.failedFault(r, "copy_write", err)
}

// assignLocked assigns deviceID to fsaID, copies every template of the FSA to
// the device and replicates the FSA's live controls to it. The caller holds
// programMu and has confirmed the device and the FSA exist. A nil result means
// the device is assigned and its lists are notified; a fault means every write
// was undone, or the answer says which stayed.
func (h *AdminFSAHandler) assignLocked(r *http.Request, deviceID, fsaID string, fsa sep2.FunctionSetAssignments, onExisting fsaprogram.OnExisting) *membershipFault {
	ctx := r.Context()
	// Every template copy is checked before anything is written, so a
	// conflict refuses the assignment with nothing to undo.
	tmpls := h.AdminFSAs.Templates(ctx, fsaID)
	if len(tmpls) > 0 && h.Copies == nil {
		return h.failedFault(r, "no_program_stores", errStoresUnwired)
	}
	for _, t := range tmpls {
		if _, err := h.Copies.Check(ctx, deviceID, fsaID, t); err != nil {
			if errors.Is(err, fsaprogram.ErrCopyConflict) {
				return statusFault(err, http.StatusConflict, "device already holds a program with the id or mRID of one of this fsa's templates")
			}
			return h.failedFault(r, "copy_check", err)
		}
	}

	// The assignment is undone last, so a write whose undo fails still
	// has its assignment, and the boot rebuild completes it. The scoped
	// href differs from the admin href: the spec surface lives under
	// the device's edev tree.
	assigned, err := fsaprogram.Assign(ctx, h.AdminFSAs, h.DeviceFSAs, deviceID, fsaID, fsa, onExisting)
	steps := rollbackSteps(deviceID, assigned)
	if err != nil {
		var ae *fsaprogram.AssignError
		if errors.As(err, &ae) && ae.Stage == fsaprogram.StageAssign {
			switch {
			case errors.Is(err, memory.ErrAdminFSADeviceOnly):
				return &membershipFault{err: err, answer: func(w http.ResponseWriter) { h.deviceOnlyRefused(w, fsaID) }}
			case errors.Is(err, store.ErrAlreadyExists):
				return statusFault(err, http.StatusConflict, "device already assigned to this FSA")
			default:
				return statusFault(err, http.StatusInternalServerError, "assign: "+ae.Err.Error())
			}
		}
		if !h.rolledBack(r, err, steps, nil) {
			return h.incompleteFault(err, deviceID, fsaID)
		}
		return statusFault(err, http.StatusInternalServerError, "scope materialize: "+errors.Unwrap(err).Error())
	}
	for _, t := range tmpls {
		undo, err := h.Copies.Ensure(ctx, deviceID, fsaID, t)
		if err == nil {
			steps = append(steps, rollbackStep{device: deviceID, template: t.ID, part: "copy", undo: undo})
			continue
		}
		if !h.rolledBack(r, err, steps, nil) {
			return h.incompleteFault(err, deviceID, fsaID)
		}
		return h.copyRefusedFault(r, err)
	}
	// Last, so nothing after it can need the replicas undone.
	if fault := h.replicateControls(r, deviceID, fsaID, steps); fault != nil {
		return fault
	}

	h.notify(ctx, "/edev/"+deviceID+"/fsa")
	h.notify(ctx, programListHref(deviceID, fsaID))
	return nil
}

// replicateControls writes the FSA's live controls to the device's new copies
// and returns nil when the assignment may complete. On failure it undoes steps
// and returns: 409 for a commitment conflict in the device's fleet, 500
// otherwise, and the incomplete answer when a replica may be stored.
func (h *AdminFSAHandler) replicateControls(r *http.Request, deviceID, fsaID string, steps []rollbackStep) *membershipFault {
	if len(liveReplicas(r.Context(), h.AdminFSAs, deviceID, fsaID, sep2time.Now().Unix())) == 0 {
		return nil
	}
	err := errors.New("fsa controls cannot be replicated: no replicator wired")
	if h.Replicas != nil {
		err = h.Replicas.ReplicateTo(r.Context(), deviceID, fsaID)
	}
	if err == nil {
		return nil
	}
	var undo *dercontrol.UndoError
	if errors.As(err, &undo) {
		// The copies and the assignment stay, so the replica is not orphaned.
		h.logged(r).ErrorContext(r.Context(), "admin: FSA control replica may be left after a failed assign", "event", "assign_replica_incomplete",
			"device", logsafe.Str(deviceID), "fsa", logsafe.Str(fsaID), "scope", logsafe.Str(undo.Scope.Key()), "mrid", logsafe.Str(undo.MRID),
			"undo_step", logsafe.Str(string(undo.Step)), "control_kept", undo.ControlKept, "lifecycle_kept", undo.LifecycleKept, "error", logsafe.Err(err))
		return h.incompleteFault(err, deviceID, fsaID)
	}
	if !h.rolledBack(r, err, steps, nil) {
		return h.incompleteFault(err, deviceID, fsaID)
	}
	var conflict *commitment.ConflictError
	if errors.As(err, &conflict) {
		message, ok := conflictMessages[conflict.Code]
		if !ok {
			message = "a control of this fsa conflicts with a commitment of the device's fleet"
		}
		h.audit(r, slog.LevelWarn, "admin: device assignment refused", "der_fsa_assign_refused",
			"code", string(conflict.Code), "conflict_mrid", conflict.MRID, "device", deviceID, "fsa", fsaID)
		return jsonFault(err, http.StatusConflict, DERControlConflict{Error: message, Code: string(conflict.Code), MRID: conflict.MRID})
	}
	return h.failedFault(r, "replicate_controls", err)
}

// unassignLocked removes deviceID's template copies and its assignment to
// fsaID. The caller holds programMu. A nil result means the device is
// unassigned and its lists are notified.
func (h *AdminFSAHandler) unassignLocked(r *http.Request, deviceID, fsaID string) *membershipFault {
	ctx := r.Context()
	if !slices.Contains(h.AdminFSAs.Devices(ctx, fsaID), deviceID) {
		return statusFault(store.ErrNotFound, http.StatusNotFound, "assignment not found")
	}

	// A device program is the operator's own, so unlike a template copy
	// it is never removed on the operator's behalf (#921).
	if h.Copies != nil {
		held, err := h.Copies.DevicePrograms(ctx, deviceID, fsaID)
		if err != nil {
			return h.failedFault(r, "device_programs", err)
		}
		if len(held) > 0 {
			return jsonFault(errors.New("device programs present"), http.StatusConflict, deviceProgramsPresent{
				Error:    "device programs sit under this fsa and must be deleted before the device is unassigned: " + strings.Join(held, ", "),
				Code:     "device_programs_present",
				Programs: held,
			})
		}
	}

	// Copies go before the assignment, so a removal that fails leaves
	// the device assigned and the boot rebuild restores its copies.
	tmpls := h.AdminFSAs.Templates(ctx, fsaID)
	if len(tmpls) > 0 && (h.Copies == nil || h.Retirer == nil) {
		return h.failedFault(r, "no_program_stores", errStoresUnwired)
	}
	var removed []string
	notifyRemoved := func() {
		for _, href := range removed {
			h.notifyStatus(ctx, href, sep2.NotificationStatusCanceledDeleted)
		}
		if len(removed) > 0 {
			h.notify(ctx, programListHref(deviceID, fsaID))
		}
	}
	// Removing a copy takes its controls off the device without
	// cancelling them, so a copy holding a live one refuses the unassign
	// (#972); Retire also keeps a control from landing meanwhile.
	scopes := make([]dercontrol.Scope, 0, len(tmpls))
	for _, t := range tmpls {
		scopes = append(scopes, dercontrol.Scope{EndDeviceID: deviceID, FSAID: fsaID, DERProgramID: t.ID})
	}
	copyFailed := false
	removeAll := func(ctx context.Context) error {
		for _, t := range tmpls {
			ok, err := h.Copies.Remove(ctx, deviceID, fsaID, t)
			if err != nil {
				var part *fsaprogram.PartError
				if errors.As(err, &part) {
					h.logUndoFailed(r, part.DeviceID, part.TemplateID, part.Part, err)
				} else {
					h.logUndoFailed(r, deviceID, t.ID, "copy", err)
				}
				copyFailed = true
				return err
			}
			if ok {
				removed = append(removed, der.DERProgramHref(deviceID, fsaID, t.ID))
			}
		}
		return h.AdminFSAs.UnassignDevice(ctx, fsaID, deviceID)
	}
	var err error
	if len(tmpls) == 0 {
		err = removeAll(ctx)
	} else {
		err = h.Retirer.Retire(ctx, scopes, removeAll)
	}
	notifyRemoved()
	var live *dercontrol.LiveControlsError
	if errors.As(err, &live) {
		h.audit(r, slog.LevelWarn, "admin: device unassign refused", "der_fsa_unassign_refused",
			"code", "live_controls", "device", deviceID, "fsa", fsaID, "controls", live.Controls)
		return jsonFault(err, http.StatusConflict, programRefusal{
			Error:    "cancel these controls, or wait for them to end, before unassigning the device",
			Code:     "live_controls",
			Controls: slices.Clone(live.Controls),
		})
	}
	if copyFailed {
		return statusFault(err, http.StatusInternalServerError, fmt.Sprintf(
			"unassign failed; device %s stays assigned to fsa %s, and its template copies are restored at the next restart", deviceID, fsaID))
	}
	switch {
	case err == nil:
		if h.DeviceFSAs != nil {
			if delErr := h.DeviceFSAs.Delete(ctx, deviceID, fsaID); delErr != nil && !errors.Is(delErr, store.ErrNotFound) {
				// The assignment is gone; the in-memory FSA goes at the next restart.
				h.logged(r).ErrorContext(ctx, "admin: device FSA left after unassign", "event", "device_fsa_delete_failed",
					"device", logsafe.Str(deviceID), "fsa", logsafe.Str(fsaID), "error", logsafe.Err(delErr))
			}
		}
		h.notify(ctx, "/edev/"+deviceID+"/fsa")
		return nil
	case errors.Is(err, store.ErrNotFound):
		return statusFault(err, http.StatusNotFound, "assignment not found")
	default:
		return statusFault(err, http.StatusInternalServerError, "unassign: "+err.Error())
	}
}
