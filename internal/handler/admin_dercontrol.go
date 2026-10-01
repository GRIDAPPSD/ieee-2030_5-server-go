package handler

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"math"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/auth"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/commitment"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/dercontrol"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/derhref"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/handlers/sep2time"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
)

// #566: admin API for DER controls.
//
//	POST /api/der/controls                 create a control (dercontrol.Issuer)
//	GET  /api/der/controls?device=<id>     list admin-issued controls
//	POST /api/der/controls/{mrid}/cancel   cancel a control
//	GET  /api/devices/{id}/der-programs    list a device's DERPrograms
//
// These writes change what a physical device does, so the router requires a
// real credential for both POST routes even from loopback: they are not on
// the server package's nonSensitiveAdminWrites list.

// derControlMaxBody bounds a request body. Larger bodies are refused, not
// truncated, so a padded body can never decode as a shorter valid one.
const derControlMaxBody = 64 << 10

// Description and reason bounds, in octets of UTF-8: IEEE 2030.5-2018
// Annex B.2 String32 and String192 bound a string by maxLength octets.
const (
	maxDescriptionOctets  = 32
	maxCancelReasonOctets = 192
)

// validString reports whether s is valid UTF-8 of at most max octets.
func validString(s string, max int) bool {
	return utf8.ValidString(s) && len(s) <= max
}

// DERControlIssuer creates and cancels admin-issued DER controls. The
// production implementation is *dercontrol.Issuer. Create goes through
// IssueInFleet only, so no control is stored without a commitment check.
type DERControlIssuer interface {
	Validate(req dercontrol.CreateRequest) error
	IssueInFleet(ctx context.Context, req dercontrol.CreateRequest, fleet dercontrol.Fleet) (dercontrol.Result, error)
	Cancel(ctx context.Context, scope dercontrol.Scope, id, reason string) (dercontrol.LifecycleRecord, error)
}

// DERControlReader is the read side of the DERControl store the list and
// cancel routes need. The production implementation is
// *memory.DERControlStore.
type DERControlReader interface {
	List(ctx context.Context, parentID string, opts store.ListOptions) (store.ListResult[sep2.DERControl], error)
	Parents(ctx context.Context) ([]string, error)
	ByMRID(ctx context.Context, mrid string) (parentID, id string, control sep2.DERControl, err error)
}

// DERControlLifecycleReader reads a control's lifecycle record. A control
// with no record was not issued through this API and is not listed.
type DERControlLifecycleReader interface {
	Get(ctx context.Context, parentID, id string) (dercontrol.LifecycleRecord, error)
}

// DERProgramLister lists the DERPrograms stored under one EndDevice.
type DERProgramLister interface {
	List(ctx context.Context, parentID string, opts store.ListOptions) (store.ListResult[sep2.DERProgram], error)
}

// EndDeviceGetter loads one EndDevice by its store id.
type EndDeviceGetter interface {
	Get(ctx context.Context, id string) (sep2.EndDevice, error)
}

// ResponseLister reads the Response records devices post to a ResponseSet.
type ResponseLister interface {
	Parents(ctx context.Context) ([]string, error)
	List(ctx context.Context, parentID string, opts store.ListOptions) (store.ListResult[sep2.Response], error)
}

// commitmentFailure is a commitment check that could not complete. It
// answers 500, never 409, and never lets the create through. sub is a fixed
// name logged in place of err, whose text can carry a store's diagnostic.
type commitmentFailure struct {
	sub string
	err error
}

func (e *commitmentFailure) Error() string { return "commitment check could not complete: " + e.sub }

func (e *commitmentFailure) Unwrap() error { return e.err }

// controlReach is the number of a fleet's devices that read one control: a
// control is served only under the EndDevice it is stored beneath.
const controlReach = 1

// FleetResolver maps an EndDevice id to its commitment fleet key. The
// production implementation is commitment.Resolver.
type FleetResolver interface {
	FleetOf(ctx context.Context, endDeviceID string) (string, error)
}

// CommitmentLedger serializes a create against the commitments of its
// fleet. The production implementation is *commitment.Ledger.
type CommitmentLedger interface {
	Within(ctx context.Context, fleetKeys []string, fn func(commitment.View) error) error
}

// AdminDERControlHandler serves the DER control admin routes. Notifier and
// Logger may be nil; every other field is required.
type AdminDERControlHandler struct {
	Issuer     DERControlIssuer
	Controls   DERControlReader
	Lifecycles DERControlLifecycleReader
	Programs   DERProgramLister
	EndDevices EndDeviceGetter
	Responses  ResponseLister
	Notifier   ResourceNotifier

	// Fleets and Ledger bound every create by the commitments of the
	// program's fleet (GRIDAPPSD/ieee-2030_5-server-go#714). A nil one
	// answers 500 rather than creating unchecked.
	Fleets FleetResolver
	Ledger CommitmentLedger

	// Persisted reports whether both the control and lifecycle stores write
	// through to disk, echoed in the create response.
	Persisted bool

	Logger *slog.Logger
}

func (h *AdminDERControlHandler) logger() *slog.Logger {
	if h.Logger != nil {
		return h.Logger
	}
	return slog.Default()
}

// derControlRefusal is a refusal with a fixed public message. code is the
// stable identifier logged in place of any request value.
type derControlRefusal struct {
	status  int
	code    string
	message string
}

