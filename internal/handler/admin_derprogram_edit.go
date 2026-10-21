package handler

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"slices"
	"strings"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/dercontrol"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/derhref"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/fsaprogram"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/handlers/der"
	coresingleton "github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/handlers/singleton"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

// #922: edit and delete templates, device programs and fixture programs.
// An edit raises version; a delete is refused while any of the program's
// control scopes holds a live control, and keeps every control record.

// ProgramRetirer runs remove only while none of scopes holds a live control,
// and keeps the scopes from gaining one until remove returns. The production
// implementation is *dercontrol.Issuer.
type ProgramRetirer interface {
	Retire(ctx context.Context, scopes []dercontrol.Scope, remove func(context.Context) error) error
}

// programRefusal is a 409 body carrying a code a client branches on.
// Controls lists the live controls of a refused delete.
type programRefusal struct {
	Error    string   `json:"error"`
	Code     string   `json:"code"`
	Controls []string `json:"controls,omitempty"`
}

func refuseProgram(w http.ResponseWriter, code, msg string) {
	writeJSON(w, http.StatusConflict, programRefusal{Error: msg, Code: code})
}

// checkMRID refuses an edit body naming a different mRID: IEEE 2030.5 keeps
// an object's mRID across modifications.
func checkMRID(w http.ResponseWriter, e programEdit, current string) bool {
	if e.mRID != "" && !strings.EqualFold(e.mRID, current) {
		refuseProgram(w, "mrid_immutable", "mRID cannot change; it is "+current)
		return false
	}
	return true
}

// decodeProgramEdit reads a PUT body, or answers the request and returns
// false.
func decodeProgramEdit(w http.ResponseWriter, r *http.Request) (programEdit, bool) {
	var body programTemplateBody
	if ref, ok := decodeDERControlBody(w, r, &body, false); !ok {
		writeError(w, ref.status, ref.message)
		return programEdit{}, false
	}
	e, msg, ok := buildProgramEdit(body)
	if !ok {
		writeError(w, http.StatusBadRequest, msg)
	}
	return e, ok
}

// HandleUpdateProgramTemplate returns a handler for PUT
// /api/fsas/{id}/der-programs/{derpId}. Every device copy takes the
// template's new content and versions.
func (h *AdminFSAHandler) HandleUpdateProgramTemplate() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		fsaID, id := r.PathValue("id"), r.PathValue("derpId")
		e, ok := decodeProgramEdit(w, r)
		if !ok {
			return
		}
		if !h.storesWired(w, r, h.Copies != nil) {
			return
		}

		h.programMu.Lock()
		defer h.programMu.Unlock()

		old, ok := h.lookupTemplate(w, r, fsaID, id)
		if !ok || !checkMRID(w, e, old.MRID) {
			return
		}
		next := old.Copy()
		next.Description, next.Primacy = e.description, e.primacy
		if e.defaultBase != nil {
			next.DefaultControlBase = e.defaultBase.Copy()
		}
		edited := !reflect.DeepEqual(old, next)
		if edited && !reflect.DeepEqual(old.DefaultControlBase, next.DefaultControlBase) {
			v, err := fsaprogram.NextVersion(&old.DefaultVersion)
			if err != nil {
				refuseProgram(w, "version_exhausted", "the default control's version is at 65535")
				return
			}
			next.DefaultVersion = *v
		}
		if edited {
			v, err := fsaprogram.NextVersion(&old.Version)
			if err != nil {
				refuseProgram(w, "version_exhausted", "the program's version is at 65535")
				return
			}
			next.Version = *v
		}

		devices, ok := h.copyHolders(w, r, fsaID, old)
		if !ok {
			return
		}
		// The template is written first and is never rolled back: it is the
		// revision every copy converges to, here, on a retry, or in the boot
		// rebuild, and its versions only rise, so none is reused.
		if edited {
			if err := h.AdminFSAs.UpdateTemplate(r.Context(), fsaID, next); err != nil {
				h.templateFailed(w, r, "template_write", err)
				return
			}
		}
		var behind []string
		for _, dev := range devices {
			ch, err := h.Copies.Sync(r.Context(), dev, fsaID, next)
			h.notifyEdited(r.Context(), dev, fsaID, id, ch)
			if err != nil {
				h.logged(r).ErrorContext(r.Context(), "admin: DER program template copy left at an older revision", "event", "der_program_template_copy_behind",
					"fsa", fsaID, "template", id, "device", dev, "error", err)
				behind = append(behind, dev)
			}
		}
		if len(behind) > 0 {
			writeError(w, http.StatusInternalServerError, fmt.Sprintf(
				"program template %s on fsa %s is saved, but the copies on devices %s are not yet; repeat the request, or restart, to update them",
				id, fsaID, strings.Join(behind, ", ")))
			return
		}
		if edited {
			h.logged(r).InfoContext(r.Context(), "admin: DER program template edited", "event", "der_program_template_edited",
				"fsa", fsaID, "template", id, "version", next.Version, "copies", len(devices))
		}
		writeJSON(w, http.StatusOK, h.templateView(r.Context(), fsaID, next))
	}
}

