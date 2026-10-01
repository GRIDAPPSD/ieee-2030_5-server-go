package handler

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"log/slog"
	"math"
	"math/big"
	"net/http"
	"slices"
	"strings"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/commitment"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/dercontrol"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/flowreservation"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/handlers/sep2time"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
)

// #764: admin read API for flow reservations, the DERMS queue pane's data.
//
//	GET /api/derms/flow-reservations?aggregatorLFDI=<lfdi>[&state=<s>,...]
//	GET /api/derms/flow-reservations/{edevId}/{frqId}
//	GET /api/derms/grants?aggregatorLFDI=<lfdi>&live=true
//
// Every status in a view is read from the decorated response and control
// stores the protocol GET serves, never computed here, so the two cannot
// disagree.

// Flow reservation states, in the order the view documents them.
const (
	frStatePending   = "pending"
	frStateOverdue   = "overdue"
	frStateGranted   = "granted"
	frStateDenied    = "denied"
	frStateCancelled = "cancelled"
	frStateWithdrawn = "withdrawn"
	frStateEnded     = "ended"
)

var frStates = []string{frStatePending, frStateOverdue, frStateGranted, frStateDenied, frStateCancelled, frStateWithdrawn, frStateEnded}

// Attribution kinds a view reports when no record names who acted.
const frKindUnrecorded = "unrecorded"

// FlowReservationRequestReader is what the read API needs of the request
// store: a request by id, a page under an EndDevice, and the EndDevices that
// hold any.
type FlowReservationRequestReader interface {
	Get(ctx context.Context, parentID, id string) (sep2.FlowReservationRequest, error)
	List(ctx context.Context, parentID string, opts store.ListOptions) (store.ListResult[sep2.FlowReservationRequest], error)
	Parents(ctx context.Context) ([]string, error)
}

// FlowReservationAttributions yields who answered and who cancelled a
// response. A nil result is "nothing recorded", which a view shows as
// kind "unrecorded", never as an operator.
type FlowReservationAttributions interface {
	AttributionsOf(ctx context.Context, edevID, responseID string) (answeredBy, cancelledBy *flowreservation.Attribution, err error)
}

// FlowReservationExecutions lists the controls that carry out a grant,
// cancelled ones included. commitment.ControlSource satisfies it.
type FlowReservationExecutions interface {
	ExecutionsOf(ctx context.Context, grantMRID string) ([]commitment.Control, error)
}

type flowReservationFleets interface {
	FleetOf(ctx context.Context, endDeviceID string) (string, error)
}

type flowReservationLifecycles interface {
	Get(ctx context.Context, parentID, id string) (dercontrol.LifecycleRecord, error)
}

// AdminFlowReservationHandler is the dependency surface of the three read
// routes and the three write routes (admin_flowreservation_write.go).
type AdminFlowReservationHandler struct {
	Requests FlowReservationRequestReader
	// Responses must be the store the protocol GET serves, the one that
	// derives EventStatus from each response's lifecycle record.
	Responses  store.ScopedReader[sep2.FlowReservationResponse]
	Lifecycles flowReservationLifecycles
	Fleets     flowReservationFleets
	// Executions and Controls are nil when no DER control store is wired:
	// then no execution exists and every response lists none. Controls must
	// be the store the protocol GET serves, for the same reason as Responses.
	Executions   FlowReservationExecutions
	Controls     store.ScopedReader[sep2.DERControl]
	Attributions FlowReservationAttributions
	// Deadline is the configuration the queue's fallback runs under.
	Deadline flowreservation.Config
	// Persisted is true when a restart keeps the records these routes read.
	Persisted bool
	// Now is the clock for state and the page's countdown base; nil uses
	// the protocol clock.
	Now func() int64

	// Queue, Revise and Canceller carry out the writes; a write whose
	// dependency is unset answers 503 not_configured. Revise.FRP must read
	// the stored responses and Revise.Answers records the reviser.
	Queue     FlowReservationAnswerer
	Revise    flowreservation.ReviseDeps
	Canceller FlowReservationGrantCanceller
	// CancelRecorder records who revised or cancelled a grant, once the
	// change committed; nil records nothing.
	CancelRecorder FlowReservationCancelRecorder
	// Notifier is the one Revise.Writers notify through. A revise holds its
	// notifications until the fleet lock is released; nil holds nothing.
	Notifier flowreservation.Notifier
	// Logger receives one line per write; nil uses slog.Default().
	Logger *slog.Logger
}

