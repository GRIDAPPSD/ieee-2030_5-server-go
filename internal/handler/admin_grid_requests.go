package handler

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"net/http"
	"slices"
	"strconv"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/commitment"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/dercontrol"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/handlers/sep2time"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

// #1156: a batch of timed grid power requests, each split across the
// available aggregators by nameplate and issued on their fleet FSAs.
//
//	POST /api/derms/grid-requests
//
// Each share is one control carrying opModTargetW, opModConnect and
// opModEnergize true. INFERRED (#1156): the aggregator reads the value as its
// fleet total and splits it across its devices; that is an agreement with the
// aggregator, since 2030.5 has each device apply a copy as its own target.

// AdminGridRequestHandler serves the grid request route. Controls issues the
// shares; Aggregators lists the aggregators; DERs and Capabilities give each
// device's nameplate. All are required.
type AdminGridRequestHandler struct {
	Controls    *AdminFSAControlHandler
	Aggregators interface {
		Managers(ctx context.Context) []string
	}
	DERs         store.ScopedReader[sep2.DER]
	Capabilities store.ScopedReader[sep2.DERCapability]
}

// Bounds of a batch. defaultNameplateW is the weight of a device that
// reports no usable rtgMaxW, a test default the reply warns about.
const (
	maxGridRows          = 24
	maxGridValue         = 999_999_999
	startGraceSeconds    = 60
	defaultNameplateW    = 5000
	gridRequestOp        = "grid_request"
	gridRequestRouteHref = "/api/derms/grid-requests"
)

type gridRequestBody struct {
	Rows []gridRowBody `json:"rows"`
}

// gridRowBody is one block: x * 10^y W in direction for durationSeconds,
// from start, from now when start is absent, or from the end of the row
// before it when followsPrevious is true.
type gridRowBody struct {
	Direction       *string `json:"direction"`
	X               *int64  `json:"x"`
	Y               *int64  `json:"y"`
	Start           *int64  `json:"start"`
	FollowsPrevious *bool   `json:"followsPrevious"`
	DurationSeconds *int64  `json:"durationSeconds"`
}

// GridRowRefusal names one refused row, numbered from 1 as the page shows it.
type GridRowRefusal struct {
	Row           int           `json:"row"`
	Code          string        `json:"code"`
	ConflictsWith *GridConflict `json:"conflictsWith,omitempty"`
}

// GridConflict is what a row overlaps: another row of the batch, or a live
// block of an earlier batch with its window.
type GridConflict struct {
	Row   int    `json:"row"`
	Batch string `json:"batch,omitempty"`
	Start int64  `json:"start,omitempty"`
	End   int64  `json:"end,omitempty"`
}

// GridRequestRefused is the 409 and 422 body. Rows lists every refused row
// and is empty for a refusal of the whole batch.
type GridRequestRefused struct {
	Error string           `json:"error"`
	Code  string           `json:"code"`
	Rows  []GridRowRefusal `json:"rows"`
}

// GridShareView is one aggregator's share of a row. TargetW is nil, and
// Control absent, for a share that encodes to zero, which is not issued.
type GridShareView struct {
	AggregatorLFDI   string          `json:"aggregatorLFDI"`
	FSA              string          `json:"fsa"`
	Program          string          `json:"program"`
	Devices          int             `json:"devices"`
	WeightW          float64         `json:"weightW"`
	DefaultedDevices []string        `json:"defaultedDevices"`
	TargetW          *frScaled       `json:"targetW"`
	Issued           bool            `json:"issued"`
	Control          *FSAControlView `json:"control,omitempty"`
}

// GridRowView is one row as split and issued. RequestedW is signed like
// opModTargetW, and DifferenceW is EncodedW minus RequestedW.
type GridRowView struct {
	Row             int             `json:"row"`
	Direction       string          `json:"direction"`
	RequestedW      float64         `json:"requestedW"`
	EncodedW        float64         `json:"encodedW"`
	DifferenceW     float64         `json:"differenceW"`
	Start           int64           `json:"start"`
	DurationSeconds uint32          `json:"durationSeconds"`
	Shares          []GridShareView `json:"shares"`
}

// GridRequestCreated is the 201 body.
type GridRequestCreated struct {
	Batch                 string        `json:"batch"`
	Rows                  []GridRowView `json:"rows"`
	Warnings              []string      `json:"warnings"`
	NotificationAttempted bool          `json:"notificationAttempted"`
	Persisted             bool          `json:"persisted"`
}