// notifyEdited tells the subscribers of each representation an edit wrote,
// and the program's FSA list, since lists are per FSA.
func (h *AdminFSAHandler) notifyEdited(ctx context.Context, deviceID, fsaID, id string, ch fsaprogram.Changes) {
	href := der.DERProgramHref(deviceID, fsaID, id)
	if ch.Program {
		h.notify(ctx, href)
	}
	if ch.Default {
		h.notify(ctx, href+"/dderc")
	}
	if ch.Program || ch.Default {
		h.notify(ctx, programListHref(deviceID, fsaID))
	}
}

// HandleDeleteProgramTemplate returns a handler for DELETE
// /api/fsas/{id}/der-programs/{derpId}.
func (h *AdminFSAHandler) HandleDeleteProgramTemplate() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		fsaID, id := r.PathValue("id"), r.PathValue("derpId")
		if !h.storesWired(w, r, h.Copies != nil && h.Retirer != nil) {
			return
		}

		h.programMu.Lock()
		defer h.programMu.Unlock()

		t, ok := h.lookupTemplate(w, r, fsaID, id)
		if !ok {
			return
		}
		devices, ok := h.copyHolders(w, r, fsaID, t)
		if !ok {
			return
		}
		scopes := make([]dercontrol.Scope, 0, len(devices))
		for _, dev := range devices {
			scopes = append(scopes, dercontrol.Scope{EndDeviceID: dev, FSAID: fsaID, DERProgramID: id})
		}

		// Copies go before the template, so a removal that fails leaves the
		// template, and the boot rebuild writes back every copy it names.
		var removed []string
		failed := "template"
		err := h.Retirer.Retire(r.Context(), scopes, func(ctx context.Context) error {
			for _, dev := range devices {
				_, err := h.Copies.Remove(ctx, dev, fsaID, t)
				var part *fsaprogram.PartError
				// Remove deletes the program first, so a later part failing
				// still leaves the device without it.
				if err == nil || (errors.As(err, &part) && part.Part != fsaprogram.PartProgram) {
					removed = append(removed, dev)
				}
				if err != nil {
					failed = "copy on device " + dev
					return err
				}
			}
			return h.AdminFSAs.DeleteTemplate(ctx, fsaID, id)
		})
		for _, dev := range removed {
			h.notifyDeleted(r.Context(), dev, fsaID, id)
		}
		if h.retireRefused(w, err) {
			return
		}
		if err != nil {
			h.logged(r).ErrorContext(r.Context(), "admin: DER program template delete failed", "event", "der_program_template_delete_failed",
				"fsa", fsaID, "template", id, "failed", failed, "removed", removed, "error", err)
			msg := fmt.Sprintf("program template delete failed at the %s; template %s is kept on fsa %s", failed, id, fsaID)
			if len(removed) > 0 {
				msg += fmt.Sprintf(", and the boot rebuild writes back its copies removed from devices %s", strings.Join(removed, ", "))
			}
			writeError(w, http.StatusInternalServerError, msg)
			return
		}
		h.logged(r).InfoContext(r.Context(), "admin: DER program template deleted", "event", "der_program_template_deleted",
			"fsa", fsaID, "template", id, "copies", len(removed))
		w.WriteHeader(http.StatusNoContent)
	}
}

// lookupTemplate reads a template, or answers 404 or 500 and returns false.
func (h *AdminFSAHandler) lookupTemplate(w http.ResponseWriter, r *http.Request, fsaID, id string) (memory.ProgramTemplate, bool) {
	t, err := h.AdminFSAs.Template(r.Context(), fsaID, id)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "program template not found")
		return t, false
	}
	if err != nil {
		h.templateFailed(w, r, "template_lookup", err)
		return t, false
	}
	return t, true
}

