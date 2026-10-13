package handler

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/auth"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/commitment"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/flowreservation"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
)

// #670: admin write routes for flow reservations.
//
//	POST /api/derms/flow-reservations/{edevId}/{frqId}/answer   201
//	POST /api/derms/flow-reservations/{edevId}/{frqId}/revise   201
//	POST /api/derms/flow-reservations/{edevId}/{frqId}/cancel   200
//
// Each replies with the request's FlowReservationView after the write. A
// refusal stores nothing and answers with frRefusal.

// frWriteMaxBody bounds a write body. Larger bodies are refused, not
// truncated.
const frWriteMaxBody = 64 << 10

// The operator's reason is a String192 (IEEE 2030.5 EventStatus reason): at
// most 192 characters, and a string of multi-byte characters is reduced so it
// is stored in 192 octets. The page counts in code points, so the server
// refuses only above 192 of those and stores at most 192 octets of UTF-8,
// the bound every CancelReason it reaches is held to.
const (
	maxFRReasonRunes  = 192
	maxFRReasonOctets = 192
)

// PowerOfTenMultiplierType: a client need support only -9..9.
const maxFRMultiplier = 9

// FlowReservationAnswerer answers a pending request; *flowreservation.Queue
// is the production one.
type FlowReservationAnswerer interface {
	Answer(ctx context.Context, edevID, frqID string, decision flowreservation.Decision) (sep2.FlowReservationResponse, error)
}

// FlowReservationGrantCanceller cancels every live grant of a request's
// answer; *flowreservation.Canceller is the production one.
type FlowReservationGrantCanceller interface {
	CancelGrants(ctx context.Context, edevID, frqID, reason string) (cancelled []flowreservation.CancelledGrant, err error)
}

// FlowReservationCancelRecorder records who cancelled a response.
type FlowReservationCancelRecorder interface {
	RecordCancel(ctx context.Context, edevID, responseID string, by flowreservation.Attribution) error
}

// frRefusalKind is one row of the refusal table: status, code and the fixed
// text the body carries.
type frRefusalKind struct {
	status int
	code   string
	text   string
}

var (
	frBodyFormat         = frRefusalKind{http.StatusBadRequest, "body_format", "the body must be one JSON object of the documented shape"}
	frUnknownField       = frRefusalKind{http.StatusBadRequest, "unknown_field", "the body has a field this route does not read"}
	frDecisionInvalid    = frRefusalKind{http.StatusBadRequest, "decision_invalid", "decision must be grant or deny"}
	frValueNegative      = frRefusalKind{http.StatusBadRequest, "value_negative", "energy and power are magnitudes and must be at least 0"}
	frReasonTooLong      = frRefusalKind{http.StatusBadRequest, "reason_too_long", "reason is at most 192 characters"}
	frReasonNotAllowed   = frRefusalKind{http.StatusBadRequest, "reason_not_allowed", "reason is accepted only when revising or cancelling"}
	frReasonInvalid      = frRefusalKind{http.StatusBadRequest, "reason_invalid", "reason may not hold control or bidirectional override characters"}
	frMultiplierRange    = frRefusalKind{http.StatusBadRequest, "multiplier_out_of_range", "a multiplier must be between -9 and 9"}
	frOutsideWindow      = frRefusalKind{http.StatusBadRequest, "interval_outside_window", "the interval is outside the requested window"}
	frNoWindow           = frRefusalKind{http.StatusBadRequest, "no_requested_window", "the request named no window to grant inside"}
	frNoEnergy           = frRefusalKind{http.StatusBadRequest, "no_requested_energy", "the request named no energy"}
	frNoPower            = frRefusalKind{http.StatusBadRequest, "no_requested_power", "the request named no power"}
	frEnergyExceeds      = frRefusalKind{http.StatusBadRequest, "energy_exceeds_request", "the energy exceeds the requested magnitude"}
	frPowerExceeds       = frRefusalKind{http.StatusBadRequest, "power_exceeds_request", "the power exceeds the requested magnitude"}
	frZeroDuration       = frRefusalKind{http.StatusBadRequest, "grant_zero_duration", "a grant cannot last zero seconds; deny the request instead"}
	frNotFound           = frRefusalKind{http.StatusNotFound, "request_not_found", "no such flow reservation request"}
	frAlreadyAnswered    = frRefusalKind{http.StatusConflict, "already_answered", "the request already has an answer"}
	frRequestCancelled   = frRefusalKind{http.StatusConflict, "request_cancelled", "the client cancelled the request"}
	frNotAnswered        = frRefusalKind{http.StatusConflict, "not_answered", "the request has no answer yet; deny it with answer"}
	frGrantNotLive       = frRefusalKind{http.StatusConflict, "grant_not_live", "the answer is not a live grant: it is a denial, cancelled or ended"}
	frEarlierAnswerLive  = frRefusalKind{http.StatusConflict, string(commitment.ConflictFleetWindow), "an earlier answer to this request is still live after an incomplete revision; cancel the request's grant to clear it"}
	frGrantUnresolved    = frRefusalKind{http.StatusConflict, "grant_unresolved", "the grant's EndDevice belongs to no fleet, so its grant cannot be changed; give the device its LFDI back and repeat, or delete the EndDevice"}
	frChainMoving        = frRefusalKind{http.StatusConflict, "chain_moving", "the request's answer kept changing during the cancel and nothing was cancelled; repeat the cancel"}
	frCancelPartial      = frRefusalKind{http.StatusInternalServerError, "cancel_partial", "the cancel stopped part way: the grants named in cancelled are cancelled, the rest are not; read the request before acting again"}
	frViewAfterWrite     = frRefusalKind{http.StatusInternalServerError, "view_after_write", "the write was committed but the request could not be read back; read it again before acting"}
	frInternal           = frRefusalKind{http.StatusInternalServerError, "internal", "flow reservation write failed, see server log"}
	frNotConfigured      = frRefusalKind{http.StatusServiceUnavailable, "not_configured", "flow reservation answers are not configured on this server"}
	frGenericConflictMsg = "the change conflicts with the fleet's commitments"
)