// gridRow is a validated row. A row either starts at start, or offset
// seconds after the batch's first creationTime when relative.
type gridRow struct {
	sign     int
	x        int64
	y        int8
	duration uint32
	relative bool
	start    int64
	offset   int64
}

// gridFleet is one available aggregator and the basis of its weight.
type gridFleet struct {
	lfdi      string
	fsaID     string
	tmplID    string
	devices   []string
	weight    *big.Rat
	defaulted []string
}

// gridShare ties one issued control to its row and fleet.
type gridShare struct {
	row, fleet int
	split      dercontrol.SplitShare
}

func (h *AdminGridRequestHandler) wired(w http.ResponseWriter, r *http.Request) bool {
	if h.Controls == nil || h.Aggregators == nil || h.DERs == nil || h.Capabilities == nil {
		writeError(w, http.StatusInternalServerError, templateInternalError)
		return false
	}
	if !h.Controls.wired(w, r) {
		return false
	}
	if fh := h.Controls.FSAs; fh.Managers == nil || fh.FleetDevices == nil {
		writeError(w, http.StatusInternalServerError, templateInternalError)
		return false
	}
	return true
}

// HandleCreate returns the handler for POST /api/derms/grid-requests.
func (h *AdminGridRequestHandler) HandleCreate() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var logged derControlLog
		if !h.wired(w, r) {
			return
		}
		ch, fh := h.Controls.Controls, h.Controls.FSAs
		var body gridRequestBody
		if ref, ok := decodeDERControlBody(w, r, &body, false); !ok {
			ch.refuse(w, r, gridRequestOp, ref, &logged)
			return
		}
		if len(body.Rows) == 0 || len(body.Rows) > maxGridRows {
			h.refuseBatch(w, r, http.StatusUnprocessableEntity, "rows_count", fmt.Sprintf("rows: give 1 to %d", maxGridRows), nil)
			return
		}
		rows, refused := h.validateRows(body.Rows, sep2time.Now().Unix())
		if len(refused) > 0 {
			h.refuseBatch(w, r, http.StatusUnprocessableEntity, "rows_invalid", "one or more rows are invalid", refused)
			return
		}

		// Held from reading the fleets to the last write, so no assignment
		// and no other batch lands in between.
		fh.programMu.Lock()
		defer fh.programMu.Unlock()
		ctx := r.Context()
		fleets, err := h.availableFleets(r)
		if err != nil {
			fh.templateFailed(w, r, "grid_fleets", err)
			return
		}
		if len(fleets) == 0 {
			h.refuseBatch(w, r, http.StatusConflict, "no_aggregators", "no aggregator with a managed device is available", nil)
			return
		}
		batch, err := newBatchID()
		if err != nil {
			fh.templateFailed(w, r, "grid_batch_id", err)
			return
		}
		shares, refused := splitRows(rows, fleets)
		if len(refused) > 0 {
			h.refuseBatch(w, r, http.StatusUnprocessableEntity, "rows_invalid", "one or more rows are invalid", refused)
			return
		}
		issues := h.gridIssues(batch, rows, fleets, shares)
		res, err := h.Controls.issueAcrossAll(ctx, issues, h.overlapCheck(ctx, shares))
		if err != nil {
			h.issueFailed(w, r, err, shares, &logged)
			return
		}

		for _, a := range res {
			for _, c := range a.Copies {
				ch.notify(ctx, c.Scope)
			}
		}
		out, err := h.created(ctx, batch, rows, fleets, shares, res)
		if err != nil {
			fh.templateFailed(w, r, "record_read", err)
			return
		}
		ch.log(r, slog.LevelInfo, "admin: DERMS grid request created", "der_grid_request_created", []any{
			"batch", batch, "rows", len(rows), "controls", len(res), "aggregators", len(fleets)})
		writeJSON(w, http.StatusCreated, out)
	}
}

// validateRows checks every row and resolves its start, returning every
// refusal. A start up to startGraceSeconds in the past starts now.
func (h *AdminGridRequestHandler) validateRows(body []gridRowBody, now int64) ([]gridRow, []GridRowRefusal) {
	rows := make([]gridRow, len(body))
	var refused []GridRowRefusal
	for n, b := range body {
		row, code := h.validateRow(b, n, rows, now)
		if code != "" {
			refused = append(refused, GridRowRefusal{Row: n + 1, Code: code})
		}
		rows[n] = row
	}
	return rows, refused
}