// copyHolders returns the assigned devices that hold t's copy. A device whose
// copy is missing gets the edited template from the boot rebuild. A device
// whose own program conflicts with t holds no copy, as the boot rebuild
// treats it, so it neither blocks nor is touched by an edit or delete.
func (h *AdminFSAHandler) copyHolders(w http.ResponseWriter, r *http.Request, fsaID string, t memory.ProgramTemplate) ([]string, bool) {
	var out []string
	for _, dev := range h.AdminFSAs.Devices(r.Context(), fsaID) {
		present, err := h.Copies.Check(r.Context(), dev, fsaID, t)
		if errors.Is(err, fsaprogram.ErrCopyConflict) {
			continue
		}
		if err != nil {
			h.templateFailed(w, r, "copy_check", err)
			return nil, false
		}
		if present {
			out = append(out, dev)
		}
	}
	return out, true
}

// HandleUpdateDeviceProgram returns a handler for PUT
// /api/devices/{id}/der-programs/{derpId}: a device program or a fixture
// program. A template copy is refused; its template is what changes.
func (h *AdminFSAHandler) HandleUpdateDeviceProgram() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		deviceID, id := r.PathValue("id"), r.PathValue("derpId")
		e, ok := decodeProgramEdit(w, r)
		if !ok {
			return
		}
		if !h.storesWired(w, r, h.Copies != nil) {
			return
		}

		h.programMu.Lock()
		defer h.programMu.Unlock()

		prog, fsaID, ok := h.lookupDeviceProgram(w, r, deviceID, id, "edit")
		if !ok || !checkMRID(w, e, prog.MRID) {
			return
		}
		scope := derhref.ScopeKey(deviceID, fsaID, id)
		var mutateDefault func(*sep2.DefaultDERControl) error
		if e.defaultBase != nil {
			if _, err := h.Copies.Defaults.Get(r.Context(), scope, coresingleton.SingletonKey); errors.Is(err, store.ErrNotFound) {
				refuseProgram(w, "no_default_control", "this program has no default control to edit")
				return
			} else if err != nil {
				h.templateFailed(w, r, "default_lookup", err)
				return
			}
			mutateDefault = func(d *sep2.DefaultDERControl) error {
				base := e.defaultBase.Copy()
				d.DERControlBase = &base
				return nil
			}
		}
		ch, err := fsaprogram.EditWithDefault(r.Context(), h.Copies.Programs, h.Copies.Defaults,
			fsaprogram.Ref{DeviceID: deviceID, ProgramID: id}, scope,
			func(p *sep2.DERProgram) error {
				p.Description, p.Primacy = e.description, e.primacy
				return nil
			}, mutateDefault)
		switch {
		case err == nil:
		case errors.Is(err, fsaprogram.ErrVersionExhausted):
			refuseProgram(w, "version_exhausted", "the program's or its default control's version is at 65535")
			return
		default:
			h.templateFailed(w, r, "program_edit", err)
			return
		}

		h.notifyEdited(r.Context(), deviceID, fsaID, id, ch)
		if ch.Program {
			h.logged(r).InfoContext(r.Context(), "admin: DER device program edited", "event", "der_device_program_edited",
				"device", deviceID, "program", id)
		}
		view, err := h.deviceProgramView(r.Context(), deviceID, fsaID, id)
		if err != nil {
			h.templateFailed(w, r, "program_read", err)
			return
		}
		writeJSON(w, http.StatusOK, view)
	}
}

// HandleDeleteDeviceProgram returns a handler for DELETE
// /api/devices/{id}/der-programs/{derpId}. A fixture program deleted with a
// data dir stays deleted at restart: the seed record skips it.
func (h *AdminFSAHandler) HandleDeleteDeviceProgram() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		deviceID, id := r.PathValue("id"), r.PathValue("derpId")
		if !h.storesWired(w, r, h.Copies != nil && h.Retirer != nil) {
			return
		}

		h.programMu.Lock()
		defer h.programMu.Unlock()

		prog, fsaID, ok := h.lookupDeviceProgram(w, r, deviceID, id, "delete")
		if !ok {
			return
		}
		scopes := []dercontrol.Scope{{EndDeviceID: deviceID, FSAID: fsaID, DERProgramID: id}}
		// The issuer files controls under the FSA the control list link
		// names, which a fixture program may set apart from its href.
		if prog.DERControlListLink != nil {
			if _, linkFSA, _, ok := derhref.ControlList(prog.DERControlListLink.Href); ok && linkFSA != fsaID {
				scopes = append(scopes, dercontrol.Scope{EndDeviceID: deviceID, FSAID: linkFSA, DERProgramID: id})
			}
		}

		programGone := false
		err := h.Retirer.Retire(r.Context(), scopes, func(ctx context.Context) error {
			err := h.Copies.RemoveProgram(ctx, deviceID, fsaID, id)
			var part *fsaprogram.PartError
			// RemoveProgram deletes the default, the program, then the origin.
			programGone = err == nil || (errors.As(err, &part) && part.Part == fsaprogram.PartOrigin)
			return err
		})
		if programGone {
			h.notifyDeleted(r.Context(), deviceID, fsaID, id)
		}
		if h.retireRefused(w, err) {
			return
		}
		if err != nil {
			h.templateFailed(w, r, "program_delete", err)
			return
		}
		h.logged(r).InfoContext(r.Context(), "admin: DER device program deleted", "event", "der_device_program_deleted",
			"device", deviceID, "program", id)
		w.WriteHeader(http.StatusNoContent)
	}
}