// frConflictTexts are the fixed texts of the 409s the commitment ledger
// returns, by code. A code not listed carries frGenericConflictMsg.
var frConflictTexts = map[commitment.ConflictCode]string{
	commitment.ConflictFleetWindow:     "the window overlaps a live grant or plain control of the fleet",
	commitment.ConflictGrantNotLive:    frGrantNotLive.text,
	commitment.ConflictOutsideInterval: "a control executing the grant falls outside the new interval",
	commitment.ConflictPower:           "a control executing the grant exceeds the new power",
	commitment.ConflictEnergy:          "the controls executing the grant exceed the new energy",
	commitment.ConflictDirection:       "a control executing the grant runs against the new direction",
}

// frDecisionErrors maps the answer builder's refusals to the table.
var frDecisionErrors = []struct {
	err  error
	kind frRefusalKind
}{
	{flowreservation.ErrIntervalOutsideWindow, frOutsideWindow},
	{flowreservation.ErrNoRequestedWindow, frNoWindow},
	{flowreservation.ErrNoRequestedEnergy, frNoEnergy},
	{flowreservation.ErrNoRequestedPower, frNoPower},
	{flowreservation.ErrEnergyExceedsRequest, frEnergyExceeds},
	{flowreservation.ErrPowerExceedsRequest, frPowerExceeds},
	// The route applies the request's own sign, so a reversal is reachable
	// only against a requested zero, which any positive value exceeds.
	{flowreservation.ErrEnergyReversesRequest, frEnergyExceeds},
	{flowreservation.ErrPowerReversesRequest, frPowerExceeds},
	{flowreservation.ErrGrantZeroDuration, frZeroDuration},
}

type frEnergyBody struct {
	Value      int64 `json:"value"`
	Multiplier int8  `json:"multiplier"`
}

type frPowerBody struct {
	Value      int16 `json:"value"`
	Multiplier int8  `json:"multiplier"`
}

// frWriteBody is the answer and revise body. Absent interval, energy or
// power grant what was asked.
type frWriteBody struct {
	Decision *string       `json:"decision"`
	Interval *frInterval   `json:"interval"`
	Energy   *frEnergyBody `json:"energy"`
	Power    *frPowerBody  `json:"power"`
	Reason   *string       `json:"reason"`
}

type frCancelBody struct {
	Reason *string `json:"reason"`
}

// frWriteLog gathers one write's audit line. Every string is passed through
// logSafe, and the reason is never added.
type frWriteLog struct {
	attrs []any
	// fault logs a refusal at ERROR: the client's answer stands, but the
	// server left something behind that an operator must see.
	fault bool
}

func (l *frWriteLog) add(key string, value any) {
	if s, ok := value.(string); ok {
		value = logSafe(s)
	}
	l.attrs = append(l.attrs, key, value)
}

