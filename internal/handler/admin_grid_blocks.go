package handler

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strconv"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/commitment"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/dercontrol"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/logsafe"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/handlers/sep2time"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

// #1157: the block view of the grid requests and their cancel.
//
//	GET  /api/derms/grid-requests                         batches, blocks and shares
//	POST /api/derms/grid-requests/{batch}/cancel          every block not yet ended
//	POST /api/derms/grid-requests/{batch}/rows/{row}/cancel   one block
//
// The batch keeps only the basis each share was split on (memory.GridShare on
// the share's FSA control record). Status, delivery and every flag are read
// from the control store and the current fleets on each request, so this view
// and the Controls tab cannot disagree.

// Flags and statuses of a block, beside the control statuses.
const (
	gridBlockPartlyCancelled = "partly cancelled"
	gridBlockMixed           = "mixed"
	gridShareError           = "error"
	gridDeliveryRoute        = "admin GET /api/derms/grid-requests"
)

const gridCancelOp = "grid_cancel"

var refuseGridNotFound = derControlRefusal{http.StatusNotFound, "grid_request_not_found", "no such grid request or block"}

// GridCurrentShare is what the current fleets would give one aggregator of a
// block whose basis changed. TargetW is null for a share of zero units.
type GridCurrentShare struct {
	AggregatorLFDI string    `json:"aggregatorLFDI"`
	WeightW        float64   `json:"weightW"`
	Units          int64     `json:"units"`
	TargetW        *frScaled `json:"targetW"`
}

// GridBlockShareView is one stored share of a block. AggregatorGone is true
// when its aggregator is no longer available; its control stays live, since
// nothing cancels on removal. DeliveredW sums the copies' average watts and is
// null when no copy has a reading, never a filled zero; CopiesWithoutReading
// counts the copies that add nothing.
type GridBlockShareView struct {
	AggregatorLFDI   string   `json:"aggregatorLFDI"`
	FSA              string   `json:"fsa"`
	Program          string   `json:"program"`
	MRID             string   `json:"mRID"`
	Devices          int      `json:"devices"`
	WeightW          float64  `json:"weightW"`
	DefaultedDevices []string `json:"defaultedDevices"`
	OverCapDevices   []string `json:"overCapDevices"`
	TestDefaultCount int      `json:"testDefaultCount"`
	// TargetW is null for a share with an Error: no value is invented for it.
	TargetW *frScaled `json:"targetW"`
	// Status is the share's control status from its lifecycle, also for a share
	// with an Error, so a live control on a corrupt record still reads live.
	Status               string   `json:"status"`
	AggregatorGone       bool     `json:"aggregatorGone"`
	DeliveredW           *float64 `json:"deliveredW"`
	CoveredSeconds       int64    `json:"coveredSeconds"`
	Copies               int      `json:"copies"`
	CopiesWithoutReading int      `json:"copiesWithoutReading"`
	ReadingsExpired      bool     `json:"readingsExpired"`
	// Error is set, with a null TargetW and no delivery, for a share
	// whose stored record cannot be read as a share: an unknown direction or no
	// target power. The rest of the view is unaffected.
	Error string `json:"error,omitempty"`
}

// GridBlockView is one row of a batch with its stored shares. RequestedW is
// signed like opModTargetW and DifferenceW is EncodedW minus RequestedW; the three are null when no share can be read. Status
// is the shares' common status, "partly cancelled" when some share is
// cancelled and some is not, or "mixed". BasisChanged is true when an
// aggregator is gone or the current weights differ from those the block was
// split on, and CurrentSplit then shows the split the current fleets give.
type GridBlockView struct {
	Row       int    `json:"row"`
	Direction string `json:"direction"`
	// RequestedW is null for a block with no share of a known direction.
	RequestedW      *float64             `json:"requestedW"`
	EncodedW        *float64             `json:"encodedW"`
	DifferenceW     *float64             `json:"differenceW"`
	Start           int64                `json:"start"`
	End             int64                `json:"end"`
	DurationSeconds uint32               `json:"durationSeconds"`
	Status          string               `json:"status"`
	BasisChanged    bool                 `json:"basisChanged"`
	CurrentSplit    []GridCurrentShare   `json:"currentSplit"`
	DeliveredW      *float64             `json:"deliveredW"`
	Shares          []GridBlockShareView `json:"shares"`
}

// GridBatchView is one batch and its blocks in row order.
type GridBatchView struct {
	Batch string          `json:"batch"`
	Rows  []GridBlockView `json:"rows"`
}

// GridRequestList is the body of GET /api/derms/grid-requests. Batches is
// always an array, earliest block first.
type GridRequestList struct {
	Batches []GridBatchView `json:"batches"`
}