var (
	refuseBodyTooLarge        = derControlRefusal{http.StatusRequestEntityTooLarge, "body_too_large", "request body too large"}
	refuseInvalidJSON         = derControlRefusal{http.StatusBadRequest, "invalid_json", "invalid JSON"}
	refuseUnknownField        = derControlRefusal{http.StatusBadRequest, "unknown_field", "unknown field"}
	refuseProgramHrefRequired = derControlRefusal{http.StatusBadRequest, "program_href_required", "derProgramHref: required"}
	refuseProgramHrefFormat   = derControlRefusal{http.StatusBadRequest, "program_href_invalid", "derProgramHref: invalid format"}
	refuseTypeRequired        = derControlRefusal{http.StatusBadRequest, "type_required", "type: required"}
	refuseTypeUnknown         = derControlRefusal{http.StatusBadRequest, "type_unknown", "type: unknown control type"}
	refuseMaxLimWRequired     = derControlRefusal{http.StatusBadRequest, "max_lim_w_required", "maxLimW: required for type maxLimW"}
	refuseMaxLimWRange        = derControlRefusal{http.StatusBadRequest, "max_lim_w_range", "maxLimW: must be 0 to 10000"}
	refuseMaxLimWNotAllowed   = derControlRefusal{http.StatusBadRequest, "max_lim_w_not_allowed", "maxLimW: not allowed for this type"}
	refusePFRequired          = derControlRefusal{http.StatusBadRequest, "power_factor_required", "powerFactor: required for type fixedPFInjectW"}
	refusePFNotAllowed        = derControlRefusal{http.StatusBadRequest, "power_factor_not_allowed", "powerFactor: not allowed for this type"}
	refuseDisplacementReq     = derControlRefusal{http.StatusBadRequest, "displacement_required", "powerFactor.displacement: required"}
	refuseDisplacementRange   = derControlRefusal{http.StatusBadRequest, "displacement_range", "powerFactor.displacement: must be 1 to 1000"}
	refuseExcitationRequired  = derControlRefusal{http.StatusBadRequest, "excitation_required", "powerFactor.excitation: required"}
	refuseValueInvalid        = derControlRefusal{http.StatusBadRequest, "value_invalid", "value: invalid for this type"}
	refuseDurationRequired    = derControlRefusal{http.StatusBadRequest, "duration_required", "durationSeconds: required"}
	refuseDurationRange       = derControlRefusal{http.StatusBadRequest, "duration_range", "durationSeconds: out of range"}
	refuseStartRange          = derControlRefusal{http.StatusBadRequest, "start_range", "startTime: out of range"}
	refuseStartInPast         = derControlRefusal{http.StatusBadRequest, "start_in_past", "startTime: in the past"}
	refuseStartTooFarAhead    = derControlRefusal{http.StatusBadRequest, "start_too_far_ahead", "startTime: too far in the future"}
	refuseDescription         = derControlRefusal{http.StatusBadRequest, "description_invalid", "description: at most 32 octets"}
	refuseProgramNotFound     = derControlRefusal{http.StatusNotFound, "program_not_found", "derProgramHref: DERProgram not found"}
	refuseNoControlListLink   = derControlRefusal{http.StatusConflict, "no_der_control_list_link", "derProgramHref: DERProgram has no usable DERControlListLink"}
	refuseTargetWRequired     = derControlRefusal{http.StatusBadRequest, "target_w_required", "targetW: required for type targetW"}
	refuseTargetWNotAllowed   = derControlRefusal{http.StatusBadRequest, "target_w_not_allowed", "targetW: not allowed for this type"}
	refuseTargetWValueReq     = derControlRefusal{http.StatusBadRequest, "target_w_value_required", "targetW.value: required"}
	refuseTargetWValueRange   = derControlRefusal{http.StatusBadRequest, "target_w_value_range", "targetW.value: must be -32768 to 32767"}
	refuseTargetWMultRange    = derControlRefusal{http.StatusBadRequest, "target_w_multiplier_range", "targetW.multiplier: must be -9 to 9"}
	refuseExecutesGrantFormat = derControlRefusal{http.StatusBadRequest, "executes_grant_invalid", "executesGrant: invalid format"}
	refusePENNotConfigured    = derControlRefusal{http.StatusServiceUnavailable, "pen_not_configured", "server PEN not configured"}
	refuseInternal            = derControlRefusal{http.StatusInternalServerError, "internal_error", "internal error"}
	refuseClientGone          = derControlRefusal{http.StatusServiceUnavailable, "client_gone", "request cancelled"}
	refuseMRIDFormat          = derControlRefusal{http.StatusBadRequest, "mrid_invalid", "mrid: invalid format"}
	refuseReason              = derControlRefusal{http.StatusBadRequest, "reason_invalid", "reason: at most 192 octets"}
	refuseControlNotFound     = derControlRefusal{http.StatusNotFound, "control_not_found", "control not found"}
	refuseAlreadyCancelled    = derControlRefusal{http.StatusConflict, "already_cancelled", "control already cancelled"}
	refuseAlreadySuperseded   = derControlRefusal{http.StatusConflict, "already_superseded", "control already superseded"}
	refuseEnded               = derControlRefusal{http.StatusConflict, "ended", "control already ended"}
	refuseDeviceRequired      = derControlRefusal{http.StatusBadRequest, "device_required", "device: required"}
	refuseDeviceNotFound      = derControlRefusal{http.StatusNotFound, "device_not_found", "device not found"}
	refuseProgramOtherDevice  = derControlRefusal{http.StatusBadRequest, "program_href_other_device", "derProgramHref: not a program of this device"}
)

// issuerRefusals maps every dercontrol.RefusalCode to its wire refusal. A
// code missing here answers 500, so a new issuer refusal cannot reach the
// wire with an unreviewed message.
var issuerRefusals = map[dercontrol.RefusalCode]derControlRefusal{
	dercontrol.RefusalPENNotConfigured:   refusePENNotConfigured,
	dercontrol.RefusalInvalidProgramHref: refuseProgramHrefFormat,
	dercontrol.RefusalProgramNotFound:    refuseProgramNotFound,
	dercontrol.RefusalNoControlListLink:  refuseNoControlListLink,
	dercontrol.RefusalStartInPast:        refuseStartInPast,
	dercontrol.RefusalStartTooFarAhead:   refuseStartTooFarAhead,
	dercontrol.RefusalDurationOutOfRange: refuseDurationRange,
	dercontrol.RefusalUnknownType:        refuseTypeUnknown,
	dercontrol.RefusalMissingValue:       refuseValueInvalid,
	dercontrol.RefusalUnexpectedValue:    refuseValueInvalid,
	dercontrol.RefusalValueOutOfRange:    refuseValueInvalid,
	dercontrol.RefusalInvalidDescription: refuseDescription,
	dercontrol.RefusalControlNotFound:    refuseControlNotFound,
	dercontrol.RefusalAlreadyCancelled:   refuseAlreadyCancelled,
	dercontrol.RefusalAlreadySuperseded:  refuseAlreadySuperseded,
	dercontrol.RefusalEnded:              refuseEnded,
}

// conflictMessages maps each commitment.ConflictCode a create can meet to
// its fixed message. A code missing here answers 500, like an unmapped
// issuer refusal.
var conflictMessages = map[commitment.ConflictCode]string{
	commitment.ConflictFleetWindow:     "control overlaps a live flow reservation grant of its fleet",
	commitment.ConflictGrantNotLive:    "executesGrant: grant is not live",
	commitment.ConflictNotExecutable:   "executesGrant: grant has no interval, energy or power to execute",
	commitment.ConflictModeNotTarget:   "type: a control carrying out a grant must be targetW",
	commitment.ConflictOutsideFleet:    "executesGrant: grant belongs to another fleet",
	commitment.ConflictZeroDuration:    "durationSeconds: a control carrying out a grant needs a duration",
	commitment.ConflictOutsideInterval: "startTime, durationSeconds: outside the grant's interval",
	commitment.ConflictDirection:       "targetW: sign reverses the grant's direction",
	commitment.ConflictPower:           "targetW: exceeds the grant's powerAvailable",
	commitment.ConflictEnergy:          "targetW: exceeds the grant's energyAvailable",
}