// logSafe removes CR, LF and "event=" from s, so a value cannot start a
// second line or forge the event key in a text log. Removal repeats until
// nothing is left to remove, since removing one occurrence can join two
// halves into another.
func logSafe(s string) string {
	for {
		t := strings.NewReplacer("\r", "", "\n", "", "event=", "").Replace(s)
		if t == s {
			return s
		}
		s = t
	}
}

func (l *frWriteLog) addAttribution(a flowreservation.Attribution) {
	l.add("attribution_kind", a.Kind)
	l.add("principal", a.Principal)
}

func (l *frWriteLog) addDecision(d flowreservation.Decision) {
	if d.Kind == flowreservation.Deny {
		l.add("decision", "deny")
		return
	}
	l.add("decision", "grant")
	if d.Interval != nil {
		l.add("interval_start", d.Interval.Start)
		l.add("interval_duration", d.Interval.Duration)
	}
	if d.Energy != nil {
		l.add("energy_value", d.Energy.Value)
		l.add("energy_multiplier", d.Energy.Multiplier)
	}
	if d.Power != nil {
		l.add("power_value", d.Power.Value)
		l.add("power_multiplier", d.Power.Multiplier)
	}
}

func (h *AdminFlowReservationHandler) logger() *slog.Logger {
	if h.Logger != nil {
		return h.Logger
	}
	return slog.Default()
}

// writeLine logs the one line of a write. op is "answer", "revise" or
// "cancel".
func (h *AdminFlowReservationHandler) writeLine(r *http.Request, level slog.Level, msg, event string, l *frWriteLog) {
	base := []any{"event", event, "remote_addr", logSafe(r.RemoteAddr), "admission", auth.AdmissionPath(r)}
	h.logger().Log(r.Context(), level, msg, append(base, l.attrs...)...)
}

// refuseWrite answers kind and writes the request's one line: WARN for a
// refusal, ERROR for a 500. mrid names the conflicting resource, if any.
func (h *AdminFlowReservationHandler) refuseWrite(w http.ResponseWriter, r *http.Request, op string, kind frRefusalKind, mrid, frqID string, l *frWriteLog) {
	h.refuseWriteBody(w, r, op, kind, frRefusal{MRID: mrid, FrqID: frqID}, l)
}

// refuseWriteBody is refuseWrite for a body that carries more than an mRID;
// its Error and Code are taken from kind.
func (h *AdminFlowReservationHandler) refuseWriteBody(w http.ResponseWriter, r *http.Request, op string, kind frRefusalKind, body frRefusal, l *frWriteLog) {
	l.add("code", kind.code)
	l.add("status", kind.status)
	if body.MRID != "" {
		l.add("conflict_mrid", body.MRID)
	}
	level, msg, event := slog.LevelWarn, "admin: flow reservation write refused", "flow_reservation_"+op+"_refused"
	if kind.status >= http.StatusInternalServerError && kind.status != http.StatusServiceUnavailable {
		level, msg, event = slog.LevelError, "admin: flow reservation write failed", "flow_reservation_"+op+"_failed"
	} else if l.fault {
		level = slog.LevelError
	}
	h.writeLine(r, level, msg, event, l)
	body.Error, body.Code = kind.text, kind.code
	writeFRJSON(w, kind.status, body)
}

func (h *AdminFlowReservationHandler) refuseConflict(w http.ResponseWriter, r *http.Request, op string, c *commitment.ConflictError, frqID string, l *frWriteLog) {
	text, ok := frConflictTexts[c.Code]
	if !ok {
		text = frGenericConflictMsg
	}
	h.refuseWrite(w, r, op, frRefusalKind{http.StatusConflict, string(c.Code), text}, c.MRID, frqID, l)
}

// operatorAttribution is who the server can say made an admin write: the
// verified client certificate's SHA-256 for an mTLS admission, and the shared
// admin key for every other one.
func operatorAttribution(r *http.Request, at int64) flowreservation.Attribution {
	a := flowreservation.Attribution{
		Kind:      flowreservation.KindOperator,
		Admission: auth.AdmissionPath(r),
		Principal: flowreservation.PrincipalAdminKey,
		At:        at,
	}
	if a.Admission == auth.AdmissionPathMTLS && r.TLS != nil && len(r.TLS.PeerCertificates) > 0 {
		sum := sha256.Sum256(r.TLS.PeerCertificates[0].Raw)
		a.Principal = "cert:" + hex.EncodeToString(sum[:])
	}
	return a
}

