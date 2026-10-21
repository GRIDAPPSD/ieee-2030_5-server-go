package handler

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/dercontrol"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/fsaprogram"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/mrid"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/handlers/der"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

// #920: DER program templates on an admin FSA.
//
//	POST /api/fsas/{id}/der-programs   create a template and copy it to every assigned device
//	GET  /api/fsas/{id}/der-programs   list the FSA's templates
//
// #921: a program on one device.
//
//	POST /api/devices/{id}/fsas/{fsaId}/der-programs   create a device program

// maxTemplatesPerFSA bounds the issuer's per-scope locks, which are never
// evicted: each template adds one scope per assigned device.
const maxTemplatesPerFSA = 16

// maxDeviceProgramsPerFSA bounds device programs under one device and FSA for
// the same reason: each is one more never-evicted issuer scope.
const maxDeviceProgramsPerFSA = 16

// templateInternalError is the whole body of every 500 here; the cause is
// logged, never returned, since store errors can carry paths (#541).
const templateInternalError = "program write failed"

type programTemplateBody struct {
	Description    *string             `json:"description"`
	Primacy        *int64              `json:"primacy"`
	MRID           *string             `json:"mRID"`
	DefaultControl *defaultControlBody `json:"defaultControl"`
}

type defaultControlBody struct {
	Type        *string                `json:"type"`
	Functions   json.RawMessage        `json:"functions"`
	MaxLimW     *int64                 `json:"maxLimW"`
	PowerFactor *derControlPowerFactor `json:"powerFactor"`
	TargetW     *derControlTargetW     `json:"targetW"`
}

// ProgramTemplateView is one template as the admin API reports it. Copies
// lists the href of every device copy, so it is [] and never null.
type ProgramTemplateView struct {
	ID             string                    `json:"id"`
	MRID           string                    `json:"mRID"`
	Description    string                    `json:"description"`
	Primacy        uint8                     `json:"primacy"`
	Version        uint16                    `json:"version"`
	DefaultControl ProgramDefaultControlView `json:"defaultControl"`
	Copies         []string                  `json:"copies"`
}

// ProgramDefaultControlView is a template's DefaultDERControl.
type ProgramDefaultControlView struct {
	MRID           string             `json:"mRID"`
	Type           string             `json:"type"`
	DERControlBase DERControlBaseView `json:"derControlBase"`
}

// HandleCreateProgramTemplate returns a handler for POST
// /api/fsas/{id}/der-programs.
func (h *AdminFSAHandler) HandleCreateProgramTemplate() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		fsaID := r.PathValue("id")
		var body programTemplateBody
		if ref, ok := decodeDERControlBody(w, r, &body, false); !ok {
			writeError(w, ref.status, ref.message)
			return
		}
		tmpl, msg, ok := buildTemplate(body)
		if !ok {
			writeError(w, http.StatusBadRequest, msg)
			return
		}

		if _, err := h.AdminFSAs.Get(r.Context(), fsaID); err != nil {
			if errors.Is(err, store.ErrNotFound) {
				writeError(w, http.StatusNotFound, "fsa not found")
				return
			}
			h.templateFailed(w, r, "fsa_lookup", err)
			return
		}
		if h.deviceOnlyRefused(w, fsaID) {
			return
		}
		if !validHrefSegment(fsaID) {
			writeError(w, http.StatusConflict, "fsa id cannot form a program href; create the FSA with an mRID of letters, digits, '.', '_' or '-'")
			return
		}
		if !h.storesWired(w, r, h.Copies != nil) {
			return
		}

		h.programMu.Lock()
		defer h.programMu.Unlock()

		if len(h.AdminFSAs.Templates(r.Context(), fsaID)) >= maxTemplatesPerFSA {
			writeError(w, http.StatusConflict, fmt.Sprintf("fsa already has %d program templates", maxTemplatesPerFSA))
			return
		}
		if err := h.mintTemplateIDs(&tmpl); err != nil {
			h.templateFailed(w, r, "mint", err)
			return
		}

		devices := h.AdminFSAs.Devices(r.Context(), fsaID)
		for _, dev := range devices {
			if _, err := h.Copies.Check(r.Context(), dev, fsaID, tmpl); err != nil {
				h.copyRefused(w, r, err)
				return
			}
		}

		switch err := h.AdminFSAs.CreateTemplate(r.Context(), fsaID, tmpl); {
		case err == nil:
		case errors.Is(err, store.ErrAlreadyExists):
			writeError(w, http.StatusConflict, "a template with this mRID already exists on this fsa")
			return
		case errors.Is(err, store.ErrNotFound):
			writeError(w, http.StatusNotFound, "fsa not found")
			return
		default:
			h.templateFailed(w, r, "template_write", err)
			return
		}

		// The template is undone last, so a copy whose undo fails still has
		// its template, and the boot rebuild completes that copy.
		steps := []rollbackStep{{template: tmpl.ID, part: "template", undo: func(ctx context.Context) error {
			return h.AdminFSAs.DeleteTemplate(ctx, fsaID, tmpl.ID)
		}}}
		for _, dev := range devices {
			undo, err := h.Copies.Ensure(r.Context(), dev, fsaID, tmpl)
			if err == nil {
				steps = append(steps, rollbackStep{device: dev, template: tmpl.ID, part: "copy", undo: undo})
				continue
			}
			if !h.rolledBack(r, err, steps, &incompleteLog{"admin: DER program template create left partly applied", "der_program_template_incomplete",
				[]any{"fsa", fsaID, "template", tmpl.ID, "error", err}}) {
				writeError(w, http.StatusInternalServerError, fmt.Sprintf(
					"program template write failed; template %s was kept on fsa %s and its device copies are completed at the next restart", tmpl.ID, fsaID))
				return
			}
			h.copyRefused(w, r, err)
			return
		}

		for _, dev := range devices {
			h.notify(r.Context(), programListHref(dev, fsaID))
		}
		h.logged(r).InfoContext(r.Context(), "admin: DER program template created", "event", "der_program_template_created",
			"fsa", fsaID, "template", tmpl.ID, "mrid", tmpl.MRID, "copies", len(devices))
		writeJSON(w, http.StatusCreated, h.templateView(r.Context(), fsaID, tmpl))
	}
}