// lookupDeviceProgram reads a device's program and the FSA its href names,
// or answers and returns false. A template copy is refused with
// edit_the_template.
func (h *AdminFSAHandler) lookupDeviceProgram(w http.ResponseWriter, r *http.Request, deviceID, id, verb string) (sep2.DERProgram, string, bool) {
	if h.EndDevices != nil {
		if _, err := h.EndDevices.Get(r.Context(), deviceID); err != nil {
			if errors.Is(err, store.ErrNotFound) {
				writeError(w, http.StatusNotFound, "device not found")
				return sep2.DERProgram{}, "", false
			}
			h.templateFailed(w, r, "device_lookup", err)
			return sep2.DERProgram{}, "", false
		}
	}
	prog, err := h.Copies.Programs.Get(r.Context(), deviceID, id)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "program not found on this device")
		return prog, "", false
	}
	if err != nil {
		h.templateFailed(w, r, "program_lookup", err)
		return prog, "", false
	}
	edev, fsaID, derp, ok := derhref.Program(prog.Href)
	if !ok || edev != deviceID || derp != id {
		refuseProgram(w, "unaddressable_program", "this program's href names no FSA of this device")
		return prog, "", false
	}
	origin, err := h.Copies.Origins.Get(r.Context(), deviceID, id)
	switch {
	case err == nil && origin.Kind == memory.ProgramOriginTemplate:
		refuseProgram(w, "edit_the_template", fmt.Sprintf(
			"this program is a copy of template %s on fsa %s; %s the template, or unassign the device", origin.TemplateID, origin.FSAID, verb))
		return prog, "", false
	case err == nil, errors.Is(err, store.ErrNotFound):
	default:
		h.templateFailed(w, r, "origin_lookup", err)
		return prog, "", false
	}
	return prog, fsaID, true
}

// retireRefused answers a delete refused for live controls and reports
// whether it did.
func (h *AdminFSAHandler) retireRefused(w http.ResponseWriter, err error) bool {
	var live *dercontrol.LiveControlsError
	if !errors.As(err, &live) {
		return false
	}
	writeJSON(w, http.StatusConflict, programRefusal{
		Error:    "cancel these controls, or wait for them to end, before deleting the program",
		Code:     "live_controls",
		Controls: slices.Clone(live.Controls),
	})
	return true
}

// notifyDeleted tells the subscribers of the program, its default control
// and its control list that they are gone, and its FSA's list subscribers
// that the list changed.
func (h *AdminFSAHandler) notifyDeleted(ctx context.Context, deviceID, fsaID, id string) {
	href := der.DERProgramHref(deviceID, fsaID, id)
	for _, gone := range []string{href, href + "/dderc", href + "/derc"} {
		h.notifyStatus(ctx, gone, sep2.NotificationStatusCanceledDeleted)
	}
	h.notify(ctx, programListHref(deviceID, fsaID))
}

func (h *AdminFSAHandler) deviceProgramView(ctx context.Context, deviceID, fsaID, id string) (DeviceProgramView, error) {
	p, err := h.Copies.Programs.Get(ctx, deviceID, id)
	if err != nil {
		return DeviceProgramView{}, err
	}
	v := DeviceProgramView{ID: id, Href: p.Href, MRID: p.MRID, Description: p.Description, Primacy: p.Primacy}
	if p.Version != nil {
		v.Version = *p.Version
	}
	d, err := h.Copies.Defaults.Get(ctx, derhref.ScopeKey(deviceID, fsaID, id), coresingleton.SingletonKey)
	switch {
	case err == nil:
		var base sep2.DERControlBase
		if d.DERControlBase != nil {
			base = *d.DERControlBase
		}
		typ, baseView, _ := controlBaseViews(&base)
		v.DefaultControl = &ProgramDefaultControlView{MRID: d.MRID, Type: typ, DERControlBase: baseView}
	case !errors.Is(err, store.ErrNotFound):
		return DeviceProgramView{}, err
	}
	return v, nil
}
