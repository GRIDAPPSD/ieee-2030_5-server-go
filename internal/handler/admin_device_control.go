package handler

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"net/http"
	"slices"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/dercontrol"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/fsaprogram"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/mrid"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/handlers/der"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/handlers/sep2time"
	coresingleton "github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/handlers/singleton"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

// #970: POST /api/devices/{id}/controls sends one control, or sets one
// default, to a single device. IEEE 2030.5 reaches one device only through an
// FSA assigned to it alone, so the route creates or reuses a device-only FSA
// and one device program in it, then issues through the same paths as every
// other program.

// noOtherProgram stands in for the lowest primacy of a device that has no
// other program, one above any uint8.
const noOtherProgram = 256

// deviceControlRefusedEvent is the audit event of a device-route refusal that
// answers with its own body instead of through refuse.
const deviceControlRefusedEvent = "der_device_control_refused"

// deviceProgramDescription is the description of the one program in a
// device-only FSA.
const deviceProgramDescription = "device-only"

// AdminDeviceControlHandler serves POST /api/devices/{id}/controls. FSAs
// owns the FSA and program stores and the lock every program write takes;
// Controls issues the control. Both are required.
type AdminDeviceControlHandler struct {
	FSAs     *AdminFSAHandler
	Controls *AdminDERControlHandler
}

// deviceControlBody is the request body: a control body without
// derProgramHref (the server chooses the program) and with kind.
type deviceControlBody struct {
	controlValuesBody
	Kind            *string `json:"kind"`
	ExecutesGrant   *string `json:"executesGrant"`
	StartTime       *int64  `json:"startTime"`
	DurationSeconds *int64  `json:"durationSeconds"`
	Description     *string `json:"description"`
}

// DeviceControlCreated is the 201 body. Exactly one of Control (kind
// scheduled) and DefaultControl (kind default) is set.
type DeviceControlCreated struct {
	Kind          string `json:"kind"`
	FSA           string `json:"fsa"`
	Program       string `json:"program"`
	ProgramHref   string `json:"programHref"`
	Primacy       uint8  `json:"primacy"`
	PrimacyReason string `json:"primacyReason"`

	Control        *DERControlCreated         `json:"control,omitempty"`
	DefaultControl *ProgramDefaultControlView `json:"defaultControl,omitempty"`
}

var (
	refuseKindRequired = derControlRefusal{http.StatusBadRequest, "kind_required", "kind: required"}
	refuseKindUnknown  = derControlRefusal{http.StatusBadRequest, "kind_unknown", "kind: must be scheduled or default"}
)

func refuseDefaultField(name string) derControlRefusal {
	return derControlRefusal{http.StatusBadRequest, "default_field_not_allowed", name + ": not allowed for kind default"}
}

const (
	kindScheduled = "scheduled"
	kindDefault   = "default"
)