// HandleList returns the handler for GET /api/derms/grid-requests.
func (h *AdminGridRequestHandler) HandleList() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !h.wired(w, r) {
			return
		}
		if h.Controls.Controls.EndDevices == nil {
			writeError(w, http.StatusInternalServerError, templateInternalError)
			return
		}
		ctx, ch, fh := r.Context(), h.Controls.Controls, h.Controls.FSAs
		// The fleets are read under the lock readFleets asks for; the controls
		// are read after it, so a long view never holds up a write.
		fh.programMu.Lock()
		fleets, _, _, err := h.readFleets(ctx)
		fh.programMu.Unlock()
		if err != nil {
			ch.internal(w, r, "grid fleets")
			return
		}
		batches, err := h.batchViews(ctx, fleets, sep2time.Now().Unix())
		if err != nil {
			ch.internal(w, r, "grid block view")
			return
		}
		writeJSON(w, http.StatusOK, GridRequestList{Batches: batches})
	}
}

// batchViews is every batch on the fleet FSAs, earliest block first, each
// with its blocks in row order.
func (h *AdminGridRequestHandler) batchViews(ctx context.Context, fleets []gridFleet, now int64) ([]GridBatchView, error) {
	rows := map[string]map[int][]gridControl{}
	first := map[string]int64{}
	for _, gc := range h.gridControls(ctx) {
		g := gc.rec.GridShare
		if rows[g.Batch] == nil {
			rows[g.Batch] = map[int][]gridControl{}
			first[g.Batch] = gc.rec.Start
		}
		rows[g.Batch][g.Row] = append(rows[g.Batch][g.Row], gc)
		first[g.Batch] = min(first[g.Batch], gc.rec.Start)
	}
	ids := make([]string, 0, len(rows))
	for id := range rows {
		ids = append(ids, id)
	}
	slices.SortFunc(ids, func(a, b string) int { return cmp.Or(cmp.Compare(first[a], first[b]), cmp.Compare(a, b)) })

	reader := &gridReader{h: h, now: now, mirrors: map[string]*lazyMirrors{}, lfdis: map[string]string{}}
	out := make([]GridBatchView, 0, len(ids))
	for _, id := range ids {
		numbers := make([]int, 0, len(rows[id]))
		for n := range rows[id] {
			numbers = append(numbers, n)
		}
		slices.Sort(numbers)
		view := GridBatchView{Batch: id, Rows: make([]GridBlockView, 0, len(numbers))}
		for _, n := range numbers {
			block, err := reader.block(ctx, rows[id][n], fleets)
			if err != nil {
				return nil, err
			}
			view.Rows = append(view.Rows, block)
		}
		out = append(out, view)
	}
	return out, nil
}

// gridReader reads shares at one instant. It keeps each device's EndDevice
// LFDI and mirror readings once, so several shares and copies of one device
// read them once.
type gridReader struct {
	h       *AdminGridRequestHandler
	now     int64
	mirrors map[string]*lazyMirrors // by device LFDI
	lfdis   map[string]string       // device id to LFDI, "" for a device with no record
}

// block is the view of the shares of one row, which share a window.
func (g *gridReader) block(ctx context.Context, ctls []gridControl, fleets []gridFleet) (GridBlockView, error) {
	slices.SortStableFunc(ctls, func(a, b gridControl) int {
		return cmp.Compare(a.rec.GridShare.AggregatorLFDI, b.rec.GridShare.AggregatorLFDI)
	})
	// The row's request is read from its first share with a known direction;
	// a share with none is shown with its error and left out of the totals.
	basis, sign := ctls[0].rec.GridShare, 0
	for _, gc := range ctls {
		if sg := directionSign(gc.rec.GridShare.Direction); sg != 0 {
			basis, sign = gc.rec.GridShare, sg
			break
		}
	}
	win := commitment.Window{Start: ctls[0].rec.Start, Duration: ctls[0].rec.Duration}
	out := GridBlockView{
		Row:             basis.Row,
		Direction:       "",
		Start:           win.Start,
		End:             win.End(),
		DurationSeconds: win.Duration,
		CurrentSplit:    []GridCurrentShare{},
		Shares:          make([]GridBlockShareView, 0, len(ctls)),
	}
	if sign != 0 {
		out.Direction = basis.Direction
		req := scaledFloat(int64(sign)*basis.RequestValue, basis.RequestMultiplier)
		out.RequestedW = &req
	}
	statuses := make([]string, 0, len(ctls))
	stored := make([]*memory.GridShare, 0, len(ctls))
	var delivered []float64
	encoded, readable := 0.0, false
	for _, gc := range ctls {
		sv, err := g.share(ctx, gc, fleets)
		if err != nil {
			return GridBlockView{}, err
		}
		out.Shares = append(out.Shares, sv)
		// Status is the control's own, so a share with an error still counts.
		statuses = append(statuses, sv.Status)
		if sv.Error != "" {
			continue
		}
		encoded += scaledFloat(sv.TargetW.Value, sv.TargetW.Multiplier)
		readable = true
		stored = append(stored, gc.rec.GridShare)
		if sv.DeliveredW != nil {
			delivered = append(delivered, *sv.DeliveredW)
		}
	}
	// With no readable share there is no encoded figure to show, not a zero.
	if readable {
		out.EncodedW = &encoded
		if out.RequestedW != nil {
			d := encoded - *out.RequestedW
			out.DifferenceW = &d
		}
	}
	out.Status = blockStatus(statuses)
	if len(delivered) > 0 {
		sum := 0.0
		for _, d := range delivered {
			sum += d
		}
		out.DeliveredW = &sum
	}
	if sign != 0 && basisChanged(stored, fleets) {
		out.BasisChanged = true
		out.CurrentSplit = currentSplit(basis, sign, fleets)
	}
	return out, nil
}

