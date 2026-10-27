package handler

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/commitment"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/dercontrol"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/derhref"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/fsaprogram"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/logsafe"
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
	// MaxDeviceW caps one device's nameplate as a split weight; a device
	// reporting more is weighted at the test default. It must be positive.
	MaxDeviceW int64
}

// Bounds of a batch. defaultNameplateW is the weight of a device that
// reports no usable rtgMaxW, a test default the reply warns about.
const (
	maxGridRows          = 24
	maxGridValue         = 999_999_999
	startGraceSeconds    = 60
	defaultNameplateW    = 5000
	batchIDBytes         = 6 // 12 hex digits, so a description naming it fits String32
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
	// AggregatorLFDI names the aggregator whose share of the row is refused,
	// when the refusal is that aggregator's.
	AggregatorLFDI string `json:"aggregatorLFDI,omitempty"`
}

// GridAggregatorRefusal is an aggregator a batch cannot reach: a device of
// its fleet that cannot be assigned to its fleet FSA, or holds no copy.
type GridAggregatorRefusal struct {
	AggregatorLFDI string `json:"aggregatorLFDI"`
	Device         string `json:"device"`
	Code           string `json:"code"`
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
	// Aggregators lists every aggregator refused, for aggregator_unavailable.
	Aggregators []GridAggregatorRefusal `json:"aggregators,omitempty"`
	// Devices lists every device the batch would take past 24 live events.
	Devices []GridDeviceEvents `json:"devices,omitempty"`
}