// HandleCreate returns the handler for POST /api/devices/{id}/controls.
func (h *AdminDeviceControlHandler) HandleCreate() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		deviceID := r.PathValue("id")
		var logged derControlLog
		var body deviceControlBody
		if ref, ok := decodeDERControlBody(w, r, &body, false); !ok {
			h.Controls.refuse(w, r, "device_control", ref, &logged)
			return
		}
		if body.Kind == nil || *body.Kind == "" {
			h.Controls.refuse(w, r, "device_control", refuseKindRequired, &logged)
			return
		}
		kind := *body.Kind
		if kind != kindScheduled && kind != kindDefault {
			h.Controls.refuse(w, r, "device_control", refuseKindUnknown, &logged)
			return
		}
		if _, ok := h.Controls.loadDevice(w, r, deviceID); !ok {
			return
		}

		cb := derControlCreateBody{
			controlValuesBody: body.controlValuesBody,
			ExecutesGrant:     body.ExecutesGrant, StartTime: body.StartTime, DurationSeconds: body.DurationSeconds, Description: body.Description,
		}
		var req dercontrol.CreateRequest
		var defaultBase *sep2.DERControlBase
		if kind == kindScheduled {
			href := dercontrol.UnscopedProgramHref
			cb.DERProgramHref = &href
			var ref derControlRefusal
			var ok bool
			if req, ref, ok = buildCreateRequest(cb, &logged); !ok {
				h.Controls.refuse(w, r, "device_control", ref, &logged)
				return
			}
			if err := h.Controls.Issuer.Validate(req); err != nil {
				h.Controls.refuse(w, r, "device_control", mapIssuerError(err, &logged), &logged)
				return
			}
		} else {
			for _, f := range []struct {
				name string
				set  bool
			}{{"startTime", body.StartTime != nil}, {"durationSeconds", body.DurationSeconds != nil}, {"description", body.Description != nil}, {"executesGrant", body.ExecutesGrant != nil}} {
				if f.set {
					h.Controls.refuse(w, r, "device_control", refuseDefaultField(f.name), &logged)
					return
				}
			}
			base, ref, ok, err := controlBaseFromValues(body.controlValuesBody, &logged)
			if !ok {
				h.Controls.refuse(w, r, "device_control", ref, &logged)
				return
			}
			if err != nil {
				h.Controls.refuse(w, r, "device_control", mapIssuerError(err, &logged), &logged)
				return
			}
			defaultBase = base
		}

		fh := h.FSAs
		if !fh.storesWired(w, r, fh.AdminFSAs != nil && fh.DeviceFSAs != nil && fh.Copies != nil) {
			return
		}
		if !validHrefSegment(deviceID) {
			ref := derControlRefusal{http.StatusConflict, "device_href_invalid", "device id cannot form a program href"}
			logRefusal(fh.logged(r), r, deviceControlRefusedEvent, ref, &logged)
			writeError(w, ref.status, ref.message)
			return
		}

		// The lock is held through the issue: the writes before it are undone
		// if the issue fails, and nothing else may see or edit them meanwhile.
		fh.programMu.Lock()
		defer fh.programMu.Unlock()
		tx := &deviceTxn{}
		defer tx.finish(fh, r)

		ctx := r.Context()
		fsaID, hasFSA := fh.AdminFSAs.DeviceOnlyFSA(deviceID)
		progID := ""
		if hasFSA {
			held, err := fh.Copies.DevicePrograms(ctx, deviceID, fsaID)
			if err != nil {
				fh.templateFailed(w, r, "device_programs", err)
				return
			}
			if len(held) > 0 {
				progID = held[0]
			}
		}

		primacy, err := fh.choosePrimacy(ctx, deviceID, fsaID, progID)
		if err != nil {
			var none *noOutrankError
			if errors.As(err, &none) {
				logged.add("device", deviceID)
				logged.add("program_href", none.href)
				logRefusal(fh.logged(r), r, deviceControlRefusedEvent, derControlRefusal{status: http.StatusConflict, code: "primacy_cannot_outrank"}, &logged)
				writeJSON(w, http.StatusConflict, programRefusal{Error: none.Error(), Code: "primacy_cannot_outrank"})
				return
			}
			fh.templateFailed(w, r, "primacy", err)
			return
		}

		if !hasFSA || !fh.deviceAssigned(ctx, fsaID, deviceID) {
			var ok bool
			if fsaID, ok = fh.ensureDeviceOnlyFSA(w, r, tx, deviceID, fsaID); !ok {
				return
			}
		}

		if progID == "" {
			var ok bool
			if progID, ok = fh.createDeviceOnlyProgram(w, r, tx, deviceID, fsaID, primacy.value, defaultBase); !ok {
				return
			}
		} else if ok := fh.updateDeviceOnlyProgram(w, r, tx, deviceID, fsaID, progID, primacy, kind == kindDefault, defaultBase); !ok {
			return
		}

		programHref := der.DERProgramHref(deviceID, fsaID, progID)
		reply := DeviceControlCreated{Kind: kind, FSA: fsaID, Program: progID, ProgramHref: programHref, Primacy: primacy.value, PrimacyReason: primacy.reason}
		if kind == kindDefault {
			view, err := fh.deviceProgramView(ctx, deviceID, fsaID, progID)
			if err == nil && view.DefaultControl == nil {
				err = errors.New("default control missing")
			}
			if err != nil {
				fh.templateFailed(w, r, "program_read", err)
				return
			}
			reply.DefaultControl = view.DefaultControl
			tx.committed = true
			tx.after(func() {
				fh.logged(r).InfoContext(ctx, "admin: DER device default set", "event", "der_device_default_set",
					"device", deviceID, "fsa", fsaID, "program", progID, "primacy", primacy.value)
			})
			writeJSON(w, http.StatusCreated, reply)
			return
		}

		req.DERProgramHref = programHref
		res, ok, kept := h.Controls.issue(w, r, "device_control", req, &logged)
		if kept {
			// A control that could not be undone may be live, so the program
			// it sits in stays.
			tx.committed = true
		}
		if !ok {
			return
		}
		tx.committed = true
		h.Controls.notify(ctx, res.Scope)
		supersedes := res.Supersedes
		if supersedes == nil {
			supersedes = []string{}
		}
		h.Controls.logSuccess(r, "der_device_control_created", "admin: DER device control created", res.Scope, res.Control, "supersedes", supersedes, "primacy", primacy.value)
		reply.Control = &DERControlCreated{
			DERControlView:        newDERControlView(res.Scope, res.Control, dercontrol.LifecycleRecord{GrantMRID: req.ExecutesGrant}, sep2time.Now().Unix()),
			Supersedes:            supersedes,
			NotificationAttempted: h.Controls.Notifier != nil,
			Persisted:             h.Controls.Persisted,
		}
		w.Header().Set("Location", res.Href)
		writeJSON(w, http.StatusCreated, reply)
	}
}