func (h *AdminGridRequestHandler) validateRow(b gridRowBody, n int, rows []gridRow, now int64) (gridRow, string) {
	var row gridRow
	switch {
	case b.Direction == nil:
		return row, "direction_required"
	case b.X == nil:
		return row, "x_required"
	case b.Y == nil:
		return row, "y_required"
	case b.DurationSeconds == nil:
		return row, "duration_required"
	}
	row.sign = directionSign(*b.Direction)
	switch {
	case row.sign == 0:
		return row, "direction_unknown"
	case *b.X < 0:
		return row, "x_negative"
	case *b.X == 0 || *b.X > maxGridValue:
		return row, "x_range"
	case !dercontrol.ValidMultiplier(*b.Y):
		return row, "y_range"
	case *b.DurationSeconds < 0 || *b.DurationSeconds > int64(^uint32(0)):
		return row, string(dercontrol.RefusalDurationOutOfRange)
	}
	row.x, row.y, row.duration = *b.X, int8(*b.Y), uint32(*b.DurationSeconds)

	follows := b.FollowsPrevious != nil && *b.FollowsPrevious
	switch {
	case follows && b.Start != nil:
		return row, "start_and_follows_previous"
	case follows && n == 0:
		return row, "follows_previous_first_row"
	case follows:
		prev := rows[n-1]
		row.relative, row.start, row.offset = prev.relative, prev.start+int64(prev.duration), prev.offset+int64(prev.duration)
	case b.Start == nil || (*b.Start < now && *b.Start >= now-startGraceSeconds):
		row.relative = true
	case *b.Start < now:
		return row, string(dercontrol.RefusalStartInPast)
	default:
		row.start = *b.Start
	}

	// The issuer's own bounds on duration and an explicit start.
	req := shareRequest(row, sep2.ActivePower{Value: 1, Multiplier: row.y}, "")
	if err := h.Controls.Controls.Issuer.Validate(req); err != nil {
		var refusal *dercontrol.RefusalError
		if errors.As(err, &refusal) {
			return row, string(refusal.Code)
		}
		return row, "invalid"
	}
	return row, ""
}

// directionSign is 1 for the word directionWord gives a discharge, -1 for
// a charge, and 0 for any other word.
func directionSign(word string) int {
	for _, sign := range []int{1, -1} {
		if *directionWord(sign) == word {
			return sign
		}
	}
	return 0
}

// shareRequest is the control a share of row carries.
func shareRequest(row gridRow, target sep2.ActivePower, description string) dercontrol.CreateRequest {
	req := dercontrol.CreateRequest{
		DERProgramHref:  dercontrol.UnscopedProgramHref,
		Functions:       dercontrol.ConnectedTarget(target),
		DurationSeconds: row.duration,
		Description:     description,
		Origin:          dercontrol.OriginDERMS,
	}
	if !row.relative {
		start := row.start
		req.Start = &start
	}
	return req
}

// availableFleets returns every aggregator with an EndDevice record whose
// fleet FSA, once reconciled, reaches at least one device, sorted by LFDI,
// with each one's weight. The caller holds programMu.
func (h *AdminGridRequestHandler) availableFleets(r *http.Request) ([]gridFleet, error) {
	ctx := r.Context()
	fh := h.Controls.FSAs
	aggregators := slices.Clone(h.Aggregators.Managers(ctx))
	slices.Sort(aggregators)
	var out []gridFleet
	for _, lfdi := range aggregators {
		own, err := fh.fleetDeviceID(ctx, lfdi)
		if err != nil {
			return nil, err
		}
		if own == "" {
			continue
		}
		fsaID, err := fh.fleetFSALocked(r, lfdi)
		if err != nil {
			return nil, err
		}
		devices := fh.AdminFSAs.Devices(ctx, fsaID)
		if len(devices) == 0 {
			continue
		}
		templates := fh.AdminFSAs.Templates(ctx, fsaID)
		if len(templates) != 1 {
			return nil, fmt.Errorf("fleet FSA %s has %d programs, want 1", fsaID, len(templates))
		}
		missing, err := h.Controls.missingCopy(ctx, fsaID, templates[0], devices)
		if err != nil {
			return nil, err
		}
		if missing != "" {
			return nil, fmt.Errorf("fleet FSA %s: device %s holds no copy of its program", fsaID, missing)
		}
		f := gridFleet{lfdi: lfdi, fsaID: fsaID, tmplID: templates[0].ID, devices: devices, weight: new(big.Rat)}
		for _, dev := range devices {
			w, err := h.nameplateW(ctx, dev)
			if err != nil {
				return nil, err
			}
			if w == nil {
				w = big.NewRat(defaultNameplateW, 1)
				f.defaulted = append(f.defaulted, dev)
			}
			f.weight.Add(f.weight, w)
		}
		out = append(out, f)
	}
	return out, nil
}