// HandleListProgramTemplates returns a handler for GET
// /api/fsas/{id}/der-programs.
func (h *AdminFSAHandler) HandleListProgramTemplates() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		fsaID := r.PathValue("id")
		if _, err := h.AdminFSAs.Get(r.Context(), fsaID); err != nil {
			if errors.Is(err, store.ErrNotFound) {
				writeError(w, http.StatusNotFound, "fsa not found")
				return
			}
			h.templateFailed(w, r, "fsa_lookup", err)
			return
		}
		tmpls := h.AdminFSAs.Templates(r.Context(), fsaID)
		out := make([]ProgramTemplateView, 0, len(tmpls))
		for _, t := range tmpls {
			out = append(out, h.templateView(r.Context(), fsaID, t))
		}
		writeJSON(w, http.StatusOK, map[string]any{"templates": out})
	}
}

// buildTemplate validates body into a template with no ids yet, or returns
// the 400 message naming the first bad field.
func buildTemplate(body programTemplateBody) (memory.ProgramTemplate, string, bool) {
	var t memory.ProgramTemplate
	e, msg, ok := buildProgramEdit(body)
	if !ok {
		return t, msg, false
	}
	if e.defaultBase == nil {
		return t, "defaultControl: required", false
	}
	t.Description, t.Primacy, t.MRID, t.DefaultControlBase = e.description, e.primacy, e.mRID, *e.defaultBase
	return t, "", true
}

// programEdit is a validated program body. mRID is "" and defaultBase nil
// when the body omits them.
type programEdit struct {
	description string
	primacy     uint8
	mRID        string
	defaultBase *sep2.DERControlBase
}