// deviceTxn collects what one device-control request wrote, so a request
// that fails after its first write can undo all of it, and what it should
// tell devices and the log only once it has succeeded.
type deviceTxn struct {
	steps     []rollbackStep
	later     []func()
	committed bool
}

func (t *deviceTxn) after(f func()) { t.later = append(t.later, f) }

// finish flushes the deferred notifications and log lines of a committed
// request, or undoes the writes of one that was not.
func (t *deviceTxn) finish(h *AdminFSAHandler, r *http.Request) {
	if t.committed {
		for _, f := range t.later {
			f()
		}
		return
	}
	if len(t.steps) == 0 {
		return
	}
	h.rolledBack(r, errors.New("device control request failed"), t.steps,
		&incompleteLog{"admin: device control left partly applied", "device_control_incomplete", []any{"path", r.URL.Path}})
}

// chosenPrimacy is the primacy a device-only program takes and why. changed
// is true when a stored program holds another value.
type chosenPrimacy struct {
	value   uint8
	reason  string
	changed bool
}

// noOutrankError is returned when a program at primacy 0 leaves no value
// that outranks the device's other programs.
type noOutrankError struct{ href string }

func (e *noOutrankError) Error() string {
	return "no primacy outranks program " + e.href + ", which holds primacy 0; change or remove that program first"
}

// allowedPrimacy reports whether sep.xsd's PrimacyType assigns p to a
// provider: 0 to definedPrimacyHigh are defined, userPrimacyLow to
// userPrimacyHigh are user-defined, and the rest are reserved. It is
// validPrimacy for a uint8.
func allowedPrimacy(p uint8) bool { return validPrimacy(int64(p)) }

// outrankPrimacy picks the primacy of a program that must beat every other
// program of its device when the lowest of theirs is lowest (noOtherProgram
// when there are none): the largest allowed value below lowest, preferring
// the user-defined range (userPrimacyLow to userPrimacyHigh) and then
// definedPrimacyHigh down to 0. Lower numbers win in
// 2030.5, and a value below every other program's cannot tie with one. ok is
// false when lowest is 0.
func outrankPrimacy(lowest int) (value uint8, userRange, ok bool) {
	if v := min(lowest-1, userPrimacyHigh); v >= userPrimacyLow {
		return uint8(v), true, true
	}
	if v := min(lowest-1, definedPrimacyHigh); v >= 0 {
		return uint8(v), false, true
	}
	return 0, false, false
}