// nameplateW is the sum of the positive rtgMaxW of the device's DERs, or nil
// when none reports one. A read that fails is an error, never a default.
func (h *AdminGridRequestHandler) nameplateW(ctx context.Context, edevID string) (*big.Rat, error) {
	ders, err := h.DERs.List(ctx, edevID, store.ListOptions{Unbounded: true})
	if err != nil {
		return nil, fmt.Errorf("DERs of %s: %w", edevID, err)
	}
	var sum *big.Rat
	for _, der := range ders.Items {
		c, err := h.Capabilities.Get(ctx, edevID+"/"+pathTail(der.Href), singletonKey)
		if errors.Is(err, store.ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("DERCapability of %s: %w", edevID, err)
		}
		if c.RTGMaxW == nil || c.RTGMaxW.Value <= 0 {
			continue
		}
		if sum == nil {
			sum = new(big.Rat)
		}
		sum.Add(sum, scaledMagnitude(int64(c.RTGMaxW.Value), c.RTGMaxW.Multiplier))
	}
	return sum, nil
}

// splitRows splits every row over fleets, returning one share per row and
// fleet in that order.
func splitRows(rows []gridRow, fleets []gridFleet) ([]gridShare, []GridRowRefusal) {
	weights := make([]*big.Rat, len(fleets))
	for n, f := range fleets {
		weights[n] = f.weight
	}
	var shares []gridShare
	var refused []GridRowRefusal
	for n, row := range rows {
		split, err := dercontrol.SplitTarget(row.x, row.y, row.sign, weights)
		if errors.Is(err, dercontrol.ErrShareUnencodable) {
			refused = append(refused, GridRowRefusal{Row: n + 1, Code: "share_out_of_range"})
			continue
		}
		if err != nil {
			refused = append(refused, GridRowRefusal{Row: n + 1, Code: "invalid"})
			continue
		}
		for fn, s := range split {
			shares = append(shares, gridShare{row: n, fleet: fn, split: s})
		}
	}
	return shares, refused
}

// gridIssues is one FSA control per issued share, in share order.
func (h *AdminGridRequestHandler) gridIssues(batch string, rows []gridRow, fleets []gridFleet, shares []gridShare) []fsaIssue {
	var out []fsaIssue
	for _, s := range shares {
		if s.split.TargetW == nil {
			continue
		}
		row, f := rows[s.row], fleets[s.fleet]
		weight, _ := f.weight.Float64()
		basis := &memory.GridShare{
			Batch: batch, Row: s.row + 1, AggregatorLFDI: f.lfdi, Direction: *directionWord(row.sign),
			RequestValue: row.x, RequestMultiplier: row.y, Devices: len(f.devices), WeightW: weight,
			TotalWeightW: totalWeightW(fleets), DefaultedDevices: slices.Clone(f.defaulted), Units: s.split.Units,
		}
		req := shareRequest(row, *s.split.TargetW, "DERMS "+batch+" row "+strconv.Itoa(s.row+1))
		is := fsaIssue{req: req, devices: f.devices, fsaID: f.fsaID, tmplID: f.tmplID,
			record: h.Controls.recorder(f.fsaID, f.tmplID, dercontrol.OriginDERMS, basis)}
		if row.relative {
			is.startOffset = row.offset
		}
		out = append(out, is)
	}
	return out
}

func totalWeightW(fleets []gridFleet) float64 {
	total := new(big.Rat)
	for _, f := range fleets {
		total.Add(total, f.weight)
	}
	v, _ := total.Float64()
	return v
}

// issuedShares is the issued shares of shares, in the order gridIssues
// built their controls.
func issuedShares(shares []gridShare) []gridShare {
	var out []gridShare
	for _, s := range shares {
		if s.split.TargetW != nil {
			out = append(out, s)
		}
	}
	return out
}

// gridRowsConflict is an overlap the check found: every refused row.
type gridRowsConflict struct{ rows []GridRowRefusal }

func (e *gridRowsConflict) Error() string { return "grid request rows overlap" }