// decodeFRBody decodes one JSON object into dst, refusing unknown fields,
// trailing data and bodies over frWriteMaxBody. allowEmpty takes an empty
// body as the zero value.
func decodeFRBody(w http.ResponseWriter, r *http.Request, dst any, allowEmpty bool) (frRefusalKind, bool) {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, frWriteMaxBody))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		if allowEmpty && errors.Is(err, io.EOF) {
			return frRefusalKind{}, true
		}
		if strings.HasPrefix(err.Error(), "json: unknown field ") {
			return frUnknownField, false
		}
		return frBodyFormat, false
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return frBodyFormat, false
	}
	return frRefusalKind{}, true
}

// reasonOf checks the operator's reason and returns it reduced to the
// octets it is stored in. Control characters could break a reader's line,
// and bidirectional controls could make it display as other text.
func reasonOf(p *string) (string, frRefusalKind, bool) {
	if p == nil {
		return "", frRefusalKind{}, true
	}
	reason := *p
	for _, c := range reason {
		if unicode.IsControl(c) || unicode.Is(unicode.Bidi_Control, c) {
			return "", frReasonInvalid, false
		}
	}
	if utf8.RuneCountInString(reason) > maxFRReasonRunes {
		return "", frReasonTooLong, false
	}
	return truncateOctets(reason, maxFRReasonOctets), frRefusalKind{}, true
}