// The JSON shapes below are the contract the frontend fixture
// (pkg/adminui/web/frontend/src/lib/flowreservation.fixture.json) pins. Every
// quantity the server did not read is a pointer, so it marshals as null and
// never as 0.

type frScaled struct {
	Value      int64 `json:"value"`
	Multiplier int8  `json:"multiplier"`
}

type frInterval struct {
	Start    int64  `json:"start"`
	Duration uint32 `json:"duration"`
}

type frEventStatus struct {
	CurrentStatus uint8  `json:"currentStatus"`
	Status        string `json:"status"`
	DateTime      int64  `json:"dateTime"`
}

// FlowReservationActor is who acted on a response.
type FlowReservationActor struct {
	Kind      string  `json:"kind"`
	Admission *string `json:"admission"`
	Principal *string `json:"principal"`
	At        int64   `json:"at"`
}

// FlowReservationExecutionView is one control carrying out a grant.
type FlowReservationExecutionView struct {
	MRID               string         `json:"mRID"`
	Href               string         `json:"href"`
	DERControlListHref string         `json:"derControlListHref"`
	Interval           *frInterval    `json:"interval"`
	TargetW            *frScaled      `json:"targetW"`
	EventStatus        *frEventStatus `json:"eventStatus"`
}

// FlowReservationResponseView is one response in a request's chain.
type FlowReservationResponseView struct {
	ID                string                         `json:"id"`
	Href              string                         `json:"href"`
	MRID              string                         `json:"mRID"`
	Subject           string                         `json:"subject"`
	CreationTime      int64                          `json:"creationTime"`
	Interval          *frInterval                    `json:"interval"`
	EnergyAvailable   *frScaled                      `json:"energyAvailable"`
	PowerAvailable    *frScaled                      `json:"powerAvailable"`
	Direction         *string                        `json:"direction"`
	EventStatus       *frEventStatus                 `json:"eventStatus"`
	CancelReason      *string                        `json:"cancelReason"`
	AnsweredBy        FlowReservationActor           `json:"answeredBy"`
	CancelledBy       *FlowReservationActor          `json:"cancelledBy"`
	Executions        []FlowReservationExecutionView `json:"executions"`
	EnergyCommittedWh *float64                       `json:"energyCommittedWh"`
	EnergyRemainingWh *float64                       `json:"energyRemainingWh"`
}

type frRequestBody struct {
	MRID              string      `json:"mRID"`
	CreationTime      int64       `json:"creationTime"`
	RequestStatus     string      `json:"requestStatus"`
	IntervalRequested *frInterval `json:"intervalRequested"`
	EnergyRequested   *frScaled   `json:"energyRequested"`
	PowerRequested    *frScaled   `json:"powerRequested"`
	Direction         *string     `json:"direction"`
}

// FlowReservationView is one request with its chain of responses.
type FlowReservationView struct {
	EdevID         string `json:"edevId"`
	FrqID          string `json:"frqId"`
	RequestHref    string `json:"requestHref"`
	AggregatorLFDI string `json:"aggregatorLFDI"`
	State          string `json:"state"`
	// RequestCancelled is set only on a granted request the client cancelled
	// while its grant is still live: the cancel is incomplete.
	RequestCancelled bool                          `json:"requestCancelled,omitempty"`
	DeadlineAt       *int64                        `json:"deadlineAt"`
	Request          frRequestBody                 `json:"request"`
	Responses        []FlowReservationResponseView `json:"responses"`
	Tip              *FlowReservationResponseView  `json:"tip"`
}

// FlowReservationList is one fleet's requests.
type FlowReservationList struct {
	AggregatorLFDI  string                `json:"aggregatorLFDI"`
	Now             int64                 `json:"now"`
	DeadlineSeconds int                   `json:"deadlineSeconds"`
	Persisted       bool                  `json:"persisted"`
	Requests        []FlowReservationView `json:"requests"`
}