// DERControlConflict is the 409 body of a create refused by a commitment.
// MRID names the grant or control it conflicts with.
type DERControlConflict struct {
	Error string `json:"error"`
	Code  string `json:"code"`
	MRID  string `json:"mRID"`
}

// derControlCreateBody is the POST /api/der/controls body. Every field is a
// pointer so "absent" and "zero" are told apart.
type derControlCreateBody struct {
	DERProgramHref  *string                `json:"derProgramHref"`
	Type            *string                `json:"type"`
	MaxLimW         *int64                 `json:"maxLimW"`
	PowerFactor     *derControlPowerFactor `json:"powerFactor"`
	TargetW         *derControlTargetW     `json:"targetW"`
	ExecutesGrant   *string                `json:"executesGrant"`
	StartTime       *int64                 `json:"startTime"`
	DurationSeconds *int64                 `json:"durationSeconds"`
	Description     *string                `json:"description"`
}

// derControlTargetW is opModTargetW. A missing multiplier is 0.
type derControlTargetW struct {
	Value      *int64 `json:"value"`
	Multiplier *int64 `json:"multiplier"`
}

type derControlPowerFactor struct {
	Displacement *int64 `json:"displacement"`
	Excitation   *bool  `json:"excitation"`
}

type derControlCancelBody struct {
	Reason *string `json:"reason"`
}

// DERControlBaseView is the JSON form of the DERControlBase values this API
// creates. Only the fields of the control's own type are present.
type DERControlBaseView struct {
	OpModConnect        *bool                 `json:"opModConnect,omitempty"`
	OpModEnergize       *bool                 `json:"opModEnergize,omitempty"`
	OpModMaxLimW        *uint16               `json:"opModMaxLimW,omitempty"`
	OpModFixedPFInjectW *FixedPowerFactorView `json:"opModFixedPFInjectW,omitempty"`
	OpModTargetW        *ActivePowerView      `json:"opModTargetW,omitempty"`
}

// ActivePowerView is opModTargetW: value x 10^multiplier watts, discharge
// positive.
type ActivePowerView struct {
	Value      int16 `json:"value"`
	Multiplier int8  `json:"multiplier"`
}

// FixedPowerFactorView is opModFixedPFInjectW.
type FixedPowerFactorView struct {
	Displacement uint16 `json:"displacement"`
	Excitation   bool   `json:"excitation"`
	Multiplier   int8   `json:"multiplier"`
}

// IntervalView is a control's interval, Unix seconds.
type IntervalView struct {
	Start    int64  `json:"start"`
	Duration uint32 `json:"duration"`
}

// EventStatusView is the derived EventStatus, with the status also named.
type EventStatusView struct {
	CurrentStatus uint8  `json:"currentStatus"`
	Status        string `json:"status"`
	DateTime      int64  `json:"dateTime"`
}

// DERControlView is one admin-issued control as the admin API reports it.
// Every href is the device-facing one, taken from the store.
type DERControlView struct {
	MRID               string             `json:"mRID"`
	Href               string             `json:"href"`
	DERProgramHref     string             `json:"derProgramHref"`
	DERControlListHref string             `json:"derControlListHref"`
	Type               string             `json:"type"`
	Description        string             `json:"description"`
	DERControlBase     DERControlBaseView `json:"derControlBase"`
	CreationTime       int64              `json:"creationTime"`
	Interval           IntervalView       `json:"interval"`
	EventStatus        EventStatusView    `json:"eventStatus"`
	ExecutesGrant      *string            `json:"executesGrant"`
}

// DERControlCreated is the 201 body of POST /api/der/controls. Supersedes
// is always an array, empty when nothing was superseded.
type DERControlCreated struct {
	DERControlView
	Supersedes            []string `json:"supersedes"`
	NotificationAttempted bool     `json:"notificationAttempted"`
	Persisted             bool     `json:"persisted"`
}

// DERControlResponseCounts counts the Response records a device posted for
// one control. ByStatus is keyed by the decimal ResponseStatus, or "absent"
// for a Response carrying no status.
type DERControlResponseCounts struct {
	Total    int            `json:"total"`
	ByStatus map[string]int `json:"byStatus"`
}

// DERControlListItem is one entry of GET /api/der/controls.
type DERControlListItem struct {
	DERControlView
	Responses DERControlResponseCounts `json:"responses"`
}

// DERControlList is the GET /api/der/controls body. Controls is always an
// array.
type DERControlList struct {
	Device   string               `json:"device"`
	Controls []DERControlListItem `json:"controls"`
}

// DERProgramView is one entry of GET /api/devices/{id}/der-programs.
// DERControlListHref is null when the program has no DERControlListLink.
type DERProgramView struct {
	Href               string  `json:"href"`
	MRID               string  `json:"mRID"`
	Description        string  `json:"description"`
	Primacy            uint8   `json:"primacy"`
	DERControlListHref *string `json:"derControlListHref"`
}

// DERProgramListView is the GET /api/devices/{id}/der-programs body.
type DERProgramListView struct {
	Device   string           `json:"device"`
	Programs []DERProgramView `json:"programs"`
}

// derControlLog accumulates the server-derived attributes of one request's
// audit line. Nothing a caller typed reaches it as text: strings are store
// values or fixed names, and request numbers are logged as numbers.
type derControlLog struct {
	attrs []any
}

func (l *derControlLog) add(key string, value any) {
	l.attrs = append(l.attrs, key, value)
}

func (l *derControlLog) addControl(scope dercontrol.Scope, ctrl sep2.DERControl) {
	l.add("mrid", ctrl.MRID)
	l.add("href", ctrl.Href)
	l.add("program_href", scope.ProgramHref())
}

// refuse answers ref and writes the request's one log line: WARN for a
// refusal, ERROR for a 5xx, whose cause l carries. op is "create" or
// "cancel".
func (h *AdminDERControlHandler) refuse(w http.ResponseWriter, r *http.Request, op string, ref derControlRefusal, l *derControlLog) {
	h.logRefusal(r, op, ref, l)
	writeError(w, ref.status, ref.message)
}

func (h *AdminDERControlHandler) logRefusal(r *http.Request, op string, ref derControlRefusal, l *derControlLog) {
	level, msg, event := slog.LevelWarn, "admin: DER control request refused", "der_control_"+op+"_refused"
	// A client that went away is not a server failure.
	if ref.status >= http.StatusInternalServerError && ref != refuseClientGone {
		level, msg, event = slog.LevelError, "admin: DER control request failed", "der_control_"+op+"_failed"
	}
	h.log(r, level, msg, event, append([]any{"code", ref.code, "status", ref.status}, l.attrs...))
}

