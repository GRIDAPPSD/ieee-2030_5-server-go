package handler

import (
	"cmp"
	"context"
	"log"
	"math/big"
	"net/http"
	"slices"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/commitment"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/flowreservation"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/handlers/sep2time"
)

// #801: what each fleet is committed to, as the ledger enforces it.
//
//	GET /api/derms/commitments?aggregatorLFDI=<lfdi>

// FleetCommitments reads one fleet's commitments; *commitment.Ledger is
// the production one.
type FleetCommitments interface {
	Commitments(ctx context.Context, fleetKey string, now int64) (commitment.Commitments, error)
}

// FleetDirectory says whether a key names a fleet; commitment.Fleets is
// the production one.
type FleetDirectory interface {
	Known(ctx context.Context, fleetKey string) (bool, error)
}

// AdminCommitmentsHandler serves the commitments read route.
type AdminCommitmentsHandler struct {
	Ledger FleetCommitments
	// Fleets is asked before Ledger: the ledger keeps a lock for every key
	// it is given, so an unknown key must never reach it.
	Fleets FleetDirectory
	// Now is the clock that decides which windows have ended; nil uses the
	// protocol clock.
	Now func() int64
}

// CommittedGrantView is a live grant. Power and energy are magnitudes and
// direction names the flow, from the rule the ledger executes by; a
// quantity the response does not carry is null.
type CommittedGrantView struct {
	MRID              string     `json:"mRID"`
	EdevID            string     `json:"edevId"`
	FrqID             string     `json:"frqId"`
	Window            frInterval `json:"window"`
	Direction         *string    `json:"direction"`
	PowerW            *float64   `json:"powerW"`
	EnergyWh          *float64   `json:"energyWh"`
	EnergyRemainingWh *float64   `json:"energyRemainingWh"`
}

// CommittedControlView is a live plain control. TargetW is opModTargetW as
// stored, discharge positive.
type CommittedControlView struct {
	MRID    string     `json:"mRID"`
	EdevID  string     `json:"edevId"`
	Window  frInterval `json:"window"`
	TargetW *float64   `json:"targetW"`
}

// CommitmentsView is one fleet's commitments whose window ends after Now.
type CommitmentsView struct {
	AggregatorLFDI string                 `json:"aggregatorLFDI"`
	Now            int64                  `json:"now"`
	Grants         []CommittedGrantView   `json:"grants"`
	PlainControls  []CommittedControlView `json:"plainControls"`
}

func (h *AdminCommitmentsHandler) now() int64 {
	if h.Now != nil {
		return h.Now()
	}
	return sep2time.Now().Unix()
}

// HandleList serves GET /api/derms/commitments. A failed read is a 500,
// never an empty list: an empty list says the fleet is free.
func (h *AdminCommitmentsHandler) HandleList() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		agg, ok := fleetParam(w, r)
		if !ok {
			return
		}
		known, err := h.Fleets.Known(r.Context(), agg)
		if err != nil {
			log.Printf("admin commitments: resolving fleet %s: %v", agg, err)
			writeCommitmentsInternal(w)
			return
		}
		if !known {
			writeFRRefusal(w, http.StatusNotFound, "fleet_not_found", "no such fleet", "")
			return
		}
		now := h.now()
		c, err := h.Ledger.Commitments(r.Context(), agg, now)
		if err != nil {
			log.Printf("admin commitments: fleet %s: %v", agg, err)
			writeCommitmentsInternal(w)
			return
		}
		writeFRJSON(w, http.StatusOK, commitmentsView(agg, now, c))
	}
}

func writeCommitmentsInternal(w http.ResponseWriter) {
	writeFRRefusal(w, http.StatusInternalServerError, "internal", "commitments read failed, see server log", "")
}

func commitmentsView(agg string, now int64, c commitment.Commitments) CommitmentsView {
	view := CommitmentsView{
		AggregatorLFDI: agg,
		Now:            now,
		Grants:         make([]CommittedGrantView, 0, len(c.Grants)),
		PlainControls:  make([]CommittedControlView, 0, len(c.Plain)),
	}
	for _, g := range c.Grants {
		view.Grants = append(view.Grants, committedGrantView(g))
	}
	for _, ctl := range c.Plain {
		cv := CommittedControlView{
			MRID:   ctl.MRID,
			EdevID: ctl.EndDeviceID,
			Window: frInterval{Start: ctl.Window.Start, Duration: ctl.Window.Duration},
		}
		if ctl.TargetW != nil {
			v := scaledFloat(int64(ctl.TargetW.Value), ctl.TargetW.Multiplier)
			cv.TargetW = &v
		}
		view.PlainControls = append(view.PlainControls, cv)
	}
	slices.SortFunc(view.Grants, func(a, b CommittedGrantView) int {
		return cmp.Or(cmp.Compare(a.Window.Start, b.Window.Start), cmp.Compare(a.MRID, b.MRID))
	})
	slices.SortFunc(view.PlainControls, func(a, b CommittedControlView) int {
		return cmp.Or(cmp.Compare(a.Window.Start, b.Window.Start), cmp.Compare(a.MRID, b.MRID))
	})
	return view
}

func committedGrantView(g commitment.CommittedGrant) CommittedGrantView {
	gv := CommittedGrantView{
		MRID:      g.MRID,
		EdevID:    g.EndDeviceID,
		FrqID:     flowreservation.RequestIDOf(g.ID),
		Window:    frInterval{Start: g.Window.Start, Duration: g.Window.Duration},
		Direction: directionOf(g.Energy),
	}
	if g.Power != nil {
		p, _ := scaledMagnitude(int64(g.Power.Value), g.Power.Multiplier).Float64()
		gv.PowerW = &p
	}
	if g.Energy != nil {
		available := scaledMagnitude(g.Energy.Value, g.Energy.Multiplier)
		e, _ := available.Float64()
		left, _ := new(big.Rat).Sub(available, committedWh(g.Executions)).Float64()
		gv.EnergyWh, gv.EnergyRemainingWh = &e, &left
	}
	return gv
}

// scaledFloat is value x 10^multiplier with value's sign.
func scaledFloat(value int64, multiplier int8) float64 {
	f, _ := scaledMagnitude(value, multiplier).Float64()
	if value < 0 {
		return -f
	}
	return f
}

var (
	_ FleetCommitments = (*commitment.Ledger)(nil)
	_ FleetDirectory   = commitment.Fleets{}
)