// buildProgramEdit validates body, or returns the 400 message naming the
// first bad field.
func buildProgramEdit(body programTemplateBody) (programEdit, string, bool) {
	var e programEdit
	if body.Description == nil || !validTemplateDescription(*body.Description) {
		return e, "description: 1 to 32 printable ASCII characters required", false
	}
	e.description = *body.Description
	if body.Primacy == nil || !validPrimacy(*body.Primacy) {
		return e, primacyRefusalText, false
	}
	e.primacy = uint8(*body.Primacy)
	if body.MRID != nil {
		m, ok := normalizeMRID(*body.MRID)
		// mRIDType reserves the high 96 bits all F, for any PEN.
		if !ok || strings.HasPrefix(m, "FFFFFFFFFFFFFFFFFFFFFFFF") {
			return e, "mRID: must be 32 hex digits, not the reserved form whose first 24 digits are F", false
		}
		e.mRID = m
	}

	dc := body.DefaultControl
	if dc == nil {
		return e, "", true
	}
	var req dercontrol.CreateRequest
	values := derControlCreateBody{MaxLimW: dc.MaxLimW, PowerFactor: dc.PowerFactor, TargetW: dc.TargetW}
	if ref, ok := buildControlValues(dc.Type, dc.Functions, values, &req, &derControlLog{}); !ok {
		return e, "defaultControl." + ref.message, false
	}
	base, err := dercontrol.ControlBase(req)
	if err != nil {
		return e, "defaultControl: invalid value", false
	}
	e.defaultBase = base
	return e, "", true
}

// mintTemplateIDs fills the server-chosen id, the program mRID when the
// operator supplied none, and the default control's mRID.
func (h *AdminFSAHandler) mintTemplateIDs(t *memory.ProgramTemplate) error {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return err
	}
	t.ID = "derp-" + hex.EncodeToString(b[:])
	if t.MRID == "" {
		m, err := mrid.New(rand.Read, h.PEN)
		if err != nil {
			return err
		}
		t.MRID = m
	}
	for {
		m, err := mrid.New(rand.Read, h.PEN)
		if err != nil {
			return err
		}
		if m != t.MRID {
			t.DefaultMRID = m
			return nil
		}
	}
}

func (h *AdminFSAHandler) templateView(ctx context.Context, fsaID string, t memory.ProgramTemplate) ProgramTemplateView {
	v := ProgramTemplateView{
		ID:             t.ID,
		MRID:           t.MRID,
		Description:    t.Description,
		Primacy:        t.Primacy,
		Version:        t.Version,
		DefaultControl: programDefaultControlView(t),
		Copies:         []string{},
	}
	if h.Copies == nil {
		return v
	}
	for _, dev := range h.AdminFSAs.Devices(ctx, fsaID) {
		if present, err := h.Copies.Check(ctx, dev, fsaID, t); err == nil && present {
			v.Copies = append(v.Copies, der.DERProgramHref(dev, fsaID, t.ID))
		}
	}
	return v
}

func programDefaultControlView(t memory.ProgramTemplate) ProgramDefaultControlView {
	base := t.DefaultControlBase
	return ProgramDefaultControlView{
		MRID:           t.DefaultMRID,
		Type:           controlTypeName(&base),
		DERControlBase: newDERControlBaseView(&base),
	}
}

// DeviceProgramView is one device program as the admin API reports it.
type DeviceProgramView struct {
	ID             string                     `json:"id"`
	Href           string                     `json:"href"`
	MRID           string                     `json:"mRID"`
	Description    string                     `json:"description"`
	Primacy        uint8                      `json:"primacy"`
	Version        uint16                     `json:"version"`
	DefaultControl *ProgramDefaultControlView `json:"defaultControl,omitempty"`
}