// share is the view of one stored share: its status from its copies'
// lifecycles, its encoded value from the control, and its delivery from its
// copies' mirror readings.
func (g *gridReader) share(ctx context.Context, gc gridControl, fleets []gridFleet) (GridBlockShareView, error) {
	ch, admin := g.h.Controls.Controls, g.h.Controls.FSAs.AdminFSAs
	rec, basis := gc.rec, gc.rec.GridShare
	devices := admin.Devices(ctx, gc.fsaID)
	lc, err := fsaCopyLifecycle(ctx, ch.Lifecycles, gc.fsaID, rec, devices)
	if err != nil {
		return GridBlockShareView{}, err
	}
	sv := GridBlockShareView{
		AggregatorLFDI:   basis.AggregatorLFDI,
		FSA:              gc.fsaID,
		Program:          rec.TemplateID,
		MRID:             rec.MRID,
		Devices:          basis.Devices,
		WeightW:          basis.WeightW,
		DefaultedDevices: append([]string{}, basis.DefaultedDevices...),
		OverCapDevices:   append([]string{}, basis.OverCapDevices...),
		TestDefaultCount: len(basis.DefaultedDevices) + len(basis.OverCapDevices),
		Status:           controlStatusName(controlOf(rec), lc, g.now),
		AggregatorGone:   !slices.ContainsFunc(fleets, func(f gridFleet) bool { return f.lfdi == basis.AggregatorLFDI }),
		Copies:           len(devices),
	}
	target := dercontrol.TargetOf(&rec.Base)
	switch {
	case directionSign(basis.Direction) == 0:
		sv.Error = fmt.Sprintf("share has direction %q", basis.Direction)
		return sv, nil
	case target == nil:
		sv.Error = "share carries no target power"
		return sv, nil
	}
	sv.TargetW = &frScaled{Value: int64(target.Value), Multiplier: target.Multiplier}
	sum, readings := 0.0, 0
	for _, dev := range devices {
		d, err := g.copyDelivery(ctx, gc, dev)
		if err != nil {
			return GridBlockShareView{}, err
		}
		switch {
		case d == nil || d.AverageW == nil:
			sv.CopiesWithoutReading++
			sv.ReadingsExpired = sv.ReadingsExpired || (d != nil && d.ReadingsExpired)
		default:
			sum += *d.AverageW
			sv.CoveredSeconds += d.CoveredSeconds
			readings++
		}
	}
	if readings > 0 {
		sv.DeliveredW = &sum
	}
	return sv, nil
}

// copyDelivery is the delivery of one device's copy of a share, or nil when
// the device holds no copy or has no EndDevice record to find readings by.
func (g *gridReader) copyDelivery(ctx context.Context, gc gridControl, dev string) (*DERControlDelivery, error) {
	ch := g.h.Controls.Controls
	scope := dercontrol.Scope{EndDeviceID: dev, FSAID: gc.fsaID, DERProgramID: gc.rec.TemplateID}
	lc, err := ch.Lifecycles.Get(ctx, scope.Key(), gc.rec.ID)
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	lfdi, err := g.lfdi(ctx, dev)
	if err != nil || lfdi == "" {
		return nil, err
	}
	lm := g.mirrors[lfdi]
	if lm == nil {
		lm = &lazyMirrors{}
		g.mirrors[lfdi] = lm
	}
	ctrl := controlOf(gc.rec)
	return ch.deliveryFor(ctx, gridDeliveryRoute, lfdi, *ctrl.Interval, lc, g.now, lm)
}