// refuseConflict answers 409 for a create a commitment refused. The body
// carries the fixed message, the code and the conflicting mRID, which is a
// stored mRID or the validated executesGrant, never other request text.
func (h *AdminDERControlHandler) refuseConflict(w http.ResponseWriter, r *http.Request, conflict *commitment.ConflictError, l *derControlLog) {
	message, ok := conflictMessages[conflict.Code]
	if !ok {
		l.add("cause", "unmapped_conflict")
		l.add("conflict_code", string(conflict.Code))
		h.refuse(w, r, "create", refuseInternal, l)
		return
	}
	l.add("conflict_mrid", conflict.MRID)
	h.logRefusal(r, "create", derControlRefusal{http.StatusConflict, string(conflict.Code), message}, l)
	writeJSON(w, http.StatusConflict, DERControlConflict{Error: message, Code: string(conflict.Code), MRID: conflict.MRID})
}

// log writes one audit line carrying the fields every line shares.
func (h *AdminDERControlHandler) log(r *http.Request, level slog.Level, msg, event string, attrs []any) {
	base := []any{"event", event, "remote_addr", r.RemoteAddr, "admission", auth.AdmissionPath(r)}
	h.logger().Log(r.Context(), level, msg, append(base, attrs...)...)
}

// DERControlIncomplete is the 500 body when a write failed and could not be
// undone, so the named control may be stored and visible to devices.
type DERControlIncomplete struct {
	Error       string `json:"error"`
	MRID        string `json:"mRID"`
	Href        string `json:"href"`
	ControlKept bool   `json:"controlKept"`
}

// incomplete handles a *dercontrol.UndoError whose control may be live: the
// scope is notified, because devices can read the control, and the 500 body
// names it so the operator can find it and cancel it.
func (h *AdminDERControlHandler) incomplete(w http.ResponseWriter, r *http.Request, op, message string, undo *dercontrol.UndoError) {
	href := undo.Scope.ControlListHref() + "/" + undo.ID
	h.notify(r.Context(), undo.Scope)
	h.log(r, slog.LevelError, "admin: DER control write could not be undone", "der_control_"+op+"_incomplete", []any{
		"code", refuseInternal.code,
		"cause", "undo_incomplete",
		"mrid", undo.MRID,
		"href", href,
		"program_href", undo.Scope.ProgramHref(),
		"undo_step", string(undo.Step),
		"control_kept", undo.ControlKept,
		"lifecycle_kept", undo.LifecycleKept,
		"unreverted_ids", undo.UnrevertedIDs,
	})
	writeJSON(w, http.StatusInternalServerError, DERControlIncomplete{Error: message, MRID: undo.MRID, Href: href, ControlKept: undo.ControlKept})
}

// HandleCreate returns the handler for POST /api/der/controls.
func (h *AdminDERControlHandler) HandleCreate() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var logged derControlLog
		var body derControlCreateBody
		if ref, ok := decodeDERControlBody(w, r, &body, false); !ok {
			h.refuse(w, r, "create", ref, &logged)
			return
		}
		req, ref, ok := buildCreateRequest(body, &logged)
		if !ok {
			h.refuse(w, r, "create", ref, &logged)
			return
		}

		// Checks that read no store run first, so a bad request is refused
		// without resolving the fleet or waiting for its lock.
		if err := h.Issuer.Validate(req); err != nil {
			h.refuse(w, r, "create", mapIssuerError(err, &logged), &logged)
			return
		}
		res, err := h.issueInFleet(r.Context(), req)
		if err != nil {
			var conflict *commitment.ConflictError
			if errors.As(err, &conflict) {
				h.refuseConflict(w, r, conflict, &logged)
				return
			}
			var failure *commitmentFailure
			if errors.As(err, &failure) {
				// The href passed validProgramHref, so its segments are
				// plain ASCII and safe to log.
				edevID, _, _, _ := derhref.Program(req.DERProgramHref)
				logged.add("cause", "commitment_check_failed")
				logged.add("sub_cause", failure.sub)
				logged.add("device_id", edevID)
				logged.add("program_href", req.DERProgramHref)
				h.refuse(w, r, "create", refuseInternal, &logged)
				return
			}
			if isContextErr(err) && r.Context().Err() != nil {
				logged.add("cause", "client_gone")
				h.refuse(w, r, "create", refuseClientGone, &logged)
				return
			}
			var undo *dercontrol.UndoError
			if errors.As(err, &undo) && undo.ControlKept {
				h.incomplete(w, r, "create", "control may be live: its write could not be undone", undo)
				return
			}
			h.refuse(w, r, "create", mapIssuerError(err, &logged), &logged)
			return
		}

		h.notify(r.Context(), res.Scope)
		supersedes := res.Supersedes
		if supersedes == nil {
			supersedes = []string{}
		}
		h.logSuccess(r, "der_control_created", "admin: DER control created", res.Scope, res.Control, "supersedes", supersedes, "executes_grant", req.ExecutesGrant)

		now := sep2time.Now().Unix()
		w.Header().Set("Location", res.Href)
		writeJSON(w, http.StatusCreated, DERControlCreated{
			DERControlView:        newDERControlView(res.Scope, res.Control, dercontrol.LifecycleRecord{GrantMRID: req.ExecutesGrant}, now),
			Supersedes:            supersedes,
			NotificationAttempted: h.Notifier != nil,
			Persisted:             h.Persisted,
		})
	}
}

// issueInFleet runs the issuer inside the commitment lock of the program's
// fleet. The flow reservation gate takes the same lock for every grant, so
// no grant or other create on that fleet lands between the check and the
// write. The fleet key is resolved before the lock is taken, so a change of
// management pairs in between is not serialized with it.
//
// A *commitmentFailure means the check did not complete; a
// *commitment.ConflictError is a refusal; a context error means the client
// went away before anything was written.
func (h *AdminDERControlHandler) issueInFleet(ctx context.Context, req dercontrol.CreateRequest) (dercontrol.Result, error) {
	if h.Fleets == nil {
		return dercontrol.Result{}, &commitmentFailure{sub: "no_resolver"}
	}
	if h.Ledger == nil {
		return dercontrol.Result{}, &commitmentFailure{sub: "no_ledger"}
	}
	edevID, _, _, ok := derhref.Program(req.DERProgramHref)
	if !ok {
		return dercontrol.Result{}, &dercontrol.RefusalError{Code: dercontrol.RefusalInvalidProgramHref}
	}
	fleetKey, err := h.Fleets.FleetOf(ctx, edevID)
	switch {
	case err == nil:
	case errors.Is(err, store.ErrNotFound):
		// A program cannot be stored under an EndDevice that does not
		// exist, so this is the issuer's own 404.
		return dercontrol.Result{}, &dercontrol.RefusalError{Code: dercontrol.RefusalProgramNotFound}
	case errors.Is(err, commitment.ErrNoLFDI):
		return dercontrol.Result{}, &commitmentFailure{sub: "device_without_lfdi", err: err}
	default:
		return dercontrol.Result{}, &commitmentFailure{sub: "fleet_resolve_failed", err: err}
	}

	var res dercontrol.Result
	ran := false
	err = h.Ledger.Within(ctx, []string{fleetKey}, func(v commitment.View) error {
		ran = true
		var err error
		res, err = h.Issuer.IssueInFleet(ctx, req, dercontrol.Fleet{Key: fleetKey, Reach: controlReach, Check: checkIn(v)})
		return err
	})
	if ran {
		return res, err
	}
	switch {
	case err == nil:
		// Nothing was issued, so there is no control to report as created.
		return dercontrol.Result{}, &commitmentFailure{sub: "ledger_did_not_run"}
	case errors.Is(err, commitment.ErrNoLedger):
		return dercontrol.Result{}, &commitmentFailure{sub: "no_ledger", err: err}
	case isContextErr(err):
		return dercontrol.Result{}, err
	default:
		return dercontrol.Result{}, &commitmentFailure{sub: "ledger_failed", err: err}
	}
}

