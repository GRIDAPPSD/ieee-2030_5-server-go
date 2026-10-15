package handler

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"log/slog"
	"net/http"

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

// maxTemplatesPerFSA bounds the issuer's per-scope locks, which are never
// evicted: each template adds one scope per assigned device.
const maxTemplatesPerFSA = 16

// templateInternalError is the whole body of every 500 here; the cause is
// logged, never returned, since store errors can carry paths (#541).
const templateInternalError = "program template write failed"

type programTemplateBody struct {
	Description    *string             `json:"description"`
	Primacy        *int64              `json:"primacy"`
	MRID           *string             `json:"mRID"`
	DefaultControl *defaultControlBody `json:"defaultControl"`
}

type defaultControlBody struct {
	Type        *string                `json:"type"`
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
		if !validHrefSegment(fsaID) {
			writeError(w, http.StatusConflict, "fsa id cannot form a program href; create the FSA with an mRID of letters, digits, '.', '_' or '-'")
			return
		}
		if h.Copies == nil {
			h.templateFailed(w, r, "no_program_stores", errors.New("program stores are not wired"))
			return
		}

		h.programMu.Lock()
		defer h.programMu.Unlock()

		if len(h.AdminFSAs.Templates(r.Context(), fsaID)) >= maxTemplatesPerFSA {
			writeError(w, http.StatusConflict, "fsa already has 16 program templates")
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

		var undos []func(context.Context) error
		for _, dev := range devices {
			undo, err := h.Copies.Ensure(r.Context(), dev, fsaID, tmpl)
			if err != nil {
				undos = append(undos, func(ctx context.Context) error {
					return h.AdminFSAs.DeleteTemplate(ctx, fsaID, tmpl.ID)
				})
				h.undoCopies(r, undos)
				h.copyRefused(w, r, err)
				return
			}
			undos = append(undos, undo)
		}

		for _, dev := range devices {
			h.notify(r.Context(), programListHref(dev, fsaID))
		}
		slog.InfoContext(r.Context(), "admin: DER program template created", "event", "der_program_template_created",
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
	if body.Description == nil || !validTemplateDescription(*body.Description) {
		return t, "description: 1 to 32 printable ASCII characters required", false
	}
	t.Description = *body.Description
	if body.Primacy == nil || !validPrimacy(*body.Primacy) {
		return t, "primacy: must be 0, 1, 2 or 65 to 191", false
	}
	t.Primacy = uint8(*body.Primacy)
	if body.MRID != nil {
		m, ok := normalizeMRID(*body.MRID)
		if !ok || m == "FFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFF" {
			return t, "mRID: must be 32 hex digits, not all F", false
		}
		t.MRID = m
	}

	dc := body.DefaultControl
	if dc == nil {
		return t, "defaultControl: required", false
	}
	if dc.Type == nil || *dc.Type == "" {
		return t, "defaultControl." + refuseTypeRequired.message, false
	}
	typ, ok := controlTypes[*dc.Type]
	if !ok {
		return t, "defaultControl." + refuseTypeUnknown.message, false
	}
	req := dercontrol.CreateRequest{Type: typ}
	values := derControlCreateBody{MaxLimW: dc.MaxLimW, PowerFactor: dc.PowerFactor, TargetW: dc.TargetW}
	if ref, ok := buildValue(typ, values, &req, &derControlLog{}); !ok {
		return t, "defaultControl." + ref.message, false
	}
	base, err := dercontrol.ControlBase(req)
	if err != nil {
		return t, "defaultControl: invalid value", false
	}
	t.DefaultControlBase = *base
	return t, "", true
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
	base := t.DefaultControlBase
	v := ProgramTemplateView{
		ID:          t.ID,
		MRID:        t.MRID,
		Description: t.Description,
		Primacy:     t.Primacy,
		Version:     t.Version,
		DefaultControl: ProgramDefaultControlView{
			MRID:           t.DefaultMRID,
			Type:           controlTypeName(&base),
			DERControlBase: newDERControlBaseView(&base),
		},
		Copies: []string{},
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

// copyRefused answers a copy that conflicts with a device's own program as
// 409, and any other failure as a 500.
func (h *AdminFSAHandler) copyRefused(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, fsaprogram.ErrCopyConflict) {
		writeError(w, http.StatusConflict, "a device assigned to this fsa already holds a program with this id or mRID")
		return
	}
	h.templateFailed(w, r, "copy_write", err)
}

func (h *AdminFSAHandler) templateFailed(w http.ResponseWriter, r *http.Request, cause string, err error) {
	slog.ErrorContext(r.Context(), "admin: DER program template write failed", "event", "der_program_template_failed",
		"cause", cause, "path", r.URL.Path, "error", err)
	writeError(w, http.StatusInternalServerError, templateInternalError)
}

// undoCopies runs undos newest first and logs any it could not finish: the
// stores then hold part of a copy, which the boot rebuild completes.
func (h *AdminFSAHandler) undoCopies(r *http.Request, undos []func(context.Context) error) {
	ctx := context.WithoutCancel(r.Context())
	for i := len(undos) - 1; i >= 0; i-- {
		if err := undos[i](ctx); err != nil {
			slog.ErrorContext(ctx, "admin: DER program template undo incomplete", "event", "der_program_template_undo_failed",
				"path", r.URL.Path, "error", err)
		}
	}
}

func (h *AdminFSAHandler) notify(ctx context.Context, href string) {
	if h.Notifier != nil {
		h.Notifier.Notify(ctx, href, sep2.NotificationStatusDefault)
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

// validPrimacy accepts the PrimacyType values IEEE 2030.5-2018 defines (0 to
// 2) and the range it leaves to the implementer (65 to 191).
func validPrimacy(p int64) bool {
	return (p >= 0 && p <= 2) || (p >= 65 && p <= 191)
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