func (g *gridReader) lfdi(ctx context.Context, dev string) (string, error) {
	if v, ok := g.lfdis[dev]; ok {
		return v, nil
	}
	e, err := g.h.Controls.Controls.EndDevices.Get(ctx, dev)
	switch {
	case err == nil:
		g.lfdis[dev] = e.LFDI
	case errors.Is(err, store.ErrNotFound):
		g.lfdis[dev] = ""
	default:
		return "", err
	}
	return g.lfdis[dev], nil
}

// blockStatus is the status of a block from its shares': their common status,
// "partly cancelled" when some are cancelled and some are not, else "mixed".
func blockStatus(statuses []string) string {
	if len(statuses) == 0 {
		return gridShareError
	}
	first := statuses[0]
	if !slices.ContainsFunc(statuses, func(s string) bool { return s != first }) {
		return first
	}
	if slices.Contains(statuses, eventStatusName(sep2.EventStatusCancelled)) {
		return gridBlockPartlyCancelled
	}
	return gridBlockMixed
}

// basisChanged reports whether the shares of one block, split over the fleets
// available then, no longer match the fleets available now: an aggregator is
// gone, one's device count or weight differs, or the total differs, which
// catches an aggregator that appeared or an unissued share's weight changing.
func basisChanged(stored []*memory.GridShare, fleets []gridFleet) bool {
	total := totalWeightW(fleets)
	for _, g := range stored {
		i := slices.IndexFunc(fleets, func(f gridFleet) bool { return f.lfdi == g.AggregatorLFDI })
		if i < 0 {
			return true
		}
		weight, _ := fleets[i].weight.Float64()
		if weight != g.WeightW || len(fleets[i].devices) != g.Devices || total != g.TotalWeightW {
			return true
		}
	}
	return false
}

// currentSplit is the split of the block's request over the current fleets,
// empty when there are none or the split cannot be encoded.
func currentSplit(basis *memory.GridShare, sign int, fleets []gridFleet) []GridCurrentShare {
	split, err := dercontrol.SplitTarget(basis.RequestValue, basis.RequestMultiplier, sign, fleetWeights(fleets))
	if err != nil {
		return []GridCurrentShare{}
	}
	out := make([]GridCurrentShare, len(split))
	for n, s := range split {
		weight, _ := fleets[n].weight.Float64()
		out[n] = GridCurrentShare{AggregatorLFDI: fleets[n].lfdi, WeightW: weight, Units: s.Units}
		if s.TargetW != nil {
			out[n].TargetW = &frScaled{Value: int64(s.TargetW.Value), Multiplier: s.TargetW.Multiplier}
		}
	}
	return out
}

// GridCancelledShare is one share a cancel ended, with the copies it changed.
type GridCancelledShare struct {
	Row            int    `json:"row"`
	AggregatorLFDI string `json:"aggregatorLFDI"`
	MRID           string `json:"mRID"`
	Copies         int    `json:"copies"`
}

// GridCancelled is the body of a cancel. Row is set for a cancel of one block.
// Cancelled lists the shares this call cancelled.
type GridCancelled struct {
	Batch     string               `json:"batch"`
	Row       *int                 `json:"row,omitempty"`
	Cancelled []GridCancelledShare `json:"cancelled"`
	Failed    []GridFailedShare    `json:"failed"`
	Error     string               `json:"error,omitempty"`
	Retry     string               `json:"retry,omitempty"`
}

// GridFailedShare is a share a cancel could not finish, with a fixed reason.
type GridFailedShare struct {
	Row            int    `json:"row"`
	AggregatorLFDI string `json:"aggregatorLFDI"`
	MRID           string `json:"mRID"`
	Reason         string `json:"reason"`
}

// HandleCancelBatch returns the handler for POST
// /api/derms/grid-requests/{batch}/cancel: every block not yet ended.
func (h *AdminGridRequestHandler) HandleCancelBatch() http.HandlerFunc {
	return h.cancel(false)
}

// HandleCancelRow returns the handler for POST
// /api/derms/grid-requests/{batch}/rows/{row}/cancel: one block.
func (h *AdminGridRequestHandler) HandleCancelRow() http.HandlerFunc {
	return h.cancel(true)
}