// cancelInFleet cancels the control under the fleet lock, the lock a client's
// cancel of the grant it executes (Ledger.CancelGrant) also holds, so the two
// cannot interleave and a failed write's restore cannot undo the other's
// cancel. A resolver or ledger that is missing, or a fleet that cannot be
// resolved, refuses rather than cancelling unserialized.
func (h *AdminDERControlHandler) cancelInFleet(ctx context.Context, scope dercontrol.Scope, id, reason string) (dercontrol.LifecycleRecord, error) {
	if h.Fleets == nil {
		return dercontrol.LifecycleRecord{}, &commitmentFailure{sub: "no_resolver"}
	}
	if h.Ledger == nil {
		return dercontrol.LifecycleRecord{}, &commitmentFailure{sub: "no_ledger"}
	}
	fleetKey, err := h.Fleets.FleetOf(ctx, scope.EndDeviceID)
	switch {
	case err == nil:
	case errors.Is(err, store.ErrNotFound):
		return dercontrol.LifecycleRecord{}, &dercontrol.RefusalError{Code: dercontrol.RefusalControlNotFound}
	default:
		return dercontrol.LifecycleRecord{}, &commitmentFailure{sub: "fleet_resolve_failed", err: err}
	}

	var lc dercontrol.LifecycleRecord
	ran := false
	err = h.Ledger.Within(ctx, []string{fleetKey}, func(commitment.View) error {
		ran = true
		var err error
		lc, err = h.Issuer.Cancel(ctx, scope, id, reason)
		return err
	})
	switch {
	case ran:
		return lc, err
	case err == nil:
		return dercontrol.LifecycleRecord{}, &commitmentFailure{sub: "ledger_did_not_run"}
	case isContextErr(err):
		return dercontrol.LifecycleRecord{}, err
	default:
		return dercontrol.LifecycleRecord{}, &commitmentFailure{sub: "ledger_failed", err: err}
	}
}

// checkIn adapts a ledger View to the issuer's check hook.
func checkIn(v commitment.View) dercontrol.Check {
	return func(ctx context.Context, p dercontrol.Proposal) error {
		err := v.CheckControl(ctx, commitment.Proposal{
			FleetKey:   p.FleetKey,
			Window:     p.Window,
			GrantMRID:  p.GrantMRID,
			TargetW:    p.TargetW,
			Reach:      p.Reach,
			Supersedes: p.Supersedes,
		})
		var conflict *commitment.ConflictError
		if err == nil || errors.As(err, &conflict) || isContextErr(err) {
			return err
		}
		return &commitmentFailure{sub: "store_read_failed", err: err}
	}
}

func isContextErr(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

// logSuccess writes the audit line for a completed create or cancel.
func (h *AdminDERControlHandler) logSuccess(r *http.Request, event, msg string, scope dercontrol.Scope, ctrl sep2.DERControl, extra ...any) {
	var l derControlLog
	l.addControl(scope, ctrl)
	addBaseToLog(&l, ctrl)
	h.log(r, slog.LevelInfo, msg, event, append(l.attrs, extra...))
}

// HandleCancel returns the handler for POST /api/der/controls/{mrid}/cancel.
func (h *AdminDERControlHandler) HandleCancel() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var logged derControlLog
		var body derControlCancelBody
		if ref, ok := decodeDERControlBody(w, r, &body, true); !ok {
			h.refuse(w, r, "cancel", ref, &logged)
			return
		}
		reason := ""
		if body.Reason != nil {
			reason = *body.Reason
		}
		if !validString(reason, maxCancelReasonOctets) {
			h.refuse(w, r, "cancel", refuseReason, &logged)
			return
		}
		mrid, ok := normalizeMRID(r.PathValue("mrid"))
		if !ok {
			h.refuse(w, r, "cancel", refuseMRIDFormat, &logged)
			return
		}

		// A stored Event is never edited (IEEE 2030.5-2018 line 5470), so
		// the copy read here is the control Cancel acts on, and the view is
		// built from it rather than from a second read that could fail
		// after the cancellation is committed.
		parentID, id, ctrl, err := h.Controls.ByMRID(r.Context(), mrid)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				h.refuse(w, r, "cancel", refuseControlNotFound, &logged)
				return
			}
			logged.add("cause", "store_error")
			h.refuse(w, r, "cancel", refuseInternal, &logged)
			return
		}
		scope, ok := dercontrol.ScopeFromKey(parentID)
		if !ok {
			// Indexed under a key the issuer never builds: not an
			// admin-issued control.
			h.refuse(w, r, "cancel", refuseControlNotFound, &logged)
			return
		}
		logged.addControl(scope, ctrl)

		lc, err := h.cancelInFleet(r.Context(), scope, id, reason)
		if err != nil {
			var undo *dercontrol.UndoError
			if errors.As(err, &undo) {
				if undo.MRID == "" {
					undo.Scope, undo.MRID = scope, ctrl.MRID
				}
				h.incomplete(w, r, "cancel", "cancellation may be recorded: its write could not be undone", undo)
				return
			}
			h.refuse(w, r, "cancel", mapIssuerError(err, &logged), &logged)
			return
		}

		h.notify(r.Context(), scope)
		h.logSuccess(r, "der_control_cancelled", "admin: DER control cancelled", scope, ctrl)
		writeJSON(w, http.StatusOK, newDERControlView(scope, ctrl, lc, sep2time.Now().Unix()))
	}
}