// choosePrimacy reads the device's other programs, those that are not
// progID's, and decides the device-only program's primacy: a stored program
// keeps its value while it still outranks them all, otherwise it is picked
// again. Nothing is written.
func (h *AdminFSAHandler) choosePrimacy(ctx context.Context, deviceID, fsaID, progID string) (chosenPrimacy, error) {
	list, err := h.Copies.Programs.List(ctx, deviceID, store.ListOptions{Unbounded: true})
	if err != nil {
		return chosenPrimacy{}, fmt.Errorf("listing programs of device %s: %w", deviceID, err)
	}
	own := ""
	if progID != "" {
		own = der.DERProgramHref(deviceID, fsaID, progID)
	}
	lowest, lowestHref := noOtherProgram, ""
	for _, p := range list.Items {
		if p.Href == own {
			continue
		}
		if int(p.Primacy) < lowest {
			lowest, lowestHref = int(p.Primacy), p.Href
		}
	}

	var current *uint8
	if own != "" {
		prog, err := h.Copies.Programs.Get(ctx, deviceID, progID)
		if err != nil {
			return chosenPrimacy{}, fmt.Errorf("reading program %s/%s: %w", deviceID, progID, err)
		}
		current = &prog.Primacy
	}
	others := "the device has no other program"
	if lowestHref != "" {
		others = fmt.Sprintf("the lowest primacy of its other programs is %d", lowest)
	}
	if current != nil && int(*current) < lowest && allowedPrimacy(*current) {
		return chosenPrimacy{value: *current, reason: fmt.Sprintf("kept %d: it still outranks the device's other programs (%s)", *current, others)}, nil
	}
	value, userRange, ok := outrankPrimacy(lowest)
	if !ok {
		return chosenPrimacy{}, &noOutrankError{href: lowestHref}
	}
	reason := fmt.Sprintf("%d is the largest user-defined primacy (%d to %d) below the others (%s)", value, userPrimacyLow, userPrimacyHigh, others)
	if !userRange {
		reason = fmt.Sprintf("%d is the largest value below the others (%s); it lies outside the user-defined range %d to %d because no value there is below them, and %d to %d are reserved",
			value, others, userPrimacyLow, userPrimacyHigh, definedPrimacyHigh+1, userPrimacyLow-1)
	}
	if current != nil {
		reason = fmt.Sprintf("re-picked %d (was %d): %s", value, *current, reason)
	}
	return chosenPrimacy{value: value, reason: reason, changed: current != nil}, nil
}

// deviceAssigned reports whether deviceID is assigned to fsaID and holds the
// FSA in its own store, the two parts an assignment writes.
func (h *AdminFSAHandler) deviceAssigned(ctx context.Context, fsaID, deviceID string) bool {
	if !slices.Contains(h.AdminFSAs.Devices(ctx, fsaID), deviceID) {
		return false
	}
	_, err := h.DeviceFSAs.Get(ctx, deviceID, fsaID)
	return err == nil
}

// ensureDeviceOnlyFSA creates deviceID's device-only FSA when fsaID is empty,
// assigns the device, and materializes the FSA into the device's own store,
// completing whatever part a restart or an earlier failure left missing. The
// writes are recorded in tx. It answers the request and returns false on
// failure.
func (h *AdminFSAHandler) ensureDeviceOnlyFSA(w http.ResponseWriter, r *http.Request, tx *deviceTxn, deviceID, fsaID string) (string, bool) {
	ctx := r.Context()
	var steps []rollbackStep
	fail := func(cause string, err error) (string, bool) {
		h.rolledBack(r, err, steps, &incompleteLog{"admin: device-only FSA left partly applied", "device_only_fsa_incomplete",
			[]any{"device", deviceID, "fsa", fsaID, "cause", cause, "error", err}})
		h.templateFailed(w, r, cause, err)
		return "", false
	}

	var fsa sep2.FunctionSetAssignments
	if fsaID == "" {
		id, err := mrid.New(rand.Read, h.PEN)
		if err != nil {
			return fail("mint", err)
		}
		fsaID = id
		desc := "device-only " + deviceID
		if len(desc) > maxDescriptionOctets {
			desc = desc[:maxDescriptionOctets]
		}
		fsa = newAdminFSA(id, desc)
		if err := h.AdminFSAs.CreateDeviceOnly(ctx, id, fsa, deviceID); err != nil {
			return fail("fsa_create", err)
		}
		steps = append(steps, rollbackStep{device: deviceID, part: "fsa", undo: func(ctx context.Context) error {
			return h.AdminFSAs.Delete(ctx, id)
		}})
	} else {
		var err error
		if fsa, err = h.AdminFSAs.Get(ctx, fsaID); err != nil {
			return fail("fsa_lookup", err)
		}
	}

	assigned, err := fsaprogram.Assign(ctx, h.AdminFSAs, h.DeviceFSAs, deviceID, fsaID, fsa, fsaprogram.CompleteIfAssigned)
	steps = append(steps, rollbackSteps(deviceID, assigned)...)
	if err != nil {
		var ae *fsaprogram.AssignError
		if errors.As(err, &ae) {
			return fail(string(ae.Stage), ae.Err)
		}
		return fail("assign", err)
	}
	tx.steps = append(tx.steps, steps...)
	tx.after(func() { h.notify(ctx, "/edev/"+deviceID+"/fsa") })
	return fsaID, true
}