// FlowReservationGrantView is a live grant the dispatch pane can execute.
type FlowReservationGrantView struct {
	EdevID           string                      `json:"edevId"`
	FrqID            string                      `json:"frqId"`
	Response         FlowReservationResponseView `json:"response"`
	SuggestedTargetW frScaled                    `json:"suggestedTargetW"`
}

// FlowReservationGrantList is one fleet's live grants.
type FlowReservationGrantList struct {
	AggregatorLFDI string                     `json:"aggregatorLFDI"`
	Now            int64                      `json:"now"`
	Grants         []FlowReservationGrantView `json:"grants"`
}

// frRefusal is the one refusal body of the flow reservation admin API.
// Cancelled and Unresolved appear only on a cancel that did not settle the
// whole chain: the mRIDs it cancelled and those the ledger could not reach.
type frRefusal struct {
	Error      string   `json:"error"`
	Code       string   `json:"code"`
	MRID       string   `json:"mRID"`
	FrqID      string   `json:"frqId"`
	Cancelled  []string `json:"cancelled,omitempty"`
	Unresolved []string `json:"unresolved,omitempty"`
}

func writeFRRefusal(w http.ResponseWriter, status int, code, text, frqID string) {
	writeFRJSON(w, status, frRefusal{Error: text, Code: code, FrqID: frqID})
}

func writeFRInternal(w http.ResponseWriter) {
	writeFRRefusal(w, http.StatusInternalServerError, "internal", "flow reservation read failed, see server log", "")
}

// writeFRJSON encodes with two-space indentation, the form the golden file
// holds, so the served bytes are the pinned bytes.
func writeFRJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		log.Printf("admin flow reservations: encode: %v", err)
	}
}

func (h *AdminFlowReservationHandler) now() int64 {
	if h.Now != nil {
		return h.Now()
	}
	return sep2time.Now().Unix()
}

// HandleList serves GET /api/derms/flow-reservations.
func (h *AdminFlowReservationHandler) HandleList() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		agg, ok := fleetParam(w, r)
		if !ok {
			return
		}
		states, ok := stateFilter(w, r)
		if !ok {
			return
		}
		now := h.now()
		views, err := h.fleetViews(r.Context(), agg, now)
		if err != nil {
			log.Printf("admin flow reservations: list %s: %v", agg, err)
			writeFRInternal(w)
			return
		}
		out := make([]FlowReservationView, 0, len(views))
		for _, v := range views {
			if len(states) == 0 || slices.Contains(states, v.State) {
				out = append(out, v)
			}
		}
		writeFRJSON(w, http.StatusOK, FlowReservationList{
			AggregatorLFDI:  agg,
			Now:             now,
			DeadlineSeconds: int(h.Deadline.EffectiveDeadline().Seconds()),
			Persisted:       h.Persisted,
			Requests:        out,
		})
	}
}

// HandleGet serves GET /api/derms/flow-reservations/{edevId}/{frqId}.
func (h *AdminFlowReservationHandler) HandleGet() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		edevID, frqID := r.PathValue("edevId"), r.PathValue("frqId")
		frq, err := h.Requests.Get(ctx, edevID, frqID)
		if errors.Is(err, store.ErrNotFound) {
			writeFRRefusal(w, http.StatusNotFound, "request_not_found", "no such flow reservation request", frqID)
			return
		}
		if err != nil {
			log.Printf("admin flow reservations: get %s/%s: %v", edevID, frqID, err)
			writeFRInternal(w)
			return
		}
		fleet, held, err := h.fleetOf(ctx, edevID)
		if err != nil {
			log.Printf("admin flow reservations: fleet of %s: %v", edevID, err)
			writeFRInternal(w)
			return
		}
		if !held {
			writeFRRefusal(w, http.StatusNotFound, "request_not_found", "no such flow reservation request", frqID)
			return
		}
		view, err := h.viewOf(ctx, edevID, frqID, fleet, frq, h.now())
		if err != nil {
			log.Printf("admin flow reservations: view %s/%s: %v", edevID, frqID, err)
			writeFRInternal(w)
			return
		}
		writeFRJSON(w, http.StatusOK, view)
	}
}