// overlapCheck refuses, under the issue's locks, every row whose window
// overlaps an earlier row of the batch or a live block of an earlier batch.
// Windows are half open, so back to back passes.
func (h *AdminGridRequestHandler) overlapCheck(ctx context.Context, shares []gridShare) dercontrol.AcrossCheck {
	issued := issuedShares(shares)
	return func(controls []sep2.DERControl) error {
		windows := map[int]commitment.Window{}
		var order []int
		for n, c := range controls {
			row := issued[n].row
			if _, seen := windows[row]; !seen {
				windows[row] = commitment.Window{Start: c.Interval.Start, Duration: c.Interval.Duration}
				order = append(order, row)
			}
		}
		live := h.liveBlocks(ctx, sep2time.Now().Unix())
		var refused []GridRowRefusal
		for n, row := range order {
			w := windows[row]
			if prev, ok := firstOverlap(w, order[:n], windows); ok {
				refused = append(refused, GridRowRefusal{Row: row + 1, Code: "overlapping_request", ConflictsWith: &GridConflict{Row: prev + 1}})
				continue
			}
			for _, b := range live {
				if b.window.Overlaps(w) {
					refused = append(refused, GridRowRefusal{Row: row + 1, Code: "overlapping_request",
						ConflictsWith: &GridConflict{Batch: b.batch, Row: b.row, Start: b.window.Start, End: b.window.End()}})
					break
				}
			}
		}
		if len(refused) > 0 {
			return &gridRowsConflict{rows: refused}
		}
		return nil
	}
}

func firstOverlap(w commitment.Window, earlier []int, windows map[int]commitment.Window) (int, bool) {
	for _, row := range earlier {
		if windows[row].Overlaps(w) {
			return row, true
		}
	}
	return 0, false
}

// liveBlock is one row of an earlier batch with a live share.
type liveBlock struct {
	batch  string
	row    int
	window commitment.Window
}

// liveBlocks lists the rows of earlier batches with a share still live at
// now, on any fleet FSA, once per row.
func (h *AdminGridRequestHandler) liveBlocks(ctx context.Context, now int64) []liveBlock {
	admin := h.Controls.FSAs.AdminFSAs
	seen := map[[2]string]bool{}
	var out []liveBlock
	for _, fsaID := range admin.IDs(ctx) {
		if admin.FleetAggregator(fsaID) == "" {
			continue
		}
		for _, t := range admin.Templates(ctx, fsaID) {
			for _, rec := range admin.FSAControls(ctx, fsaID, t.ID) {
				g := rec.GridShare
				if g == nil || !rec.Live(now) {
					continue
				}
				key := [2]string{g.Batch, strconv.Itoa(g.Row)}
				if seen[key] {
					continue
				}
				seen[key] = true
				out = append(out, liveBlock{batch: g.Batch, row: g.Row, window: commitment.Window{Start: rec.Start, Duration: rec.Duration}})
			}
		}
	}
	return out
}

// issueFailed answers a batch the issuer did not write, naming every row it
// refused. A failure that is not a refusal goes to the control handler's
// answer for a failed create.
func (h *AdminGridRequestHandler) issueFailed(w http.ResponseWriter, r *http.Request, err error, shares []gridShare, logged *derControlLog) {
	var overlap *gridRowsConflict
	if errors.As(err, &overlap) {
		h.refuseBatch(w, r, http.StatusConflict, "rows_conflict", "one or more rows overlap a scheduled block", overlap.rows)
		return
	}
	items, ok := err.(dercontrol.AcrossItemErrors)
	if !ok {
		h.failed(w, r, err, logged)
		return
	}
	issued := issuedShares(shares)
	status := http.StatusConflict
	var refused []GridRowRefusal
	for _, it := range items {
		row := issued[it.Item].row + 1
		var conflict *commitment.ConflictError
		var refusal *dercontrol.RefusalError
		switch {
		case errors.As(it.Err, &conflict):
			if _, known := conflictMessages[conflict.Code]; !known {
				h.failed(w, r, it.Err, logged)
				return
			}
			refused = appendRowRefusal(refused, GridRowRefusal{Row: row, Code: string(conflict.Code)})
		case errors.As(it.Err, &refusal):
			status = http.StatusUnprocessableEntity
			refused = appendRowRefusal(refused, GridRowRefusal{Row: row, Code: string(refusal.Code)})
		default:
			h.failed(w, r, it.Err, logged)
			return
		}
	}
	h.refuseBatch(w, r, status, "rows_refused", "one or more rows were refused", refused)
}

