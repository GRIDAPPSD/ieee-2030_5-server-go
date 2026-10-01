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

// maxFRReasonRunes bounds reason in Unicode code points, the unit the admin
// page counts in.
const maxFRReasonRunes = 192

// FlowReservationAnswerer answers a pending request; *flowreservation.Queue
// is the production one.
type FlowReservationAnswerer interface {
	Answer(ctx context.Context, edevID, frqID string, decision flowreservation.Decision) (sep2.FlowReservationResponse, error)
}

// FlowReservationGrantCanceller cancels every live grant of a request's
// answer; *flowreservation.Canceller is the production one.
type FlowReservationGrantCanceller interface {
	CancelGrants(ctx context.Context, edevID, frqID, reason string) (cancelled []string, err error)
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
	frGrantUnresolved    = frRefusalKind{http.StatusConflict, "grant_unresolved", "the grant's EndDevice belongs to no fleet, so its grant cannot be changed; restore the device's fleet and repeat, or delete the EndDevice"}
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
	l.add("code", kind.code)
	l.add("status", kind.status)
	if mrid != "" {
		l.add("conflict_mrid", mrid)
	}
	level, msg, event := slog.LevelWarn, "admin: flow reservation write refused", "flow_reservation_"+op+"_refused"
	if kind.status >= http.StatusInternalServerError && kind.status != http.StatusServiceUnavailable {
		level, msg, event = slog.LevelError, "admin: flow reservation write failed", "flow_reservation_"+op+"_failed"
	}
	h.writeLine(r, level, msg, event, l)
	writeFRJSON(w, kind.status, frRefusal{Error: kind.text, Code: kind.code, MRID: mrid, FrqID: frqID})
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

func reasonOf(p *string) (string, bool) {
	if p == nil {
		return "", true
	}
	return *p, utf8.RuneCountInString(*p) <= maxFRReasonRunes
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
type frTarget struct {
	edevID, frqID, fleet string
	frq                  sep2.FlowReservationRequest
}

// target reads the addressed request. A request whose EndDevice belongs to
// no fleet is not addressable, as on the read route.
func (h *AdminFlowReservationHandler) target(w http.ResponseWriter, r *http.Request, op string, l *frWriteLog) (frTarget, bool) {
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
	if !held {
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
		h.refuseWrite(w, r, op, frInternal, "", "", l)
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
		t, ok := h.target(w, r, op, &l)
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

func (h *AdminFlowReservationHandler) refuseAnswer(w http.ResponseWriter, r *http.Request, err error, t frTarget, l *frWriteLog) {
	const op = "answer"
	var conflict *commitment.ConflictError
	switch {
	case errors.Is(err, flowreservation.ErrAlreadyAnswered):
		_, tip, cerr := h.chainTip(r.Context(), t)
		if cerr != nil {
			l.add("cause", "chain_read")
			h.refuseWrite(w, r, op, frInternal, "", "", l)
			return
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
		reason, ok := reasonOf(body.Reason)
		if !ok {
			h.refuseWrite(w, r, op, frReasonTooLong, "", "", &l)
			return
		}
		t, ok := h.target(w, r, op, &l)
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
		level := h.recordCancels(ctx, t.edevID, []string{tipID}, by, &l)
		h.reply(w, r, op, "flow_reservation_revised", http.StatusCreated, t, &l, level)
	}
}

func (h *AdminFlowReservationHandler) refuseRevise(w http.ResponseWriter, r *http.Request, err error, t frTarget, chain []sep2.FlowReservationResponse, l *frWriteLog) {
	const op = "revise"
	var conflict *commitment.ConflictError
	switch {
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
	case errors.Is(err, commitment.ErrNoGrant):
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
func (h *AdminFlowReservationHandler) recordCancels(ctx context.Context, edevID string, ids []string, by flowreservation.Attribution, l *frWriteLog) slog.Level {
	if h.CancelRecorder == nil {
		return slog.LevelInfo
	}
	var failed []string
	for _, id := range ids {
		if err := h.CancelRecorder.RecordCancel(context.WithoutCancel(ctx), edevID, id, by); err != nil {
			failed = append(failed, id)
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
		reason, ok := reasonOf(body.Reason)
		if !ok {
			h.refuseWrite(w, r, op, frReasonTooLong, "", "", &l)
			return
		}
		t, ok := h.target(w, r, op, &l)
		if !ok {
			return
		}
		ctx := r.Context()
		now := h.now()
		_, tip, err := h.chainTip(ctx, t)
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
		by := operatorAttribution(r, now)
		l.addAttribution(by)

		cancelled, err := h.Canceller.CancelGrants(ctx, t.edevID, t.frqID, reason)
		l.add("cancelled_count", len(cancelled))
		level := h.recordCancels(ctx, t.edevID, cancelled, by, &l)
		if err != nil {
			h.refuseCancel(w, r, err, len(cancelled), t, &l)
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

func (h *AdminFlowReservationHandler) refuseCancel(w http.ResponseWriter, r *http.Request, err error, cancelled int, t frTarget, l *frWriteLog) {
	const op = "cancel"
	var unresolved *flowreservation.UnresolvedGrantsError
	switch {
	case cancelled > 0:
		// Part of the chain is cancelled, so this is no refusal: the write
		// is partial and the log line names how far it got.
		l.add("write_committed", "partial")
		l.add("cause", causeOf(err))
		h.refuseWrite(w, r, op, frInternal, "", "", l)
	case errors.As(err, &unresolved):
		h.refuseWrite(w, r, op, frGrantUnresolved, unresolved.MRIDs[0], t.frqID, l)
	case errors.Is(err, commitment.ErrNoLedger):
		h.refuseWrite(w, r, op, frNotConfigured, "", "", l)
	case errors.Is(err, store.ErrNotFound):
		h.refuseWrite(w, r, op, frNotAnswered, "", t.frqID, l)
	default:
		l.add("cause", causeOf(err))
		h.refuseWrite(w, r, op, frInternal, "", "", l)
	}
}