// HandleCreateDeviceProgram returns a handler for POST
// /api/devices/{id}/fsas/{fsaId}/der-programs. The FSA must be in the
// device's own FSA store: the wire serves a default FSA for any id, so the
// store, not the wire, says what the device has.
func (h *AdminFSAHandler) HandleCreateDeviceProgram() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		deviceID, fsaID := r.PathValue("id"), r.PathValue("fsaId")
		var body programTemplateBody
		if ref, ok := decodeDERControlBody(w, r, &body, false); !ok {
			writeError(w, ref.status, ref.message)
			return
		}
		prog, msg, ok := buildTemplate(body)
		if !ok {
			writeError(w, http.StatusBadRequest, msg)
			return
		}

		if h.EndDevices != nil {
			if _, err := h.EndDevices.Get(r.Context(), deviceID); err != nil {
				if errors.Is(err, store.ErrNotFound) {
					writeError(w, http.StatusNotFound, "device not found")
					return
				}
				h.templateFailed(w, r, "device_lookup", err)
				return
			}
		}
		if !h.storesWired(w, r, h.DeviceFSAs != nil && h.Copies != nil) {
			return
		}
		if !validHrefSegment(deviceID) || !validHrefSegment(fsaID) {
			writeError(w, http.StatusConflict, "device or fsa id cannot form a program href")
			return
		}

		h.programMu.Lock()
		defer h.programMu.Unlock()

		// Under the lock, so an unassign cannot remove the device's FSA
		// between this check and the write.
		if _, err := h.DeviceFSAs.Get(r.Context(), deviceID, fsaID); err != nil {
			if errors.Is(err, store.ErrNotFound) {
				writeError(w, http.StatusNotFound, "fsa not found on this device")
				return
			}
			h.templateFailed(w, r, "fsa_lookup", err)
			return
		}

		held, err := h.Copies.DevicePrograms(r.Context(), deviceID, fsaID)
		if err != nil {
			h.templateFailed(w, r, "device_programs", err)
			return
		}
		if len(held) >= maxDeviceProgramsPerFSA {
			writeError(w, http.StatusConflict, fmt.Sprintf("device already has %d programs of its own under this fsa", maxDeviceProgramsPerFSA))
			return
		}
		if err := h.mintTemplateIDs(&prog); err != nil {
			h.templateFailed(w, r, "mint", err)
			return
		}

		if _, err := h.Copies.EnsureDevice(r.Context(), deviceID, fsaID, prog); err != nil {
			h.ensureDeviceFailed(w, r, err)
			return
		}

		h.notify(r.Context(), programListHref(deviceID, fsaID))
		dflt := programDefaultControlView(prog)
		h.logged(r).InfoContext(r.Context(), "admin: DER device program created", "event", "der_device_program_created",
			"device", deviceID, "fsa", fsaID, "program", prog.ID, "mrid", prog.MRID)
		writeJSON(w, http.StatusCreated, DeviceProgramView{
			ID:             prog.ID,
			Href:           der.DERProgramHref(deviceID, fsaID, prog.ID),
			MRID:           prog.MRID,
			Description:    prog.Description,
			Primacy:        prog.Primacy,
			Version:        prog.Version,
			DefaultControl: &dflt,
		})
	}
}

// ensureDeviceFailed answers a failed Copies.EnsureDevice: 409 for a program
// the device already holds, and a logged 500 otherwise.
func (h *AdminFSAHandler) ensureDeviceFailed(w http.ResponseWriter, r *http.Request, err error) {
	var part *fsaprogram.PartError
	switch {
	case errors.Is(err, fsaprogram.ErrCopyConflict):
		writeError(w, http.StatusConflict, "the device already holds a program with this id or mRID")
	case errors.As(err, &part):
		h.logUndoFailed(r, part.DeviceID, part.TemplateID, part.Part, err)
		writeError(w, http.StatusInternalServerError, templateInternalError)
	default:
		h.templateFailed(w, r, "program_write", err)
	}
}

// copyRefused answers a copy that conflicts with a device's own program as
// 409, and any other failure as a 500.
func (h *AdminFSAHandler) copyRefused(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, fsaprogram.ErrCopyConflict) {
		writeError(w, http.StatusConflict, "a device assigned to this fsa already holds a program with this id or mRID")
		return
	}
	h.templateFailed(w, r, "copy_write", err)
}

// storesWired reports whether the program stores a route needs are wired. When
// they are not it answers a logged 500 and the caller returns.
func (h *AdminFSAHandler) storesWired(w http.ResponseWriter, r *http.Request, wired bool) bool {
	if !wired {
		h.templateFailed(w, r, "no_program_stores", errors.New("program stores are not wired"))
	}
	return wired
}

func (h *AdminFSAHandler) templateFailed(w http.ResponseWriter, r *http.Request, cause string, err error) {
	h.logged(r).ErrorContext(r.Context(), "admin: DER program write failed", "event", "der_program_template_failed",
		"cause", cause, "path", r.URL.Path, "error", err)
	writeError(w, http.StatusInternalServerError, templateInternalError)
}

// rollbackStep undoes one write of a template create, assign or unassign.
// device, template and part name what the write touched, for the log line
// written when the undo fails.
type rollbackStep struct {
	device, template, part string
	undo                   func(context.Context) error
}