// cancel cancels the shares of a batch, or of one row of it, each through the
// FSA control cancel core, so every copy and its record end exactly as a
// cancel from the Controls tab does. A share already cancelled or ended is
// left. A failure stops the cancel: the shares before it stay cancelled and a
// retry finishes the rest.
func (h *AdminGridRequestHandler) cancel(oneRow bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var logged derControlLog
		if !h.wired(w, r) {
			return
		}
		ch, fh := h.Controls.Controls, h.Controls.FSAs
		batch, row := r.PathValue("batch"), 0
		if oneRow {
			n, err := strconv.Atoi(r.PathValue("row"))
			if err != nil || n < 1 {
				ch.refuse(w, r, gridCancelOp, refuseGridNotFound, &logged)
				return
			}
			row = n
		}
		reason, ok := h.Controls.cancelReason(w, r, gridCancelOp, &logged)
		if !ok {
			return
		}

		// Held so no device is assigned, and handed a live replica, while
		// the copies are being cancelled.
		fh.programMu.Lock()
		defer fh.programMu.Unlock()
		ctx := r.Context()
		var shares []gridControl
		for _, gc := range h.gridControls(ctx) {
			if g := gc.rec.GridShare; g.Batch == batch && (!oneRow || g.Row == row) {
				shares = append(shares, gc)
			}
		}
		if len(shares) == 0 {
			ch.refuse(w, r, gridCancelOp, refuseGridNotFound, &logged)
			return
		}
		slices.SortStableFunc(shares, func(a, b gridControl) int {
			return cmp.Or(cmp.Compare(a.rec.GridShare.Row, b.rec.GridShare.Row), cmp.Compare(a.rec.GridShare.AggregatorLFDI, b.rec.GridShare.AggregatorLFDI))
		})
		now := sep2time.Now().Unix()
		if ref, none := noneLive(shares, now); none {
			ch.refuse(w, r, gridCancelOp, ref, &logged)
			return
		}

		out := GridCancelled{Batch: batch, Cancelled: []GridCancelledShare{}, Failed: []GridFailedShare{}}
		if oneRow {
			out.Row = &row
		}
		var mrids, failedMRIDs []string
		var causes []error
		for _, gc := range shares {
			if _, refused := cancelRefusal(gc.rec, now); refused {
				continue
			}
			g := gc.rec.GridShare
			res, err := h.Controls.cancelControl(ctx, gc.fsaID, gc.rec, reason)
			if err != nil {
				// Every share is tried: one failing copy must not leave the
				// shares after it live.
				out.Failed = append(out.Failed, GridFailedShare{Row: g.Row, AggregatorLFDI: g.AggregatorLFDI, MRID: gc.rec.MRID, Reason: cancelFailReason(err)})
				failedMRIDs = append(failedMRIDs, logsafe.Str(gc.rec.MRID))
				causes = append(causes, err)
				continue
			}
			out.Cancelled = append(out.Cancelled, GridCancelledShare{Row: g.Row, AggregatorLFDI: g.AggregatorLFDI, MRID: gc.rec.MRID, Copies: res.copies})
			mrids = append(mrids, logsafe.Str(gc.rec.MRID))
		}
		if len(out.Failed) > 0 {
			out.Retry = "/api/derms/grid-requests/" + batch + "/cancel"
			out.Error = fmt.Sprintf("%d of %d shares could not be cancelled; each listed share is still live or only partly cancelled. "+
				"Repeat POST %s: it skips the shares already cancelled and finishes the rest", len(out.Failed), len(out.Failed)+len(out.Cancelled), out.Retry)
			ch.log(r, slog.LevelError, "admin: DERMS grid request cancel left shares live", "der_grid_request_cancel_failed", []any{
				"batch", logsafe.Str(batch), "row", row, "cancelled_mrids", mrids, "failed_mrids", failedMRIDs, "error", logsafe.Err(errors.Join(causes...))})
			writeJSON(w, http.StatusInternalServerError, out)
			return
		}
		ch.log(r, slog.LevelInfo, "admin: DERMS grid request cancelled", "der_grid_request_cancelled", []any{
			"batch", logsafe.Str(batch), "row", row, "shares", len(mrids), "mrids", mrids})
		writeJSON(w, http.StatusOK, out)
	}
}

// noneLive returns the refusal for shares none of which can be cancelled:
// already cancelled when all are, else ended.
func noneLive(shares []gridControl, now int64) (derControlRefusal, bool) {
	allCancelled := true
	for _, gc := range shares {
		ref, refused := cancelRefusal(gc.rec, now)
		if !refused {
			return derControlRefusal{}, false
		}
		allCancelled = allCancelled && ref == refuseAlreadyCancelled
	}
	if allCancelled {
		return refuseAlreadyCancelled, true
	}
	return refuseEnded, true
}