// HandleGrants serves GET /api/derms/grants.
func (h *AdminFlowReservationHandler) HandleGrants() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		agg, ok := fleetParam(w, r)
		if !ok {
			return
		}
		if r.URL.Query().Get("live") != "true" {
			writeFRRefusal(w, http.StatusBadRequest, "live_required", "live=true is required", "")
			return
		}
		now := h.now()
		views, err := h.fleetViews(r.Context(), agg, now)
		if err != nil {
			log.Printf("admin flow reservations: grants %s: %v", agg, err)
			writeFRInternal(w)
			return
		}
		grants := make([]FlowReservationGrantView, 0)
		for _, v := range views {
			if v.State != frStateGranted || v.Tip == nil {
				continue
			}
			target, ok := suggestedTarget(v.Tip)
			if !ok {
				continue
			}
			grants = append(grants, FlowReservationGrantView{EdevID: v.EdevID, FrqID: v.FrqID, Response: *v.Tip, SuggestedTargetW: target})
		}
		writeFRJSON(w, http.StatusOK, FlowReservationGrantList{AggregatorLFDI: agg, Now: now, Grants: grants})
	}
}

// suggestedTarget is the opModTargetW that executes the grant at its full
// power, signed in the DER frame (discharge positive) by the direction the
// view already derived. A grant with no direction or no power has none.
func suggestedTarget(tip *FlowReservationResponseView) (frScaled, bool) {
	if tip.Direction == nil || tip.PowerAvailable == nil {
		return frScaled{}, false
	}
	magnitude := tip.PowerAvailable.Value
	if magnitude < 0 {
		magnitude = -magnitude
	}
	if *tip.Direction == "charge" {
		magnitude = -magnitude
	}
	return frScaled{Value: magnitude, Multiplier: tip.PowerAvailable.Multiplier}, true
}

func fleetParam(w http.ResponseWriter, r *http.Request) (string, bool) {
	v := r.URL.Query().Get("aggregatorLFDI")
	if len(v) != 40 || strings.Trim(v, "0123456789abcdefABCDEF") != "" {
		writeFRRefusal(w, http.StatusBadRequest, "aggregator_lfdi_invalid", "aggregatorLFDI must be 40 hex digits", "")
		return "", false
	}
	return strings.ToUpper(v), true
}

func stateFilter(w http.ResponseWriter, r *http.Request) ([]string, bool) {
	var states []string
	for _, v := range r.URL.Query()["state"] {
		for s := range strings.SplitSeq(v, ",") {
			if !slices.Contains(frStates, s) {
				writeFRRefusal(w, http.StatusBadRequest, "state_invalid", "state is not one of the documented states", "")
				return nil, false
			}
			states = append(states, s)
		}
	}
	return states, true
}