// createDeviceOnlyProgram writes the device's one program in fsaID, with
// defaultBase as its default control or an empty one, and returns its id.
func (h *AdminFSAHandler) createDeviceOnlyProgram(w http.ResponseWriter, r *http.Request, tx *deviceTxn, deviceID, fsaID string, primacy uint8, defaultBase *sep2.DERControlBase) (string, bool) {
	prog := memory.ProgramTemplate{Description: deviceProgramDescription, Primacy: primacy}
	if defaultBase != nil {
		prog.DefaultControlBase = defaultBase.Copy()
	}
	if err := h.mintTemplateIDs(&prog); err != nil {
		h.templateFailed(w, r, "mint", err)
		return "", false
	}
	undo, err := h.Copies.EnsureDevice(r.Context(), deviceID, fsaID, prog)
	if err != nil {
		h.ensureDeviceFailed(w, r, err)
		return "", false
	}
	tx.steps = append(tx.steps, rollbackStep{device: deviceID, template: prog.ID, part: "program", undo: undo})
	tx.after(func() {
		h.notify(r.Context(), programListHref(deviceID, fsaID))
		h.logged(r).InfoContext(r.Context(), "admin: DER device-only program created", "event", "der_device_program_created",
			"device", deviceID, "fsa", fsaID, "program", prog.ID, "mrid", prog.MRID, "primacy", primacy)
	})
	return prog.ID, true
}

// updateDeviceOnlyProgram applies a re-picked primacy and, for kind default,
// the new default control, as one edit that raises the versions it changes.
func (h *AdminFSAHandler) updateDeviceOnlyProgram(w http.ResponseWriter, r *http.Request, tx *deviceTxn, deviceID, fsaID, progID string, primacy chosenPrimacy, setDefault bool, defaultBase *sep2.DERControlBase) bool {
	if !primacy.changed && !setDefault {
		return true
	}
	var mutateProgram func(*sep2.DERProgram) error
	if primacy.changed {
		mutateProgram = func(p *sep2.DERProgram) error {
			p.Primacy = primacy.value
			return nil
		}
	}
	var mutateDefault func(*sep2.DefaultDERControl) error
	if setDefault {
		mutateDefault = func(d *sep2.DefaultDERControl) error {
			base := defaultBase.Copy()
			d.DERControlBase = &base
			return nil
		}
	}
	ctx := r.Context()
	scope := fsaprogram.DefaultScope(deviceID, fsaID, progID)
	pBefore, err := h.Copies.Programs.Get(ctx, deviceID, progID)
	if err != nil {
		h.templateFailed(w, r, "program_lookup", err)
		return false
	}
	dBefore, err := h.Copies.Defaults.Get(ctx, scope, coresingleton.SingletonKey)
	if err != nil {
		h.templateFailed(w, r, "default_lookup", err)
		return false
	}
	ch, err := fsaprogram.EditWithDefault(ctx, h.Copies.Programs, h.Copies.Defaults,
		fsaprogram.Ref{DeviceID: deviceID, ProgramID: progID}, scope, mutateProgram, mutateDefault)
	switch {
	case err == nil:
	case errors.Is(err, fsaprogram.ErrVersionExhausted):
		refuseProgram(w, "version_exhausted", "the program's or its default control's version is at 65535")
		return false
	default:
		h.templateFailed(w, r, "program_edit", err)
		return false
	}
	if ch.Program || ch.Default {
		// The stored values go back as they were, versions included.
		tx.steps = append(tx.steps, rollbackStep{device: deviceID, template: progID, part: "program_edit", undo: func(ctx context.Context) error {
			if err := h.Copies.Defaults.Update(ctx, scope, coresingleton.SingletonKey, dBefore); err != nil {
				return err
			}
			return h.Copies.Programs.Update(ctx, deviceID, progID, pBefore)
		}})
	}
	tx.after(func() {
		h.notifyEdited(ctx, deviceID, fsaID, progID, ch)
		if ch.Program {
			h.logged(r).InfoContext(ctx, "admin: DER device-only program edited", "event", "der_device_program_edited",
				"device", deviceID, "program", progID, "primacy", primacy.value)
		}
	})
	return true
}

// deviceOnlyRefused answers a template create on a device-only FSA, and
// reports whether it did.
func (h *AdminFSAHandler) deviceOnlyRefused(w http.ResponseWriter, fsaID string) bool {
	dev := h.AdminFSAs.DeviceOnlyDevice(fsaID)
	if dev == "" {
		return false
	}
	refuseProgram(w, "fsa_device_only", "this fsa is bound to device "+dev+" alone; send its controls with POST /api/devices/{id}/controls")
	return true
}