// rollbackAfter undoes steps newest first after cause and reports whether
// every write was undone. It stops at the first undo that fails, and at once
// when cause is an Ensure whose own undo failed, so every write older than
// the failure stays: the admin record is always the oldest write, and the
// boot rebuild completes every copy it names. Each write left is logged with
// its device, template and part.
func (h *AdminFSAHandler) rollbackAfter(r *http.Request, cause error, steps []rollbackStep) bool {
	ctx := context.WithoutCancel(r.Context())
	var part *fsaprogram.PartError
	if errors.As(cause, &part) {
		h.logUndoFailed(r, part.DeviceID, part.TemplateID, part.Part, cause)
		return false
	}
	for i := len(steps) - 1; i >= 0; i-- {
		st := steps[i]
		if err := st.undo(ctx); err != nil {
			if errors.As(err, &part) {
				h.logUndoFailed(r, part.DeviceID, part.TemplateID, part.Part, err)
			} else {
				h.logUndoFailed(r, st.device, st.template, st.part, err)
			}
			return false
		}
	}
	return true
}

// incompleteLog is the audit line of a write that could not be undone: its
// message, its event name and the attributes that name what is left.
type incompleteLog struct {
	msg, event string
	attrs      []any
}

// rolledBack undoes steps after cause and reports whether every write was
// undone. When one was not, it logs note when there is one, and the caller
// answers. It is the one place a rollback's failure is turned into a log line.
func (h *AdminFSAHandler) rolledBack(r *http.Request, cause error, steps []rollbackStep, note *incompleteLog) bool {
	if h.rollbackAfter(r, cause, steps) {
		return true
	}
	if note != nil {
		h.logged(r).ErrorContext(r.Context(), note.msg, append([]any{"event", note.event}, note.attrs...)...)
	}
	return false
}

func (h *AdminFSAHandler) logUndoFailed(r *http.Request, device, template, part string, err error) {
	h.logged(r).ErrorContext(r.Context(), "admin: DER program undo incomplete", "event", "der_program_template_undo_failed",
		"path", r.URL.Path, "device", device, "template", template, "part", part, "error", err)
}

// audit writes an audit line with the base fields every audit line shares.
func (h *AdminFSAHandler) audit(r *http.Request, level slog.Level, msg, event string, attrs ...any) {
	auditLog(h.logged(r), r, level, msg, event, attrs)
}

// logged is the handler's logger, or slog's default when none is set.
func (h *AdminFSAHandler) logged(*http.Request) *slog.Logger {
	if h.Logger != nil {
		return h.Logger
	}
	return slog.Default()
}

func (h *AdminFSAHandler) notify(ctx context.Context, href string) {
	h.notifyStatus(ctx, href, sep2.NotificationStatusDefault)
}

func (h *AdminFSAHandler) notifyStatus(ctx context.Context, href string, status uint8) {
	if h.Notifier != nil {
		h.Notifier.Notify(ctx, href, status)
	}
}

// programListHref is the per-FSA program list a device walks; lists are per
// FSA (#916), so a program change notifies only its own FSA's list.
func programListHref(deviceID, fsaID string) string {
	return "/edev/" + deviceID + "/fsa/" + fsaID + "/derp"
}

// validTemplateDescription is IEEE 2030.5 String32, narrowed to printable
// ASCII so the admin UI and logs show exactly what a device receives.
func validTemplateDescription(s string) bool {
	if len(s) == 0 || len(s) > maxDescriptionOctets {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < 0x20 || s[i] > 0x7E {
			return false
		}
	}
	return true
}

// Primacy bounds. IEEE 2030.5-2018 defines 0 to definedPrimacyHigh and leaves
// userPrimacyLow to userPrimacyHigh to the implementer; the device path
// prefers the user range and falls back below it only when no value there
// outranks the device's other programs.
const (
	definedPrimacyHigh = 2
	userPrimacyLow     = 65
	userPrimacyHigh    = 191
)

// primacyRefusalText is the 400 message of a program write whose primacy is
// outside the allowed values.
var primacyRefusalText = fmt.Sprintf("primacy: must be 0, 1, %d or %d to %d", definedPrimacyHigh, userPrimacyLow, userPrimacyHigh)

// validPrimacy accepts the PrimacyType values IEEE 2030.5-2018 defines and
// the range it leaves to the implementer.
func validPrimacy(p int64) bool {
	return (p >= 0 && p <= definedPrimacyHigh) || (p >= userPrimacyLow && p <= userPrimacyHigh)
}

// validHrefSegment reports whether s can be a segment of a program href the
// DER control API accepts (validProgramHref).
func validHrefSegment(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c == '.' || c == '_' || c == '-') {
			return false
		}
	}
	return true
}