// fleetOf resolves the fleet an EndDevice's requests list under. held is
// false for a device that is gone or has no LFDI: it belongs to no fleet, so
// its requests are not addressable.
func (h *AdminFlowReservationHandler) fleetOf(ctx context.Context, edevID string) (fleet string, held bool, err error) {
	fleet, err = h.Fleets.FleetOf(ctx, edevID)
	if errors.Is(err, store.ErrNotFound) || errors.Is(err, commitment.ErrNoLFDI) {
		log.Printf("WARNING: admin flow reservations: EndDevice %s skipped, its requests belong to no fleet: %v", edevID, err)
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return fleet, true, nil
}

// fleetViews returns every request of the fleet in display order. Any store
// error fails the whole read: a partial list is not a fact to assert.
func (h *AdminFlowReservationHandler) fleetViews(ctx context.Context, agg string, now int64) ([]FlowReservationView, error) {
	parents, err := h.Requests.Parents(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing request parents: %w", err)
	}
	views := make([]FlowReservationView, 0)
	for _, edevID := range parents {
		fleet, held, err := h.fleetOf(ctx, edevID)
		if err != nil {
			return nil, fmt.Errorf("fleet of %s: %w", edevID, err)
		}
		if !held || fleet != agg {
			continue
		}
		page, err := h.Requests.List(ctx, edevID, store.ListOptions{Unbounded: true})
		if err != nil {
			return nil, fmt.Errorf("listing requests of %s: %w", edevID, err)
		}
		for _, frq := range page.Items {
			frqID, ok := strings.CutPrefix(frq.Href, "/edev/"+edevID+"/frq/")
			if !ok || frqID == "" || strings.Contains(frqID, "/") {
				return nil, fmt.Errorf("request href %q under %s has no store id", frq.Href, edevID)
			}
			v, err := h.viewOf(ctx, edevID, frqID, fleet, frq, now)
			if err != nil {
				return nil, fmt.Errorf("request %s/%s: %w", edevID, frqID, err)
			}
			views = append(views, v)
		}
	}
	slices.SortFunc(views, compareViews)
	return views, nil
}

// compareViews puts pending requests first by deadline, then orders the rest
// as IEEE 2030.5-2023 Table 54 does: interval.start ascending, creationTime
// descending, mRID descending. A request with no interval sorts last, and
// the store id settles any remaining tie so the order is stable.
func compareViews(a, b FlowReservationView) int {
	ap, bp := a.State == frStatePending, b.State == frStatePending
	if ap != bp {
		if ap {
			return -1
		}
		return 1
	}
	if ap && bp {
		if c := cmp.Compare(*a.DeadlineAt, *b.DeadlineAt); c != 0 {
			return c
		}
	}
	return cmp.Or(
		cmp.Compare(startKey(a.Request.IntervalRequested), startKey(b.Request.IntervalRequested)),
		cmp.Compare(b.Request.CreationTime, a.Request.CreationTime),
		cmp.Compare(b.Request.MRID, a.Request.MRID),
		cmp.Compare(a.EdevID, b.EdevID),
		cmp.Compare(a.FrqID, b.FrqID),
	)
}

func startKey(i *frInterval) int64 {
	if i == nil {
		return math.MaxInt64
	}
	return i.Start
}

func (h *AdminFlowReservationHandler) viewOf(ctx context.Context, edevID, frqID, fleet string, frq sep2.FlowReservationRequest, now int64) (FlowReservationView, error) {
	chain, err := flowreservation.ChainOf(ctx, h.Responses, edevID, frqID)
	if err != nil {
		return FlowReservationView{}, err
	}
	responses := make([]FlowReservationResponseView, 0, len(chain))
	id := frqID
	for i, resp := range chain {
		if i > 0 {
			id = flowreservation.RevisionID(id)
		}
		rv, err := h.responseView(ctx, edevID, id, resp)
		if err != nil {
			return FlowReservationView{}, err
		}
		responses = append(responses, rv)
	}

	view := FlowReservationView{
		EdevID:         edevID,
		FrqID:          frqID,
		RequestHref:    frq.Href,
		AggregatorLFDI: fleet,
		Request:        requestBody(frq),
		Responses:      responses,
	}
	if n := len(responses); n > 0 {
		tip := responses[n-1]
		view.Tip = &tip
	}
	deadlineAt := flowreservation.DeadlineAt(h.Deadline, frq)
	view.State = stateOf(frq, view.Tip, now, deadlineAt)
	view.RequestCancelled = view.State == frStateGranted && frq.RequestStatus.RequestStatus == sep2.RequestStatusCancelled
	if view.State == frStatePending || view.State == frStateOverdue {
		view.DeadlineAt = &deadlineAt
	}
	return view, nil
}

// stateOf names where a request stands, from its chain first: an unanswered
// request is pending until its deadline and overdue after, and an answered
// one follows its tip. A client cancel marks the request before it cancels
// the grant, so a cancelled request whose tip is still live stays granted
// (requestCancelled tells the operator the cancel did not finish); every
// other cancelled request is withdrawn.
func stateOf(frq sep2.FlowReservationRequest, tip *FlowReservationResponseView, now, deadlineAt int64) string {
	state := chainState(tip, now, deadlineAt)
	if frq.RequestStatus.RequestStatus == sep2.RequestStatusCancelled && state != frStateGranted {
		return frStateWithdrawn
	}
	return state
}

func chainState(tip *FlowReservationResponseView, now, deadlineAt int64) string {
	switch {
	case tip == nil && now < deadlineAt:
		return frStatePending
	case tip == nil:
		return frStateOverdue
	case tip.Interval == nil || tip.Interval.Duration == 0:
		return frStateDenied
	case tip.EventStatus != nil && tip.EventStatus.CurrentStatus == sep2.EventStatusCancelled:
		return frStateCancelled
	case now >= tip.Interval.Start+int64(tip.Interval.Duration):
		return frStateEnded
	default:
		return frStateGranted
	}
}

func requestBody(frq sep2.FlowReservationRequest) frRequestBody {
	return frRequestBody{
		MRID:              frq.MRID,
		CreationTime:      frq.CreationTime,
		RequestStatus:     requestStatusWord(frq.RequestStatus.RequestStatus),
		IntervalRequested: intervalOf(frq.IntervalRequested),
		EnergyRequested:   energyOf(frq.EnergyRequested),
		PowerRequested:    powerOf(frq.PowerRequested),
		Direction:         directionOf(frq.EnergyRequested),
	}
}

func requestStatusWord(s uint8) string {
	if s == sep2.RequestStatusCancelled {
		return "cancelled"
	}
	return "requested"
}

func intervalOf(i *sep2.DateTimeInterval) *frInterval {
	if i == nil {
		return nil
	}
	return &frInterval{Start: i.Start, Duration: i.Duration}
}

func energyOf(e *sep2.SignedRealEnergy) *frScaled {
	if e == nil {
		return nil
	}
	return &frScaled{Value: e.Value, Multiplier: e.Multiplier}
}

func powerOf(p *sep2.ActivePower) *frScaled {
	if p == nil {
		return nil
	}
	return &frScaled{Value: int64(p.Value), Multiplier: p.Multiplier}
}

// directionOf names the flow of an energy quantity from the one rule the
// ledger executes by, so the page never flips a sign itself. Zero or absent
// energy has no direction.
func directionOf(e *sep2.SignedRealEnergy) *string {
	if e == nil || e.Value == 0 {
		return nil
	}
	word := "charge"
	if commitment.DirectionOf(commitment.Grant{Energy: e}) > 0 {
		word = "discharge"
	}
	return &word
}

func eventStatusOf(es *sep2.EventStatus) *frEventStatus {
	if es == nil {
		return nil
	}
	return &frEventStatus{CurrentStatus: es.CurrentStatus, Status: eventStatusWord(es.CurrentStatus), DateTime: es.DateTime}
}

func eventStatusWord(s uint8) string {
	switch s {
	case sep2.EventStatusScheduled:
		return "scheduled"
	case sep2.EventStatusActive:
		return "active"
	case sep2.EventStatusCancelled:
		return "cancelled"
	case sep2.EventStatusSuperseded:
		return "superseded"
	case sep2.EventStatusComplete:
		return "complete"
	default:
		return fmt.Sprintf("status_%d", s)
	}
}

func (h *AdminFlowReservationHandler) responseView(ctx context.Context, edevID, id string, resp sep2.FlowReservationResponse) (FlowReservationResponseView, error) {
	rv := FlowReservationResponseView{
		ID:              id,
		Href:            resp.Href,
		MRID:            resp.MRID,
		Subject:         resp.Subject,
		CreationTime:    resp.CreationTime,
		Interval:        intervalOf(resp.Interval),
		EnergyAvailable: energyOf(resp.EnergyAvailable),
		PowerAvailable:  powerOf(resp.PowerAvailable),
		Direction:       directionOf(resp.EnergyAvailable),
		EventStatus:     eventStatusOf(resp.EventStatus),
		Executions:      []FlowReservationExecutionView{},
	}

	lc, err := h.Lifecycles.Get(ctx, edevID, id)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return rv, fmt.Errorf("lifecycle of response %s/%s: %w", edevID, id, err)
	}
	if lc.CancelReason != "" {
		reason := lc.CancelReason
		rv.CancelReason = &reason
	}

	answered, cancelled, err := h.attributions(ctx, edevID, id)
	if err != nil {
		return rv, err
	}
	rv.AnsweredBy = actorOf(answered, resp.CreationTime)
	if rv.EventStatus != nil && rv.EventStatus.CurrentStatus == sep2.EventStatusCancelled {
		actor := actorOf(cancelled, rv.EventStatus.DateTime)
		rv.CancelledBy = &actor
	}

	live, err := h.executions(ctx, &rv)
	if err != nil {
		return rv, err
	}
	rv.EnergyCommittedWh, rv.EnergyRemainingWh = energyFigures(resp, live)
	return rv, nil
}