// appendRowRefusal adds ref unless its row is already named: every share of
// a row is refused for the same reason.
func appendRowRefusal(refused []GridRowRefusal, ref GridRowRefusal) []GridRowRefusal {
	if slices.ContainsFunc(refused, func(o GridRowRefusal) bool { return o.Row == ref.Row }) {
		return refused
	}
	return append(refused, ref)
}

// failed answers a batch that stopped for a reason other than a refusal.
func (h *AdminGridRequestHandler) failed(w http.ResponseWriter, r *http.Request, err error, logged *derControlLog) {
	var undo *dercontrol.UndoError
	if errors.As(err, &undo) && undo.Step == dercontrol.UndoStepStoreRecord {
		if fsaID, rec, ok := h.Controls.FSAs.AdminFSAs.FSAControlByMRID(undo.MRID); ok {
			h.Controls.recordKept(w, r, "/api/fsas/"+fsaID+"/der-programs/"+rec.TemplateID+"/controls", undo)
			return
		}
	}
	h.Controls.Controls.issueFailed(w, r, gridRequestOp, err, "", gridRequestRouteHref, logged)
}

// refuseBatch answers a refusal of the batch and writes its one log line.
func (h *AdminGridRequestHandler) refuseBatch(w http.ResponseWriter, r *http.Request, status int, code, message string, rows []GridRowRefusal) {
	if rows == nil {
		rows = []GridRowRefusal{}
	}
	var l derControlLog
	l.add("rows_refused", len(rows))
	logRefusal(h.Controls.Controls.logger(), r, refusedEvent(gridRequestOp), derControlRefusal{status: status, code: code, message: message}, &l)
	writeJSON(w, status, GridRequestRefused{Error: message, Code: code, Rows: rows})
}

// created is the 201 body: every row with every share, issued or not.
func (h *AdminGridRequestHandler) created(ctx context.Context, batch string, rows []gridRow, fleets []gridFleet, shares []gridShare, res []dercontrol.AcrossResult) (GridRequestCreated, error) {
	fh := h.Controls.FSAs
	ch := h.Controls.Controls
	now := sep2time.Now().Unix()
	out := GridRequestCreated{Batch: batch, Warnings: []string{}, NotificationAttempted: ch.Notifier != nil,
		Persisted: ch.Persisted && fh.AdminFSAs.Persists()}
	defaulted := 0
	for _, f := range fleets {
		defaulted += len(f.defaulted)
	}
	if defaulted > 0 {
		out.Warnings = append(out.Warnings, fmt.Sprintf("%d devices used the 5 kW test default (no nameplate reported)", defaulted))
	}
	views := make([]GridRowView, len(rows))
	for n, row := range rows {
		requested := scaledFloat(int64(row.sign)*row.x, row.y)
		views[n] = GridRowView{Row: n + 1, Direction: *directionWord(row.sign), RequestedW: requested,
			DurationSeconds: row.duration, Shares: []GridShareView{}}
	}
	next := 0
	for _, s := range shares {
		f := fleets[s.fleet]
		weight, _ := f.weight.Float64()
		sv := GridShareView{AggregatorLFDI: f.lfdi, FSA: f.fsaID, Program: f.tmplID, Devices: len(f.devices),
			WeightW: weight, DefaultedDevices: append([]string{}, f.defaulted...)}
		v := &views[s.row]
		if t := s.split.TargetW; t != nil {
			a := res[next]
			next++
			rec, err := fh.AdminFSAs.FSAControl(ctx, f.fsaID, f.tmplID, a.ID)
			if err != nil {
				return GridRequestCreated{}, err
			}
			view := h.Controls.view(ctx, f.fsaID, rec, f.devices, now)
			sv.TargetW, sv.Issued, sv.Control = &frScaled{Value: int64(t.Value), Multiplier: t.Multiplier}, true, &view
			v.EncodedW += scaledFloat(int64(t.Value), t.Multiplier)
			v.Start = rec.Start
		}
		v.Shares = append(v.Shares, sv)
	}
	for n := range views {
		views[n].DifferenceW = views[n].EncodedW - views[n].RequestedW
	}
	out.Rows = views
	return out, nil
}

// newBatchID mints a batch id: 12 hex digits, short enough that a control
// description naming it fits String32.
func newBatchID() (string, error) {
	b := make([]byte, 6)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