// HandleList returns the handler for GET /api/der/controls.
func (h *AdminDERControlHandler) HandleList() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		deviceID := r.URL.Query().Get("device")
		if deviceID == "" {
			writeError(w, refuseDeviceRequired.status, refuseDeviceRequired.message)
			return
		}
		edev, ok := h.loadDevice(w, r, deviceID)
		if !ok {
			return
		}

		wantDerp := ""
		if href := r.URL.Query().Get("derProgramHref"); href != "" {
			progEdev, _, derp, ok := derhref.Program(href)
			if !ok || !validProgramHref(href) {
				writeError(w, refuseProgramHrefFormat.status, refuseProgramHrefFormat.message)
				return
			}
			if progEdev != deviceID {
				writeError(w, refuseProgramOtherDevice.status, refuseProgramOtherDevice.message)
				return
			}
			wantDerp = derp
		}

		counts, err := h.responseCounts(ctx, edev.LFDI)
		if err != nil {
			h.internal(w, r, "list responses")
			return
		}

		parents, err := h.Controls.Parents(ctx)
		if err != nil {
			h.internal(w, r, "list control scopes")
			return
		}
		now := sep2time.Now().Unix()
		items := []DERControlListItem{}
		for _, key := range parents {
			scope, ok := dercontrol.ScopeFromKey(key)
			if !ok || scope.EndDeviceID != deviceID || (wantDerp != "" && scope.DERProgramID != wantDerp) {
				continue
			}
			page, err := h.Controls.List(ctx, key, store.ListOptions{Unbounded: true})
			if err != nil {
				h.internal(w, r, "list controls")
				return
			}
			for _, ctrl := range page.Items {
				id, ok := derhref.ControlID(ctrl.Href)
				if !ok {
					continue
				}
				lc, err := h.Lifecycles.Get(ctx, key, id)
				if err != nil {
					if errors.Is(err, store.ErrNotFound) {
						continue // not issued through this API
					}
					h.internal(w, r, "load lifecycle")
					return
				}
				items = append(items, DERControlListItem{
					DERControlView: newDERControlView(scope, ctrl, lc, now),
					Responses:      counts.forMRID(ctrl.MRID),
				})
			}
		}
		writeJSON(w, http.StatusOK, DERControlList{Device: deviceID, Controls: items})
	}
}

// HandleListPrograms returns the handler for GET /api/devices/{id}/der-programs.
// Programs are ordered primacy ascending, then mRID descending, the order a
// device ranks programs of equal primacy by.
func (h *AdminDERControlHandler) HandleListPrograms() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		deviceID := r.PathValue("id")
		if _, ok := h.loadDevice(w, r, deviceID); !ok {
			return
		}
		page, err := h.Programs.List(r.Context(), deviceID, store.ListOptions{Unbounded: true})
		if err != nil {
			h.internal(w, r, "list programs")
			return
		}
		programs := make([]DERProgramView, 0, len(page.Items))
		for _, p := range page.Items {
			v := DERProgramView{Href: p.Href, MRID: p.MRID, Description: p.Description, Primacy: p.Primacy}
			if p.DERControlListLink != nil {
				href := p.DERControlListLink.Href
				v.DERControlListHref = &href
			}
			programs = append(programs, v)
		}
		slices.SortStableFunc(programs, func(a, b DERProgramView) int {
			if c := cmp.Compare(a.Primacy, b.Primacy); c != 0 {
				return c
			}
			return cmp.Compare(b.MRID, a.MRID)
		})
		writeJSON(w, http.StatusOK, DERProgramListView{Device: deviceID, Programs: programs})
	}
}

func (h *AdminDERControlHandler) loadDevice(w http.ResponseWriter, r *http.Request, id string) (sep2.EndDevice, bool) {
	edev, err := h.EndDevices.Get(r.Context(), id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, refuseDeviceNotFound.status, refuseDeviceNotFound.message)
			return sep2.EndDevice{}, false
		}
		h.internal(w, r, "load device")
		return sep2.EndDevice{}, false
	}
	return edev, true
}

// internal answers 500 for a read route. step is a fixed string naming the
// failed operation; the store's error text is not logged because it may
// carry a request-derived id.
func (h *AdminDERControlHandler) internal(w http.ResponseWriter, r *http.Request, step string) {
	h.logger().Error("admin: DER control read failed",
		"event", "der_control_read_failed",
		"step", step,
		"remote_addr", r.RemoteAddr,
	)
	writeError(w, refuseInternal.status, refuseInternal.message)
}

// notify fans a Changed notification out to the program list and the
// control list the control is stored under, both taken from the resolved
// scope rather than the request.
func (h *AdminDERControlHandler) notify(ctx context.Context, scope dercontrol.Scope) {
	if h.Notifier == nil {
		return
	}
	h.Notifier.Notify(ctx, scope.ProgramListHref(), sep2.NotificationStatusChanged)
	h.Notifier.Notify(ctx, scope.ControlListHref(), sep2.NotificationStatusChanged)
}

// mapIssuerError turns an Issue or Cancel error into its wire refusal, and
// records the cause of a 500 on l. Error text is not logged: a store's
// diagnostic may carry a request-derived id.
func mapIssuerError(err error, l *derControlLog) derControlRefusal {
	var refusal *dercontrol.RefusalError
	if errors.As(err, &refusal) {
		if ref, ok := issuerRefusals[refusal.Code]; ok {
			return ref
		}
		l.add("cause", "unmapped_refusal")
		l.add("issuer_code", string(refusal.Code))
		return refuseInternal
	}
	var undo *dercontrol.UndoError
	if errors.As(err, &undo) {
		l.add("cause", "undo_incomplete")
		l.add("undo_step", string(undo.Step))
		l.add("control_kept", undo.ControlKept)
		l.add("lifecycle_kept", undo.LifecycleKept)
		l.add("store_id", undo.ID)
		return refuseInternal
	}
	var cf *commitmentFailure
	if errors.As(err, &cf) {
		l.add("cause", "commitment_unavailable")
		l.add("commitment", cf.sub)
		return refuseInternal
	}
	l.add("cause", "store_error")
	return refuseInternal
}

// decodeDERControlBody decodes one JSON object of at most derControlMaxBody
// bytes into dst, refusing unknown fields and trailing data. allowEmpty
// accepts an empty body as the zero value.
func decodeDERControlBody(w http.ResponseWriter, r *http.Request, dst any, allowEmpty bool) (derControlRefusal, bool) {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, derControlMaxBody))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		if allowEmpty && errors.Is(err, io.EOF) {
			return derControlRefusal{}, true
		}
		return decodeRefusal(err), false
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			return refuseBodyTooLarge, false
		}
		return refuseInvalidJSON, false
	}
	return derControlRefusal{}, true
}