func (h *AdminFlowReservationHandler) attributions(ctx context.Context, edevID, id string) (answered, cancelled *flowreservation.Attribution, err error) {
	if h.Attributions == nil {
		return nil, nil, nil
	}
	answered, cancelled, err = h.Attributions.AttributionsOf(ctx, edevID, id)
	if err != nil {
		return nil, nil, fmt.Errorf("attribution of response %s/%s: %w", edevID, id, err)
	}
	return answered, cancelled, nil
}

// actorOf shows a recorded attribution, or "unrecorded" at fallbackAt when
// none exists.
func actorOf(a *flowreservation.Attribution, fallbackAt int64) FlowReservationActor {
	if a == nil || a.Kind == "" {
		return FlowReservationActor{Kind: frKindUnrecorded, At: fallbackAt}
	}
	return FlowReservationActor{Kind: a.Kind, Admission: nonEmpty(a.Admission), Principal: nonEmpty(a.Principal), At: a.At}
}

func nonEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// executions fills rv.Executions with every control carrying out the
// response, cancelled ones included, and returns the ones that count toward
// committed energy.
func (h *AdminFlowReservationHandler) executions(ctx context.Context, rv *FlowReservationResponseView) ([]commitment.Control, error) {
	if h.Executions == nil || rv.MRID == "" {
		return nil, nil
	}
	all, err := h.Executions.ExecutionsOf(ctx, rv.MRID)
	if err != nil {
		return nil, fmt.Errorf("executions of %s: %w", rv.MRID, err)
	}
	slices.SortFunc(all, func(a, b commitment.Control) int {
		return cmp.Or(cmp.Compare(a.Window.Start, b.Window.Start), cmp.Compare(a.MRID, b.MRID))
	})
	var live []commitment.Control
	for _, c := range all {
		ev, err := h.executionView(ctx, c)
		if err != nil {
			return nil, err
		}
		rv.Executions = append(rv.Executions, ev)
		if !c.Cancelled {
			live = append(live, c)
		}
	}
	return live, nil
}