// GridShareView is one aggregator's share of a row. TargetW is nil, and
// Control absent, for a share that encodes to zero, which is not issued.
type GridShareView struct {
	AggregatorLFDI   string   `json:"aggregatorLFDI"`
	FSA              string   `json:"fsa"`
	Program          string   `json:"program"`
	Devices          int      `json:"devices"`
	WeightW          float64  `json:"weightW"`
	DefaultedDevices []string `json:"defaultedDevices"`
	// OverCapDevices are weighted at the default because their rtgMaxW is
	// over the per-device cap; DefaultedDevices reported none.
	OverCapDevices []string        `json:"overCapDevices"`
	TargetW        *frScaled       `json:"targetW"`
	Issued         bool            `json:"issued"`
	Control        *FSAControlView `json:"control,omitempty"`
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
// devices are the managed devices its shares reach; assigned are those
// already on its fleet FSA with a copy of its program, which a check before
// any write can target. fsaID and tmplID are empty until the FSA exists.
type gridFleet struct {
	lfdi      string
	fsaID     string
	tmplID    string
	devices   []string
	assigned  []string
	weight    *big.Rat
	defaulted []string
	overCap   []string
}

// gridShare ties one issued control to its row and fleet.
type gridShare struct {
	row, fleet int
	split      dercontrol.SplitShare
}

func (h *AdminGridRequestHandler) wired(w http.ResponseWriter, r *http.Request) bool {
	if h.Controls == nil || h.Aggregators == nil || h.DERs == nil || h.Capabilities == nil || h.MaxDeviceW <= 0 {
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
		fleets, unavailable, skipped, err := h.readFleets(ctx)
		if err != nil {
			fh.templateFailed(w, r, "grid_fleets", err)
			return
		}
		if len(fleets) == 0 {
			h.refuseBatch(w, r, http.StatusConflict, "no_aggregators", "no aggregator with a managed device is available", nil)
			return
		}
		batch := newHexID(batchIDBytes)
		shares, refused := splitRows(rows, fleets)
		if len(refused) > 0 {
			h.refuseBatch(w, r, http.StatusUnprocessableEntity, "rows_invalid", "one or more rows are invalid", refused)
			return
		}
		if len(unavailable) > 0 {
			h.refuseUnavailable(w, r, unavailable, fleets, shares)
			return
		}

		// Every check runs before the fleet FSAs are reconciled, so a refused
		// batch assigns and unassigns nothing.
		check := h.overlapCheck(ctx, fleets, shares)
		if err := h.Controls.planAcrossAll(ctx, h.gridIssues(batch, rows, fleets, shares, true), check); err != nil {
			h.issueFailed(w, r, batch, err, fleets, shares, &logged)
			return
		}
		unavailable, err = h.reconcileFleets(r, fleets)
		if err != nil {
			fh.templateFailed(w, r, "grid_reconcile", err)
			return
		}
		if len(unavailable) > 0 {
			h.refuseUnavailable(w, r, unavailable, fleets, shares)
			return
		}
		res, err := h.Controls.issueAcrossAll(ctx, h.gridIssues(batch, rows, fleets, shares, false), check)
		if err != nil {
			h.issueFailed(w, r, batch, err, fleets, shares, &logged)
			return
		}

		h.Controls.notifyCopies(ctx, res...)
		recs := make([]memory.FSAControl, len(res))
		for n, s := range issuedShares(shares) {
			f := fleets[s.fleet]
			var ok bool
			if recs[n], ok = h.Controls.readRecord(w, r, f.fsaID, f.tmplID, res[n].ID); !ok {
				return
			}
		}
		out := h.created(ctx, batch, rows, fleets, shares, recs)
		out.Warnings = append(out.Warnings, skipped...)
		h.logCreated(r, out)
		writeJSON(w, http.StatusCreated, out)
	}
}

// validateRows checks every row and resolves its start, returning every
// refusal. A start up to startGraceSeconds in the past starts now.
func (h *AdminGridRequestHandler) validateRows(body []gridRowBody, now int64) ([]gridRow, []GridRowRefusal) {
	rows := make([]gridRow, len(body))
	var refused []GridRowRefusal
	for n, b := range body {
		prevInvalid := len(refused) > 0 && refused[len(refused)-1].Row == n
		row, code := h.validateRow(b, n, rows, prevInvalid, now)
		if code != "" {
			refused = append(refused, GridRowRefusal{Row: n + 1, Code: code})
		}
		rows[n] = row
	}
	return rows, refused
}

// validateRow checks row n. prevInvalid says row n-1 was refused, so a row
// chained to it has no start to resolve from.
func (h *AdminGridRequestHandler) validateRow(b gridRowBody, n int, rows []gridRow, prevInvalid bool, now int64) (gridRow, string) {
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
	case follows && prevInvalid:
		return row, "follows_invalid"
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

// readFleets returns every aggregator with an EndDevice record that manages
// at least one device with one, sorted by LFDI, with each one's weight, and
// the devices of those fleets that cannot be assigned or hold no copy. It
// writes nothing. skipped says why each other aggregator takes no share.
// The caller holds programMu.
func (h *AdminGridRequestHandler) readFleets(ctx context.Context) ([]gridFleet, []GridAggregatorRefusal, []string, error) {
	fh := h.Controls.FSAs
	aggregators := slices.Clone(h.Aggregators.Managers(ctx))
	slices.Sort(aggregators)
	var out []gridFleet
	var unavailable []GridAggregatorRefusal
	var skipped []string
	for _, lfdi := range aggregators {
		own, err := fh.fleetDeviceID(ctx, lfdi)
		if err != nil {
			return nil, nil, nil, err
		}
		if own == "" {
			skipped = append(skipped, "aggregator "+lfdi+" skipped: it has no EndDevice record")
			continue
		}
		want, err := fh.fleetWant(ctx, lfdi)
		if err != nil {
			return nil, nil, nil, err
		}
		if len(want) == 0 {
			skipped = append(skipped, "aggregator "+lfdi+" skipped: it manages no device with an EndDevice record")
			continue
		}
		f := gridFleet{lfdi: lfdi, devices: want, weight: new(big.Rat)}
		refused, err := h.readAssigned(ctx, &f)
		if err != nil {
			return nil, nil, nil, err
		}
		unavailable = append(unavailable, refused...)
		for _, dev := range want {
			w, over, err := h.nameplateW(ctx, dev)
			if err != nil {
				return nil, nil, nil, err
			}
			switch {
			case over:
				f.overCap = append(f.overCap, dev)
				w = big.NewRat(defaultNameplateW, 1)
			case w == nil:
				f.defaulted = append(f.defaulted, dev)
				w = big.NewRat(defaultNameplateW, 1)
			}
			f.weight.Add(f.weight, w)
		}
		out = append(out, f)
	}
	return out, unavailable, skipped, nil
}

// readAssigned fills f's fleet FSA, program and assigned devices when the FSA
// exists, and returns each of f's devices that holds no copy of the program
// though assigned, or holds a program the copy would clash with.
func (h *AdminGridRequestHandler) readAssigned(ctx context.Context, f *gridFleet) ([]GridAggregatorRefusal, error) {
	admin := h.Controls.FSAs.AdminFSAs
	fsaID, ok := admin.FleetFSA(f.lfdi)
	if !ok {
		return nil, nil
	}
	templates := admin.Templates(ctx, fsaID)
	switch len(templates) {
	case 0:
		// The FSA's program is written when it is reconciled.
		return nil, nil
	case 1:
	default:
		return nil, fmt.Errorf("fleet FSA %s has %d programs, want 1", fsaID, len(templates))
	}
	f.fsaID, f.tmplID = fsaID, templates[0].ID
	assigned := admin.Devices(ctx, fsaID)
	var refused []GridAggregatorRefusal
	for _, dev := range f.devices {
		present, err := h.Controls.FSAs.Copies.Check(ctx, dev, fsaID, templates[0])
		isAssigned := slices.Contains(assigned, dev)
		switch {
		case errors.Is(err, fsaprogram.ErrCopyConflict):
			refused = append(refused, GridAggregatorRefusal{AggregatorLFDI: f.lfdi, Device: dev, Code: "device_not_assignable"})
		case err != nil:
			return nil, err
		case isAssigned && present:
			f.assigned = append(f.assigned, dev)
		case isAssigned:
			refused = append(refused, GridAggregatorRefusal{AggregatorLFDI: f.lfdi, Device: dev, Code: "device_without_copy"})
		}
	}
	return refused, nil
}

// reconcileFleets makes each fleet FSA, and its assignment, match the
// fleets as read, returning every device that could not be assigned or
// unassigned. The caller holds programMu.
func (h *AdminGridRequestHandler) reconcileFleets(r *http.Request, fleets []gridFleet) ([]GridAggregatorRefusal, error) {
	fh := h.Controls.FSAs
	var refused []GridAggregatorRefusal
	for n := range fleets {
		f := &fleets[n]
		fsaID, err := fh.ensureFleetFSA(r, f.lfdi)
		if err != nil {
			return nil, err
		}
		err = fh.reconcileFleet(r, fsaID, f.lfdi)
		devErrs, other := splitFleetDeviceErrors(err)
		if other != nil {
			return nil, other
		}
		for _, d := range devErrs {
			refused = append(refused, GridAggregatorRefusal{AggregatorLFDI: f.lfdi, Device: d.Device, Code: "device_not_" + d.Op + "able"})
		}
		templates := fh.AdminFSAs.Templates(r.Context(), fsaID)
		if len(templates) != 1 {
			return nil, fmt.Errorf("fleet FSA %s has %d programs, want 1", fsaID, len(templates))
		}
		f.fsaID, f.tmplID = fsaID, templates[0].ID
		if len(devErrs) > 0 {
			continue
		}
		missing, err := h.Controls.missingCopy(r.Context(), fsaID, templates[0], f.devices)
		if err != nil {
			return nil, err
		}
		if missing != "" {
			refused = append(refused, GridAggregatorRefusal{AggregatorLFDI: f.lfdi, Device: missing, Code: "device_without_copy"})
		}
	}
	return refused, nil
}

// splitFleetDeviceErrors separates the device failures of a reconcile from
// any other error it joined.
func splitFleetDeviceErrors(err error) ([]*FleetDeviceError, error) {
	if err == nil {
		return nil, nil
	}
	errs := []error{err}
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		errs = joined.Unwrap()
	}
	var devs []*FleetDeviceError
	var other []error
	for _, e := range errs {
		var d *FleetDeviceError
		if errors.As(e, &d) {
			devs = append(devs, d)
			continue
		}
		other = append(other, e)
	}
	return devs, errors.Join(other...)
}

// refuseUnavailable answers 409 for a batch an aggregator cannot take,
// naming each such device and each row with a share on its aggregator.
func (h *AdminGridRequestHandler) refuseUnavailable(w http.ResponseWriter, r *http.Request, unavailable []GridAggregatorRefusal, fleets []gridFleet, shares []gridShare) {
	var rows []GridRowRefusal
	for _, s := range issuedShares(shares) {
		lfdi := fleets[s.fleet].lfdi
		if slices.ContainsFunc(unavailable, func(u GridAggregatorRefusal) bool { return u.AggregatorLFDI == lfdi }) {
			rows = append(rows, GridRowRefusal{Row: s.row + 1, Code: "aggregator_unavailable", AggregatorLFDI: lfdi})
		}
	}
	h.writeRefusal(w, r, http.StatusConflict, GridRequestRefused{Error: "an aggregator's fleet cannot take this batch", Code: "aggregator_unavailable", Rows: rows, Aggregators: unavailable})
}

// nameplateW is the sum of the positive rtgMaxW of the device's DERs, or nil
// when none reports one; over reports a sum above MaxDeviceW. A read that
// fails is an error, never a default.
func (h *AdminGridRequestHandler) nameplateW(ctx context.Context, edevID string) (sum *big.Rat, over bool, err error) {
	ders, err := h.DERs.List(ctx, edevID, store.ListOptions{Unbounded: true})
	if err != nil {
		return nil, false, fmt.Errorf("DERs of %s: %w", edevID, err)
	}
	for _, der := range ders.Items {
		c, err := h.Capabilities.Get(ctx, edevID+"/"+pathTail(der.Href), singletonKey)
		if errors.Is(err, store.ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, false, fmt.Errorf("DERCapability of %s: %w", edevID, err)
		}
		if c.RTGMaxW == nil || c.RTGMaxW.Value <= 0 {
			continue
		}
		if sum == nil {
			sum = new(big.Rat)
		}
		sum.Add(sum, scaledMagnitude(int64(c.RTGMaxW.Value), c.RTGMaxW.Multiplier))
	}
	return sum, sum != nil && sum.Cmp(big.NewRat(h.MaxDeviceW, 1)) > 0, nil
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

// gridIssues is one FSA control per issued share, in share order. A check
// run targets only the devices already assigned, since nothing is assigned
// before every check passes.
func (h *AdminGridRequestHandler) gridIssues(batch string, rows []gridRow, fleets []gridFleet, shares []gridShare, check bool) []fsaIssue {
	var out []fsaIssue
	for _, s := range issuedShares(shares) {
		row, f := rows[s.row], fleets[s.fleet]
		weight, _ := f.weight.Float64()
		basis := &memory.GridShare{
			Batch: batch, Row: s.row + 1, AggregatorLFDI: f.lfdi, Direction: *directionWord(row.sign),
			RequestValue: row.x, RequestMultiplier: row.y, Devices: len(f.devices), WeightW: weight,
			TotalWeightW: totalWeightW(fleets), DefaultedDevices: slices.Clone(f.defaulted),
			OverCapDevices: slices.Clone(f.overCap), Units: s.split.Units,
		}
		req := shareRequest(row, *s.split.TargetW, "DERMS "+batch+" row "+strconv.Itoa(s.row+1))
		is := fsaIssue{req: req, devices: f.devices, fsaID: f.fsaID, tmplID: f.tmplID, fleetTotal: true,
			record: h.Controls.recorder(f.fsaID, f.tmplID, dercontrol.OriginDERMS, basis)}
		if check {
			is.devices = f.assigned
		}
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

// issued reports whether s is written as a control: a share that encodes
// to zero is listed and not issued.
func (s gridShare) issued() bool { return s.split.TargetW != nil }

// issuedShares is the issued shares of shares, in the order gridIssues
// built their controls.
func issuedShares(shares []gridShare) []gridShare {
	var out []gridShare
	for _, s := range shares {
		if s.issued() {
			out = append(out, s)
		}
	}
	return out
}

// gridRowsConflict is what the check refused: every row, and every device a
// batch would take past maxLiveEvents.
type gridRowsConflict struct {
	rows    []GridRowRefusal
	devices []GridDeviceEvents
}

func (e *gridRowsConflict) Error() string { return "grid request rows overlap" }

// maxLiveEvents is how many scheduled events a device must be able to hold:
// CSIP v2 has an aggregator store at least 24 per DER, so a batch never takes
// a device past 24 live controls of any origin.
const maxLiveEvents = 24

// GridDeviceEvents is a device a batch would take past maxLiveEvents.
type GridDeviceEvents struct {
	Device         string `json:"device"`
	AggregatorLFDI string `json:"aggregatorLFDI"`
	Live           int    `json:"live"`
	Added          int    `json:"added"`
}

// overlapCheck refuses, under the issue's locks, every row whose window
// overlaps an earlier row of the batch or a live block of an earlier batch,
// and every row whose shares would take a device past maxLiveEvents.
// Windows are half open, so back to back passes. An item the issuer refused
// before resolving it has no interval and is skipped.
func (h *AdminGridRequestHandler) overlapCheck(ctx context.Context, fleets []gridFleet, shares []gridShare) dercontrol.AcrossCheck {
	issued := issuedShares(shares)
	return func(controls []sep2.DERControl) error {
		windows := map[int]commitment.Window{}
		var order []int
		for n, c := range controls {
			row := issued[n].row
			if _, seen := windows[row]; seen || c.Interval == nil {
				continue
			}
			windows[row] = commitment.Window{Start: c.Interval.Start, Duration: c.Interval.Duration}
			order = append(order, row)
		}
		now := sep2time.Now().Unix()
		live := h.liveBlocks(ctx, now)
		conflict := &gridRowsConflict{}
		for n, row := range order {
			w := windows[row]
			if prev, ok := firstOverlap(w, order[:n], windows); ok {
				conflict.rows = append(conflict.rows, GridRowRefusal{Row: row + 1, Code: "overlapping_request", ConflictsWith: &GridConflict{Row: prev + 1}})
				continue
			}
			for _, b := range live {
				if b.window.Overlaps(w) {
					conflict.rows = append(conflict.rows, GridRowRefusal{Row: row + 1, Code: "overlapping_request",
						ConflictsWith: &GridConflict{Batch: b.batch, Row: b.row, Start: b.window.Start, End: b.window.End()}})
					break
				}
			}
		}
		if err := h.checkEvents(ctx, now, fleets, issued, conflict); err != nil {
			return err
		}
		if len(conflict.rows) > 0 {
			return conflict
		}
		return nil
	}
}

// checkEvents adds to conflict every device the issued shares would take
// past maxLiveEvents, and each row with a share on its fleet.
func (h *AdminGridRequestHandler) checkEvents(ctx context.Context, now int64, fleets []gridFleet, issued []gridShare, conflict *gridRowsConflict) error {
	for fn, f := range fleets {
		var rows []int
		for _, s := range issued {
			if s.fleet == fn {
				rows = append(rows, s.row)
			}
		}
		if len(rows) == 0 {
			continue
		}
		over := false
		for _, dev := range f.devices {
			n, err := h.liveEvents(ctx, dev, now)
			if err != nil {
				return err
			}
			if n+len(rows) > maxLiveEvents {
				conflict.devices = append(conflict.devices, GridDeviceEvents{Device: dev, AggregatorLFDI: f.lfdi, Live: n, Added: len(rows)})
				over = true
			}
		}
		if !over {
			continue
		}
		for _, row := range rows {
			conflict.rows = appendRowRefusal(conflict.rows, GridRowRefusal{Row: row + 1, Code: "too_many_events", AggregatorLFDI: f.lfdi})
		}
	}
	slices.SortStableFunc(conflict.rows, func(a, b GridRowRefusal) int { return a.Row - b.Row })
	return nil
}

// liveEvents counts the controls stored under device dev, in any program and
// of any origin, that are neither ended nor cancelled at now.
func (h *AdminGridRequestHandler) liveEvents(ctx context.Context, dev string, now int64) (int, error) {
	ch := h.Controls.Controls
	parents, err := ch.Controls.Parents(ctx)
	if err != nil {
		return 0, fmt.Errorf("control scopes: %w", err)
	}
	n := 0
	for _, scope := range parents {
		if !strings.HasPrefix(scope, dev+"/") {
			continue
		}
		list, err := ch.Controls.List(ctx, scope, store.ListOptions{Unbounded: true})
		if err != nil {
			return 0, fmt.Errorf("controls of %s: %w", scope, err)
		}
		for _, c := range list.Items {
			if c.Interval == nil || dercontrol.IntervalEnded(c.Interval, now) {
				continue
			}
			if id, ok := derhref.ControlID(c.Href); ok {
				lc, err := ch.Lifecycles.Get(ctx, scope, id)
				switch {
				case err == nil && lc.CancelledAt != nil:
					continue
				case err != nil && !errors.Is(err, store.ErrNotFound):
					return 0, fmt.Errorf("lifecycle of %s/%s: %w", scope, id, err)
				}
			}
			n++
		}
	}
	return n, nil
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

// issueFailed answers a batch the issuer did not write: every row refused,
// by the overlap check and by the issuer together, or the controls an undo
// could not remove. Any other failure goes to the control handler's answer
// for a failed create.
func (h *AdminGridRequestHandler) issueFailed(w http.ResponseWriter, r *http.Request, batch string, err error, fleets []gridFleet, shares []gridShare, logged *derControlLog) {
	issued := issuedShares(shares)
	var undoAll *dercontrol.AcrossUndoError
	var undo *dercontrol.UndoError
	switch {
	case errors.As(err, &undoAll):
		h.incomplete(w, r, batch, undoAll.Kept, fleets, issued)
		return
	case errors.As(err, &undo) && len(issued) == 1:
		h.incomplete(w, r, batch, dercontrol.AcrossItemErrors{{Item: 0, Err: undo}}, fleets, issued)
		return
	}
	var overlap *gridRowsConflict
	var items dercontrol.AcrossItemErrors
	hasOverlap, hasItems := errors.As(err, &overlap), errors.As(err, &items)
	if !hasOverlap && !hasItems {
		h.failed(w, r, err, logged)
		return
	}
	var refused []GridRowRefusal
	var devices []GridDeviceEvents
	status, code := http.StatusConflict, "rows_conflict"
	if hasOverlap {
		refused = append(refused, overlap.rows...)
		devices = overlap.devices
		if len(devices) > 0 && !slices.ContainsFunc(refused, func(r GridRowRefusal) bool { return r.Code != "too_many_events" }) {
			code = "too_many_events"
		}
	}
	for _, it := range items {
		row := issued[it.Item].row + 1
		var conflict *commitment.ConflictError
		var refusal *dercontrol.RefusalError
		switch {
		case errors.Is(it.Err, dercontrol.ErrAcrossOverlap):
			// Two rows of the batch overlap; the check names them.
			refused = appendRowRefusal(refused, GridRowRefusal{Row: row, Code: "overlapping_request"})
			continue
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
		code = "rows_refused"
	}
	slices.SortStableFunc(refused, func(a, b GridRowRefusal) int { return a.Row - b.Row })
	h.writeRefusal(w, r, status, GridRequestRefused{Error: "one or more rows were refused", Code: code, Rows: refused, Devices: devices})
}

// appendRowRefusal adds ref unless its row is already named: every share of
// a row is refused for the same reason.
func appendRowRefusal(refused []GridRowRefusal, ref GridRowRefusal) []GridRowRefusal {
	if slices.ContainsFunc(refused, func(o GridRowRefusal) bool { return o.Row == ref.Row }) {
		return refused
	}
	return append(refused, ref)
}

// failed answers a batch that stopped for a reason other than a refusal or
// a failed undo, which wrote nothing that remains.
func (h *AdminGridRequestHandler) failed(w http.ResponseWriter, r *http.Request, err error, logged *derControlLog) {
	h.Controls.Controls.issueFailed(w, r, gridRequestOp, err, "", gridRequestRouteHref, logged)
}

// GridKeptControl is a share an undo could not remove. OnDevices is true when
// a device may hold a copy; the record is kept either way, held from
// replication, and the cancel href ends it.
type GridKeptControl struct {
	Row            int    `json:"row"`
	AggregatorLFDI string `json:"aggregatorLFDI"`
	FSA            string `json:"fsa"`
	Program        string `json:"program"`
	ID             string `json:"id"`
	MRID           string `json:"mRID"`
	OnDevices      bool   `json:"onDevices"`
	CancelHref     string `json:"cancelHref"`
}

// GridRequestIncomplete is the 500 body of a batch that failed and could not
// undo every share.
type GridRequestIncomplete struct {
	Error string            `json:"error"`
	Batch string            `json:"batch"`
	Kept  []GridKeptControl `json:"kept"`
}

// incomplete answers a batch whose undo left the kept shares: each is named,
// held from replication, and its devices notified when a copy may remain.
func (h *AdminGridRequestHandler) incomplete(w http.ResponseWriter, r *http.Request, batch string, kept dercontrol.AcrossItemErrors, fleets []gridFleet, issued []gridShare) {
	ctx := r.Context()
	ch, admin := h.Controls.Controls, h.Controls.FSAs.AdminFSAs
	out := GridRequestIncomplete{
		Error: "the batch failed and not every share could be undone; each listed share is kept and not sent to devices assigned later. " +
			"Devices may hold those with onDevices true. Cancel each with its cancelHref.",
		Batch: batch,
		Kept:  []GridKeptControl{},
	}
	var mrids []string
	onDevices := 0
	for _, it := range kept {
		var undo *dercontrol.UndoError
		if !errors.As(it.Err, &undo) {
			continue
		}
		s := issued[it.Item]
		f := fleets[s.fleet]
		admin.HoldFSAControl(f.fsaID, f.tmplID, undo.ID)
		if undo.ControlKept {
			onDevices++
			for _, dev := range f.devices {
				ch.notify(ctx, dercontrol.Scope{EndDeviceID: dev, FSAID: f.fsaID, DERProgramID: f.tmplID})
			}
		}
		route := "/api/fsas/" + f.fsaID + "/der-programs/" + f.tmplID + "/controls/" + undo.ID + "/cancel"
		out.Kept = append(out.Kept, GridKeptControl{Row: s.row + 1, AggregatorLFDI: f.lfdi, FSA: f.fsaID, Program: f.tmplID,
			ID: undo.ID, MRID: undo.MRID, OnDevices: undo.ControlKept, CancelHref: route})
		mrids = append(mrids, logsafe.Str(undo.MRID))
	}
	ch.log(r, slog.LevelError, "admin: DERMS grid request left shares after a failed undo", "der_grid_request_incomplete", []any{
		"code", refuseInternal.code, "batch", logsafe.Str(batch), "kept_mrids", mrids, "on_devices", onDevices})
	writeJSON(w, http.StatusInternalServerError, out)
}

// refuseBatch answers a refusal of the batch and writes its one log line.
func (h *AdminGridRequestHandler) refuseBatch(w http.ResponseWriter, r *http.Request, status int, code, message string, rows []GridRowRefusal) {
	h.writeRefusal(w, r, status, GridRequestRefused{Error: message, Code: code, Rows: rows})
}

// writeRefusal writes body with status and its one log line.
func (h *AdminGridRequestHandler) writeRefusal(w http.ResponseWriter, r *http.Request, status int, body GridRequestRefused) {
	if body.Rows == nil {
		body.Rows = []GridRowRefusal{}
	}
	var l derControlLog
	l.add("rows_refused", len(body.Rows))
	l.add("aggregators_refused", len(body.Aggregators))
	logRefusal(h.Controls.Controls.logger(), r, refusedEvent(gridRequestOp), derControlRefusal{status: status, code: body.Code, message: body.Error}, &l)
	writeJSON(w, status, body)
}

// created is the 201 body: every row with every share, issued or not.
// recs are the records of the issued shares, in issued order.
func (h *AdminGridRequestHandler) created(ctx context.Context, batch string, rows []gridRow, fleets []gridFleet, shares []gridShare, recs []memory.FSAControl) GridRequestCreated {
	now := sep2time.Now().Unix()
	notified, persisted := h.Controls.createdFlags()
	out := GridRequestCreated{Batch: batch, Warnings: h.warnings(rows, fleets), NotificationAttempted: notified, Persisted: persisted}
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
			WeightW: weight, DefaultedDevices: append([]string{}, f.defaulted...), OverCapDevices: append([]string{}, f.overCap...)}
		v := &views[s.row]
		if s.issued() {
			rec := recs[next]
			next++
			t := s.split.TargetW
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
	return out
}

// warnings are what a reply says about the split's basis: devices at the
// test default, and rows asking for more than the fleets' nameplate.
func (h *AdminGridRequestHandler) warnings(rows []gridRow, fleets []gridFleet) []string {
	out := []string{}
	var defaulted, overCap []string
	total := new(big.Rat)
	for _, f := range fleets {
		defaulted = append(defaulted, f.defaulted...)
		overCap = append(overCap, f.overCap...)
		total.Add(total, f.weight)
	}
	if len(defaulted) > 0 {
		out = append(out, fmt.Sprintf("%d devices used the 5 kW test default (no nameplate reported)", len(defaulted)))
	}
	if len(overCap) > 0 {
		out = append(out, fmt.Sprintf("%d devices used the 5 kW test default (nameplate ignored, over cap of %d W): %s",
			len(overCap), h.MaxDeviceW, strings.Join(overCap, ", ")))
	}
	totalW, _ := total.Float64()
	for n, row := range rows {
		if scaledMagnitude(row.x, row.y).Cmp(total) > 0 {
			out = append(out, fmt.Sprintf("row %d asks for %g W, more than the %g W the fleets' nameplates total", n+1, scaledFloat(row.x, row.y), totalW))
		}
	}
	return out
}

// logCreated writes the success line: the batch, and per row its requested
// and encoded watts, with the shares' mRIDs.
func (h *AdminGridRequestHandler) logCreated(r *http.Request, out GridRequestCreated) {
	requested := make([]float64, len(out.Rows))
	encoded := make([]float64, len(out.Rows))
	var mrids []string
	for n, row := range out.Rows {
		requested[n], encoded[n] = row.RequestedW, row.EncodedW
		for _, s := range row.Shares {
			if s.Control != nil {
				mrids = append(mrids, logsafe.Str(s.Control.MRID))
			}
		}
	}
	h.Controls.Controls.log(r, slog.LevelInfo, "admin: DERMS grid request created", "der_grid_request_created", []any{
		"batch", logsafe.Str(out.Batch), "rows", len(out.Rows), "controls", len(mrids),
		"requested_w", requested, "encoded_w", encoded, "mrids", mrids})
}