// decodeRefusal classifies a decode error without echoing it: a JSON error
// names the offending field or input, which is request text.
func decodeRefusal(err error) derControlRefusal {
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		return refuseBodyTooLarge
	}
	var typeErr *json.UnmarshalTypeError
	if errors.As(err, &typeErr) {
		if ref, ok := wrongTypeRefusals[typeErr.Field]; ok {
			return ref
		}
		return refuseInvalidJSON
	}
	if strings.HasPrefix(err.Error(), "json: unknown field ") {
		return refuseUnknownField
	}
	return refuseInvalidJSON
}

// wrongTypeRefusals names each decodable field, so a type error reports the
// field from this fixed table and never from the decoder's own text.
var wrongTypeRefusals = map[string]derControlRefusal{
	"derProgramHref":           {http.StatusBadRequest, "program_href_type", "derProgramHref: must be a string"},
	"type":                     {http.StatusBadRequest, "type_type", "type: must be a string"},
	"maxLimW":                  {http.StatusBadRequest, "max_lim_w_type", "maxLimW: must be an integer"},
	"powerFactor":              {http.StatusBadRequest, "power_factor_type", "powerFactor: must be an object"},
	"powerFactor.displacement": {http.StatusBadRequest, "displacement_type", "powerFactor.displacement: must be an integer"},
	"powerFactor.excitation":   {http.StatusBadRequest, "excitation_type", "powerFactor.excitation: must be a boolean"},
	"startTime":                {http.StatusBadRequest, "start_type", "startTime: must be an integer"},
	"durationSeconds":          {http.StatusBadRequest, "duration_type", "durationSeconds: must be an integer"},
	"description":              {http.StatusBadRequest, "description_type", "description: must be a string"},
	"targetW":                  {http.StatusBadRequest, "target_w_type", "targetW: must be an object"},
	"targetW.value":            {http.StatusBadRequest, "target_w_value_type", "targetW.value: must be an integer"},
	"targetW.multiplier":       {http.StatusBadRequest, "target_w_multiplier_type", "targetW.multiplier: must be an integer"},
	"executesGrant":            {http.StatusBadRequest, "executes_grant_type", "executesGrant: must be a string"},
	"reason":                   {http.StatusBadRequest, "reason_type", "reason: must be a string"},
}

// controlTypes is the closed set of request types, keyed by their wire name.
var controlTypes = map[string]dercontrol.ControlType{
	string(dercontrol.Connect):        dercontrol.Connect,
	string(dercontrol.Disconnect):     dercontrol.Disconnect,
	string(dercontrol.MaxLimW):        dercontrol.MaxLimW,
	string(dercontrol.FixedPFInjectW): dercontrol.FixedPFInjectW,
	string(dercontrol.TargetW):        dercontrol.TargetW,
}

// buildCreateRequest validates body field by field so each refusal names
// its field, and records the validated values on l as it goes.
func buildCreateRequest(body derControlCreateBody, l *derControlLog) (dercontrol.CreateRequest, derControlRefusal, bool) {
	var req dercontrol.CreateRequest
	if body.DERProgramHref == nil || *body.DERProgramHref == "" {
		return req, refuseProgramHrefRequired, false
	}
	if !validProgramHref(*body.DERProgramHref) {
		return req, refuseProgramHrefFormat, false
	}
	req.DERProgramHref = *body.DERProgramHref

	if body.Type == nil || *body.Type == "" {
		return req, refuseTypeRequired, false
	}
	typ, ok := controlTypes[*body.Type]
	if !ok {
		return req, refuseTypeUnknown, false
	}
	req.Type = typ
	l.add("type", string(typ))

	if ref, ok := buildValue(typ, body, &req, l); !ok {
		return req, ref, false
	}

	if body.ExecutesGrant != nil {
		grant, ok := normalizeMRID(*body.ExecutesGrant)
		if !ok {
			return req, refuseExecutesGrantFormat, false
		}
		req.ExecutesGrant = grant
	}

	if body.DurationSeconds == nil {
		return req, refuseDurationRequired, false
	}
	if *body.DurationSeconds <= 0 || *body.DurationSeconds > int64(^uint32(0)) {
		return req, refuseDurationRange, false
	}
	req.DurationSeconds = uint32(*body.DurationSeconds)
	l.add("duration", req.DurationSeconds)

	if body.StartTime != nil {
		if *body.StartTime < 0 {
			return req, refuseStartRange, false
		}
		start := *body.StartTime
		req.Start = &start
		l.add("start", start)
	}

	if body.Description != nil {
		d := *body.Description
		if !validString(d, maxDescriptionOctets) {
			return req, refuseDescription, false
		}
		req.Description = d
	}
	return req, derControlRefusal{}, true
}

func buildValue(typ dercontrol.ControlType, body derControlCreateBody, req *dercontrol.CreateRequest, l *derControlLog) (derControlRefusal, bool) {
	if typ != dercontrol.TargetW && body.TargetW != nil {
		return refuseTargetWNotAllowed, false
	}
	switch typ {
	case dercontrol.MaxLimW:
		if body.PowerFactor != nil {
			return refusePFNotAllowed, false
		}
		if body.MaxLimW == nil {
			return refuseMaxLimWRequired, false
		}
		if *body.MaxLimW < 0 || *body.MaxLimW > 10000 {
			return refuseMaxLimWRange, false
		}
		v := uint16(*body.MaxLimW)
		req.MaxLimW = &v
		l.add("value", v)
	case dercontrol.FixedPFInjectW:
		if body.MaxLimW != nil {
			return refuseMaxLimWNotAllowed, false
		}
		if body.PowerFactor == nil {
			return refusePFRequired, false
		}
		pf := body.PowerFactor
		if pf.Displacement == nil {
			return refuseDisplacementReq, false
		}
		if pf.Excitation == nil {
			return refuseExcitationRequired, false
		}
		if *pf.Displacement < 1 || *pf.Displacement > 1000 {
			return refuseDisplacementRange, false
		}
		excitation := *pf.Excitation
		req.PowerFactor = &dercontrol.PowerFactorValue{Displacement: uint16(*pf.Displacement), Excitation: &excitation}
		l.add("value", uint16(*pf.Displacement))
		l.add("excitation", excitation)
	case dercontrol.TargetW:
		if body.MaxLimW != nil {
			return refuseMaxLimWNotAllowed, false
		}
		if body.PowerFactor != nil {
			return refusePFNotAllowed, false
		}
		tw := body.TargetW
		if tw == nil {
			return refuseTargetWRequired, false
		}
		if tw.Value == nil {
			return refuseTargetWValueReq, false
		}
		if *tw.Value < math.MinInt16 || *tw.Value > math.MaxInt16 {
			return refuseTargetWValueRange, false
		}
		var mult int64
		if tw.Multiplier != nil {
			mult = *tw.Multiplier
		}
		if mult < -9 || mult > 9 {
			return refuseTargetWMultRange, false
		}
		req.TargetW = &sep2.ActivePower{Value: int16(*tw.Value), Multiplier: int8(mult)}
		l.add("value", req.TargetW.Value)
		l.add("multiplier", req.TargetW.Multiplier)
	default: // Connect and Disconnect carry no value.
		if body.MaxLimW != nil {
			return refuseMaxLimWNotAllowed, false
		}
		if body.PowerFactor != nil {
			return refusePFNotAllowed, false
		}
	}
	return derControlRefusal{}, true
}