// truncateOctets cuts s to at most n octets at a character boundary.
func truncateOctets(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

func multiplierInRange(m int8) bool {
	return m >= -maxFRMultiplier && m <= maxFRMultiplier
}

// decisionOf builds the queue's Decision from the body. Energy and power
// arrive as magnitudes and take the request's own sign, so the page never
// chooses one. A denial carries no quantities, so deny ignores them.
func decisionOf(body frWriteBody, frq sep2.FlowReservationRequest) (flowreservation.Decision, frRefusalKind, bool) {
	if body.Decision == nil {
		return flowreservation.Decision{}, frDecisionInvalid, false
	}
	switch *body.Decision {
	case "deny":
		return flowreservation.Decision{Kind: flowreservation.Deny}, frRefusalKind{}, true
	case "grant":
	default:
		return flowreservation.Decision{}, frDecisionInvalid, false
	}
	if (body.Energy != nil && body.Energy.Value < 0) || (body.Power != nil && body.Power.Value < 0) {
		return flowreservation.Decision{}, frValueNegative, false
	}
	if (body.Energy != nil && !multiplierInRange(body.Energy.Multiplier)) || (body.Power != nil && !multiplierInRange(body.Power.Multiplier)) {
		return flowreservation.Decision{}, frMultiplierRange, false
	}
	d := flowreservation.Decision{Kind: flowreservation.Grant}
	if body.Interval != nil {
		d.Interval = &sep2.DateTimeInterval{Start: body.Interval.Start, Duration: body.Interval.Duration}
	}
	if body.Energy != nil {
		v := body.Energy.Value
		if frq.EnergyRequested != nil && frq.EnergyRequested.Value < 0 {
			v = -v
		}
		d.Energy = &sep2.SignedRealEnergy{Value: v, Multiplier: body.Energy.Multiplier}
	}
	if body.Power != nil {
		v := body.Power.Value
		if frq.PowerRequested != nil && frq.PowerRequested.Value < 0 {
			v = -v
		}
		d.Power = &sep2.ActivePower{Value: v, Multiplier: body.Power.Multiplier}
	}
	return d, frRefusalKind{}, true
}

func decisionRefusal(err error) (frRefusalKind, bool) {
	for _, m := range frDecisionErrors {
		if errors.Is(err, m.err) {
			return m.kind, true
		}
	}
	return frRefusalKind{}, false
}

// frTarget is the request a write addresses, read and placed in its fleet.
// fleet is empty when the EndDevice belongs to no fleet.
type frTarget struct {
	edevID, frqID, fleet string
	frq                  sep2.FlowReservationRequest
}

// target reads the addressed request. A request whose EndDevice belongs to
// no fleet is not addressable for answer, as on the read route; revise and
// cancel pass fleetless so a grant the device took with it when it left its
// fleet is refused by name (grant_unresolved) rather than as not found.
func (h *AdminFlowReservationHandler) target(w http.ResponseWriter, r *http.Request, op string, fleetless bool, l *frWriteLog) (frTarget, bool) {
	t := frTarget{edevID: r.PathValue("edevId"), frqID: r.PathValue("frqId")}
	l.add("edev_id", t.edevID)
	l.add("frq_id", t.frqID)
	ctx := r.Context()
	frq, err := h.Requests.Get(ctx, t.edevID, t.frqID)
	if errors.Is(err, store.ErrNotFound) {
		h.refuseWrite(w, r, op, frNotFound, "", t.frqID, l)
		return t, false
	}
	if err != nil {
		l.add("cause", "request_read")
		h.refuseWrite(w, r, op, frInternal, "", "", l)
		return t, false
	}
	fleet, held, err := h.fleetOf(ctx, t.edevID)
	if err != nil {
		l.add("cause", "fleet_read")
		h.refuseWrite(w, r, op, frInternal, "", "", l)
		return t, false
	}
	if !held && !fleetless {
		h.refuseWrite(w, r, op, frNotFound, "", t.frqID, l)
		return t, false
	}
	t.frq, t.fleet = frq, fleet
	return t, true
}

// reply writes the view after a committed write. The write stands even when
// the view cannot be read, so that failure is logged at ERROR with the
// write's own fields.
func (h *AdminFlowReservationHandler) reply(w http.ResponseWriter, r *http.Request, op, event string, status int, t frTarget, l *frWriteLog, level slog.Level) {
	view, err := h.viewOf(r.Context(), t.edevID, t.frqID, t.fleet, t.frq, h.now())
	if err != nil {
		l.add("cause", "view_after_write")
		l.add("write_committed", true)
		h.refuseWrite(w, r, op, frViewAfterWrite, "", t.frqID, l)
		return
	}
	msg := "admin: flow reservation " + op
	if level == slog.LevelError {
		msg += " committed, attribution unrecorded"
	}
	h.writeLine(r, level, msg, event, l)
	writeFRJSON(w, status, view)
}

// chainTip reads the request's answer chain as the protocol GET serves it.
// tip is nil for an unanswered request.
func (h *AdminFlowReservationHandler) chainTip(ctx context.Context, t frTarget) (chain []sep2.FlowReservationResponse, tip *sep2.FlowReservationResponse, err error) {
	chain, err = flowreservation.ChainOf(ctx, h.Responses, t.edevID, t.frqID)
	if err != nil || len(chain) == 0 {
		return chain, nil, err
	}
	return chain, &chain[len(chain)-1], nil
}

// liveGrant reports whether a response is a grant that can still be revised
// or cancelled: not a denial, not cancelled, not ended.
func liveGrant(frp sep2.FlowReservationResponse, now int64) bool {
	switch {
	case frp.Interval == nil || frp.Interval.Duration == 0:
		return false
	case frp.EventStatus != nil && frp.EventStatus.CurrentStatus == sep2.EventStatusCancelled:
		return false
	default:
		return now < frp.Interval.Start+int64(frp.Interval.Duration)
	}
}

// HandleAnswer serves POST .../answer: grant as asked, grant adjusted, or
// deny a request that has no answer yet.
func (h *AdminFlowReservationHandler) HandleAnswer() http.HandlerFunc {
	const op = "answer"
	return func(w http.ResponseWriter, r *http.Request) {
		var l frWriteLog
		l.add("op", op)
		if h.Queue == nil {
			h.refuseWrite(w, r, op, frNotConfigured, "", "", &l)
			return
		}
		var body frWriteBody
		if kind, ok := decodeFRBody(w, r, &body, false); !ok {
			h.refuseWrite(w, r, op, kind, "", "", &l)
			return
		}
		if body.Reason != nil {
			h.refuseWrite(w, r, op, frReasonNotAllowed, "", "", &l)
			return
		}
		t, ok := h.target(w, r, op, false, &l)
		if !ok {
			return
		}
		d, kind, ok := decisionOf(body, t.frq)
		if !ok {
			h.refuseWrite(w, r, op, kind, "", t.frqID, &l)
			return
		}
		l.addDecision(d)
		d.By = operatorAttribution(r, h.now())
		l.addAttribution(d.By)

		ctx := r.Context()
		frp, err := h.Queue.Answer(ctx, t.edevID, t.frqID, d)
		if err != nil {
			h.refuseAnswer(w, r, err, t, &l)
			return
		}
		l.add("new_mrid", frp.MRID)
		h.reply(w, r, op, "flow_reservation_answered", http.StatusCreated, t, &l, slog.LevelInfo)
	}
}

// internalFailure reports a failure that is the server's, whatever else it
// wraps: a failed undo, or a commitment check that could not complete. Each
// refuse mapper checks it first, since its cause can wrap a refusal's
// sentinel (a NotFound inside a failed undo) and must never read as a 4xx.
func internalFailure(err error) bool {
	return errors.Is(err, commitment.ErrUndo) || errors.Is(err, flowreservation.ErrCommitmentCheck)
}

func (h *AdminFlowReservationHandler) refuseAnswer(w http.ResponseWriter, r *http.Request, err error, t frTarget, l *frWriteLog) {
	const op = "answer"
	var conflict *commitment.ConflictError
	switch {
	case internalFailure(err):
		l.add("cause", causeOf(err))
		h.refuseWrite(w, r, op, frInternal, "", "", l)
	case errors.Is(err, flowreservation.ErrAlreadyAnswered):
		_, tip, cerr := h.chainTip(r.Context(), t)
		if cerr != nil {
			l.add("cause", "chain_read")
			h.refuseWrite(w, r, op, frInternal, "", "", l)
			return
		}
		// A response exists, so this is still a 409; the stray record that
		// may name this operator is logged as the server's fault.
		if errors.Is(err, flowreservation.ErrAnswerRecordTakeBack) {
			l.add("cause", "answer_record_take_back")
			l.fault = true
		}
		mrid := ""
		if tip != nil {
			mrid = tip.MRID
		}
		h.refuseWrite(w, r, op, frAlreadyAnswered, mrid, t.frqID, l)
	case errors.Is(err, flowreservation.ErrRequestCancelled):
		h.refuseWrite(w, r, op, frRequestCancelled, "", t.frqID, l)
	case errors.As(err, &conflict):
		h.refuseConflict(w, r, op, conflict, t.frqID, l)
	case errors.Is(err, store.ErrNotFound):
		h.refuseWrite(w, r, op, frNotFound, "", t.frqID, l)
	default:
		if kind, ok := decisionRefusal(err); ok {
			h.refuseWrite(w, r, op, kind, "", t.frqID, l)
			return
		}
		l.add("cause", causeOf(err))
		h.refuseWrite(w, r, op, frInternal, "", "", l)
	}
}

// causeOf names an internal failure for the log from fixed words, never from
// the error text, which can carry store keys.
func causeOf(err error) string {
	switch {
	case errors.Is(err, flowreservation.ErrCommitmentCheck):
		return "commitment_check"
	case errors.Is(err, commitment.ErrUndo):
		return "undo_failed"
	case errors.Is(err, flowreservation.ErrChainMoving):
		return "chain_moving"
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return "client_gone"
	default:
		return "store_error"
	}
}

// HandleRevise serves POST .../revise: cancel the live grant and answer the
// request again, moving the grant's DER controls when they still fit.
func (h *AdminFlowReservationHandler) HandleRevise() http.HandlerFunc {
	const op = "revise"
	return func(w http.ResponseWriter, r *http.Request) {
		var l frWriteLog
		l.add("op", op)
		if h.Revise.Ledger == nil || h.Revise.FRQ == nil || h.Revise.FRP == nil || h.Revise.Replace == nil {
			h.refuseWrite(w, r, op, frNotConfigured, "", "", &l)
			return
		}
		var body frWriteBody
		if kind, ok := decodeFRBody(w, r, &body, false); !ok {
			h.refuseWrite(w, r, op, kind, "", "", &l)
			return
		}
		reason, kind, ok := reasonOf(body.Reason)
		if !ok {
			h.refuseWrite(w, r, op, kind, "", "", &l)
			return
		}
		t, ok := h.target(w, r, op, true, &l)
		if !ok {
			return
		}
		d, kind, ok := decisionOf(body, t.frq)
		if !ok {
			h.refuseWrite(w, r, op, kind, "", t.frqID, &l)
			return
		}
		l.addDecision(d)
		if t.frq.RequestStatus.RequestStatus == sep2.RequestStatusCancelled {
			h.refuseWrite(w, r, op, frRequestCancelled, "", t.frqID, &l)
			return
		}
		ctx := r.Context()
		now := h.now()
		chain, tip, err := h.chainTip(ctx, t)
		if err != nil {
			l.add("cause", "chain_read")
			h.refuseWrite(w, r, op, frInternal, "", "", &l)
			return
		}
		if tip == nil {
			h.refuseWrite(w, r, op, frNotAnswered, "", t.frqID, &l)
			return
		}
		l.add("old_mrid", tip.MRID)
		if !liveGrant(*tip, now) {
			h.refuseWrite(w, r, op, frGrantNotLive, tip.MRID, t.frqID, &l)
			return
		}
		tipID, ok := flowreservation.ResponseID(t.edevID, tip.Href)
		if !ok {
			l.add("cause", "response_href")
			h.refuseWrite(w, r, op, frInternal, "", "", &l)
			return
		}
		by := operatorAttribution(r, now)
		l.addAttribution(by)

		reviseCtx := ctx
		flush := func() {}
		if h.Notifier != nil {
			reviseCtx, flush = flowreservation.DeferNotifications(ctx, h.Notifier)
		}
		frp, err := flowreservation.Revise(reviseCtx, h.Revise, t.edevID, t.frqID, d, reason, by, time.Unix(now, 0))
		flush()
		if err != nil {
			h.refuseRevise(w, r, err, t, chain, &l)
			return
		}
		l.add("new_mrid", frp.MRID)
		level := h.recordCancels(ctx, t.edevID, []flowreservation.CancelledGrant{{ID: tipID, MRID: tip.MRID}}, by, &l)
		h.reply(w, r, op, "flow_reservation_revised", http.StatusCreated, t, &l, level)
	}
}

func (h *AdminFlowReservationHandler) refuseRevise(w http.ResponseWriter, r *http.Request, err error, t frTarget, chain []sep2.FlowReservationResponse, l *frWriteLog) {
	const op = "revise"
	var conflict *commitment.ConflictError
	switch {
	case internalFailure(err):
		l.add("cause", causeOf(err))
		h.refuseWrite(w, r, op, frInternal, "", "", l)
	case errors.As(err, &conflict):
		// A revise whose undo could not relink every control keeps the
		// revision live beside the grant it replaced, and the next revise is
		// then refused by that older member of the same chain.
		if conflict.Code == commitment.ConflictFleetWindow && inChain(chain[:len(chain)-1], conflict.MRID) {
			h.refuseWrite(w, r, op, frEarlierAnswerLive, conflict.MRID, t.frqID, l)
			return
		}
		h.refuseConflict(w, r, op, conflict, t.frqID, l)
	case errors.Is(err, flowreservation.ErrNothingToRevise):
		h.refuseWrite(w, r, op, frNotAnswered, "", t.frqID, l)
	case errors.Is(err, flowreservation.ErrRequestCancelled):
		h.refuseWrite(w, r, op, frRequestCancelled, "", t.frqID, l)
	case errors.Is(err, commitment.ErrNoGrant), errors.Is(err, commitment.ErrNoLFDI):
		h.refuseWrite(w, r, op, frGrantUnresolved, chain[len(chain)-1].MRID, t.frqID, l)
	case errors.Is(err, commitment.ErrNoLedger):
		h.refuseWrite(w, r, op, frNotConfigured, "", "", l)
	case errors.Is(err, store.ErrNotFound):
		h.refuseWrite(w, r, op, frNotFound, "", t.frqID, l)
	default:
		if kind, ok := decisionRefusal(err); ok {
			h.refuseWrite(w, r, op, kind, "", t.frqID, l)
			return
		}
		l.add("cause", causeOf(err))
		h.refuseWrite(w, r, op, frInternal, "", "", l)
	}
}

func inChain(chain []sep2.FlowReservationResponse, mrid string) bool {
	for _, frp := range chain {
		if frp.MRID == mrid {
			return true
		}
	}
	return false
}

// recordCancels records by as the canceller of each response, after the
// cancel committed. A failure leaves the response reading "unrecorded" and
// raises the write's line to ERROR, which it returns.
func (h *AdminFlowReservationHandler) recordCancels(ctx context.Context, edevID string, cancelled []flowreservation.CancelledGrant, by flowreservation.Attribution, l *frWriteLog) slog.Level {
	if h.CancelRecorder == nil {
		return slog.LevelInfo
	}
	var failed []string
	for _, g := range cancelled {
		if err := h.CancelRecorder.RecordCancel(context.WithoutCancel(ctx), edevID, g.ID, by); err != nil {
			failed = append(failed, g.ID)
		}
	}
	if len(failed) == 0 {
		return slog.LevelInfo
	}
	l.add("cancel_record_failed", strings.Join(failed, ","))
	return slog.LevelError
}

// HandleCancel serves POST .../cancel: cancel the live grant and every DER
// control executing it, in one fleet-locked call per grant.
func (h *AdminFlowReservationHandler) HandleCancel() http.HandlerFunc {
	const op = "cancel"
	return func(w http.ResponseWriter, r *http.Request) {
		var l frWriteLog
		l.add("op", op)
		if h.Canceller == nil {
			h.refuseWrite(w, r, op, frNotConfigured, "", "", &l)
			return
		}
		var body frCancelBody
		if kind, ok := decodeFRBody(w, r, &body, true); !ok {
			h.refuseWrite(w, r, op, kind, "", "", &l)
			return
		}
		reason, kind, ok := reasonOf(body.Reason)
		if !ok {
			h.refuseWrite(w, r, op, kind, "", "", &l)
			return
		}
		t, ok := h.target(w, r, op, true, &l)
		if !ok {
			return
		}
		ctx := r.Context()
		now := h.now()
		chain, tip, err := h.chainTip(ctx, t)
		if err != nil {
			l.add("cause", "chain_read")
			h.refuseWrite(w, r, op, frInternal, "", "", &l)
			return
		}
		if tip == nil {
			h.refuseWrite(w, r, op, frNotAnswered, "", t.frqID, &l)
			return
		}
		l.add("old_mrid", tip.MRID)
		// Any live member is checked, not only the tip: a revise whose undo
		// failed can leave an earlier grant live after the tip has ended, and
		// a cancel is what clears it.
		if !anyLiveGrant(chain, now) {
			h.refuseWrite(w, r, op, frGrantNotLive, tip.MRID, t.frqID, &l)
			return
		}
		by := operatorAttribution(r, now)
		l.addAttribution(by)

		cancelled, err := h.Canceller.CancelGrants(ctx, t.edevID, t.frqID, reason)
		l.add("cancelled_count", len(cancelled))
		level := h.recordCancels(ctx, t.edevID, cancelled, by, &l)
		if err != nil {
			h.refuseCancel(w, r, err, cancelled, t, &l)
			return
		}
		if len(cancelled) == 0 {
			// Another cancel or a revision got there first.
			h.refuseWrite(w, r, op, frGrantNotLive, tip.MRID, t.frqID, &l)
			return
		}
		h.reply(w, r, op, "flow_reservation_cancelled", http.StatusOK, t, &l, level)
	}
}

func anyLiveGrant(chain []sep2.FlowReservationResponse, now int64) bool {
	for _, frp := range chain {
		if liveGrant(frp, now) {
			return true
		}
	}
	return false
}

// refuseCancel answers a CancelGrants error. Once anything was cancelled the
// write is committed in part, so it is never answered as a refusal: the body
// names every grant cancelled and every one the ledger could not reach.
func (h *AdminFlowReservationHandler) refuseCancel(w http.ResponseWriter, r *http.Request, err error, cancelled []flowreservation.CancelledGrant, t frTarget, l *frWriteLog) {
	const op = "cancel"
	var unresolvedErr *flowreservation.UnresolvedGrantsError
	var unresolved []string
	if errors.As(err, &unresolvedErr) {
		unresolved = unresolvedErr.MRIDs
		l.add("unresolved_mrids", strings.Join(unresolved, ","))
	}
	switch {
	case len(cancelled) > 0:
		mrids := make([]string, len(cancelled))
		for i, g := range cancelled {
			mrids[i] = g.MRID
		}
		l.add("cancelled_mrids", strings.Join(mrids, ","))
		l.add("write_committed", "partial")
		l.add("cause", causeOf(err))
		h.refuseWriteBody(w, r, op, frCancelPartial, frRefusal{FrqID: t.frqID, Cancelled: mrids, Unresolved: unresolved}, l)
	case internalFailure(err):
		l.add("cause", causeOf(err))
		h.refuseWrite(w, r, op, frInternal, "", "", l)
	case len(unresolved) > 0:
		h.refuseWriteBody(w, r, op, frGrantUnresolved, frRefusal{MRID: unresolved[0], FrqID: t.frqID, Unresolved: unresolved}, l)
	case errors.Is(err, flowreservation.ErrChainMoving):
		h.refuseWrite(w, r, op, frChainMoving, "", t.frqID, l)
	case errors.Is(err, commitment.ErrNoLedger):
		h.refuseWrite(w, r, op, frNotConfigured, "", "", l)
	case errors.Is(err, store.ErrNotFound):
		h.refuseWrite(w, r, op, frNotAnswered, "", t.frqID, l)
	default:
		l.add("cause", causeOf(err))
		h.refuseWrite(w, r, op, frInternal, "", "", l)
	}
}