func (h *AdminFlowReservationHandler) executionView(ctx context.Context, c commitment.Control) (FlowReservationExecutionView, error) {
	ev := FlowReservationExecutionView{
		MRID:     c.MRID,
		Interval: &frInterval{Start: c.Window.Start, Duration: c.Window.Duration},
		TargetW:  powerOf(c.TargetW),
	}
	if h.Controls == nil {
		return ev, nil
	}
	ctrl, err := h.Controls.Get(ctx, c.Scope, c.ID)
	if err != nil {
		return ev, fmt.Errorf("control %s/%s: %w", c.Scope, c.ID, err)
	}
	ev.Href = ctrl.Href
	if i := strings.LastIndex(ctrl.Href, "/"); i >= 0 {
		ev.DERControlListHref = ctrl.Href[:i]
	}
	ev.EventStatus = eventStatusOf(ctrl.EventStatus)
	return ev, nil
}

// energyFigures returns the energy the live executions commit and what is
// left of the grant, in watt-hours. Both come from commitment.Committed,
// the figure the 409 rule enforces. A response with no executable grant
// (a denial, or one with no energy) has nothing remaining.
func energyFigures(resp sep2.FlowReservationResponse, live []commitment.Control) (committed, remaining *float64) {
	wh := committedWh(live)
	c, _ := wh.Float64()
	committed = &c
	if resp.EnergyAvailable == nil || resp.Interval == nil || resp.Interval.Duration == 0 {
		return committed, nil
	}
	available := scaledMagnitude(resp.EnergyAvailable.Value, resp.EnergyAvailable.Multiplier)
	left, _ := available.Sub(available, wh).Float64()
	return committed, &left
}

// committedWh is commitment.Committed in watt-hours.
func committedWh(live []commitment.Control) *big.Rat {
	return new(big.Rat).Quo(commitment.Committed(live), big.NewRat(3600, 1))
}

// scaledMagnitude is |value| x 10^multiplier, exact.
func scaledMagnitude(value int64, multiplier int8) *big.Rat {
	r := new(big.Rat).SetInt64(value)
	r.Abs(r)
	scale := new(big.Rat).SetInt(new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(absInt8(multiplier))), nil))
	if multiplier >= 0 {
		return r.Mul(r, scale)
	}
	return r.Quo(r, scale)
}

func absInt8(v int8) int {
	if v < 0 {
		return -int(v)
	}
	return int(v)
}