// addBaseToLog records a stored control's type, value and interval.
func addBaseToLog(l *derControlLog, ctrl sep2.DERControl) {
	l.add("type", controlTypeName(ctrl.DERControlBase))
	if b := ctrl.DERControlBase; b != nil {
		switch {
		case b.OpModMaxLimW != nil:
			l.add("value", uint16(*b.OpModMaxLimW))
		case b.OpModFixedPFInjectW != nil:
			l.add("value", b.OpModFixedPFInjectW.Displacement)
			l.add("excitation", b.OpModFixedPFInjectW.Excitation)
		case b.OpModTargetW != nil:
			l.add("value", b.OpModTargetW.Value)
			l.add("multiplier", b.OpModTargetW.Multiplier)
		}
	}
	if ctrl.Interval != nil {
		l.add("start", ctrl.Interval.Start)
		l.add("duration", ctrl.Interval.Duration)
	}
}

// validProgramHref reports whether href has the DERProgram shape and every
// id segment is plain ASCII letters, digits, '.', '_' or '-'. The fsa
// segment is otherwise ignored: the issuer stores the control under the fsa
// the program's own DERControlListLink names.
func validProgramHref(href string) bool {
	edev, fsa, derp, ok := derhref.Program(href)
	if !ok || strings.TrimSpace(href) != href {
		return false
	}
	for _, seg := range []string{edev, fsa, derp} {
		for i := 0; i < len(seg); i++ {
			c := seg[i]
			if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c == '.' || c == '_' || c == '-') {
				return false
			}
		}
	}
	return true
}

// normalizeMRID accepts exactly 32 hex digits, either case, and returns them
// uppercase, the form the issuer mints.
func normalizeMRID(s string) (string, bool) {
	if len(s) != 32 {
		return "", false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F') {
			return "", false
		}
	}
	return strings.ToUpper(s), true
}

func newDERControlView(scope dercontrol.Scope, ctrl sep2.DERControl, lc dercontrol.LifecycleRecord, now int64) DERControlView {
	v := DERControlView{
		MRID:               ctrl.MRID,
		Href:               ctrl.Href,
		DERProgramHref:     scope.ProgramHref(),
		DERControlListHref: scope.ControlListHref(),
		Type:               controlTypeName(ctrl.DERControlBase),
		Description:        ctrl.Description,
		CreationTime:       ctrl.CreationTime,
	}
	if b := ctrl.DERControlBase; b != nil {
		v.DERControlBase = DERControlBaseView{OpModConnect: b.OpModConnect, OpModEnergize: b.OpModEnergize}
		if b.OpModMaxLimW != nil {
			m := uint16(*b.OpModMaxLimW)
			v.DERControlBase.OpModMaxLimW = &m
		}
		if pf := b.OpModFixedPFInjectW; pf != nil {
			v.DERControlBase.OpModFixedPFInjectW = &FixedPowerFactorView{Displacement: pf.Displacement, Excitation: pf.Excitation, Multiplier: pf.Multiplier}
		}
		if tw := b.OpModTargetW; tw != nil {
			v.DERControlBase.OpModTargetW = &ActivePowerView{Value: tw.Value, Multiplier: tw.Multiplier}
		}
	}
	if lc.GrantMRID != "" {
		grant := lc.GrantMRID
		v.ExecutesGrant = &grant
	}
	if ctrl.Interval != nil {
		v.Interval = IntervalView{Start: ctrl.Interval.Start, Duration: ctrl.Interval.Duration}
		st := dercontrol.DeriveStatus(now, ctrl.CreationTime, ctrl.Interval.Start, lc)
		v.EventStatus = EventStatusView{CurrentStatus: st.CurrentStatus, Status: eventStatusName(st.CurrentStatus), DateTime: st.DateTime}
	}
	return v
}

// controlTypeName names a stored control's type by the request type that
// builds that DERControlBase shape.
func controlTypeName(b *sep2.DERControlBase) string {
	switch {
	case b == nil:
		return ""
	case b.OpModConnect != nil && *b.OpModConnect:
		return string(dercontrol.Connect)
	case b.OpModConnect != nil:
		return string(dercontrol.Disconnect)
	case b.OpModMaxLimW != nil:
		return string(dercontrol.MaxLimW)
	case b.OpModFixedPFInjectW != nil:
		return string(dercontrol.FixedPFInjectW)
	case b.OpModTargetW != nil:
		return string(dercontrol.TargetW)
	default:
		return "other"
	}
}

func eventStatusName(s uint8) string {
	switch s {
	case sep2.EventStatusScheduled:
		return "scheduled"
	case sep2.EventStatusActive:
		return "active"
	case sep2.EventStatusCancelled:
		return "cancelled"
	case sep2.EventStatusSuperseded:
		return "superseded"
	default:
		return "unknown"
	}
}

// responseCountIndex holds Response counts for one device, keyed by the
// Response's subject mRID.
type responseCountIndex map[string]DERControlResponseCounts

func (idx responseCountIndex) forMRID(mrid string) DERControlResponseCounts {
	if c, ok := idx[mrid]; ok {
		return c
	}
	return DERControlResponseCounts{ByStatus: map[string]int{}}
}

// responseCounts reads every ResponseSet once and counts the Responses whose
// endDeviceLFDI matches lfdi. LFDIs compare case-insensitively: hexBinary
// allows either case and devices differ in which they send.
func (h *AdminDERControlHandler) responseCounts(ctx context.Context, lfdi string) (responseCountIndex, error) {
	idx := responseCountIndex{}
	if h.Responses == nil || lfdi == "" {
		return idx, nil
	}
	sets, err := h.Responses.Parents(ctx)
	if err != nil {
		return nil, err
	}
	for _, set := range sets {
		page, err := h.Responses.List(ctx, set, store.ListOptions{Unbounded: true})
		if err != nil {
			return nil, err
		}
		for _, rsp := range page.Items {
			if rsp.Subject == "" || !strings.EqualFold(rsp.EndDeviceLFDI, lfdi) {
				continue
			}
			key := "absent"
			if rsp.Status != nil {
				key = strconv.Itoa(int(*rsp.Status))
			}
			c := idx.forMRID(strings.ToUpper(rsp.Subject))
			c.Total++
			c.ByStatus[key]++
			idx[strings.ToUpper(rsp.Subject)] = c
		}
	}
	return idx, nil
}
