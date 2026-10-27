package handler

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log"
	"maps"
	"math"
	"net/http"
	"slices"
	"strconv"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/dercontrol"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/derhref"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/logsafe"
	sep2lfdi "github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/lfdi"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

// GET /api/derms/control-events: every issued DER control that was in force,
// or waiting to be, during a window, one event per control per group, for the
// Overview's timeline under the power chart. It reads the stores the control
// list routes read and decides nothing itself: status and in-force time come
// from dercontrol.DeriveStatus and ControlState, origin and direction from
// the helpers the list views use.

// Group kinds of a control event.
const (
	controlEventAggregator = "aggregator"
	controlEventDirect     = "direct"
)

// Scope of a control event: sent to one device, or to an FSA's devices.
const (
	controlEventDevice = "device"
	controlEventFSA    = "fsa"
)

var errControlEventsRead = errors.New("admin control events: store read failed")

// ControlEventFSAs is the FSA store surface the route reads. The production
// implementation is *memory.AdminFSAStore.
type ControlEventFSAs interface {
	IDs(ctx context.Context) []string
	Devices(ctx context.Context, fsaID string) []string
	Templates(ctx context.Context, fsaID string) []memory.ProgramTemplate
	FSAControls(ctx context.Context, fsaID, templateID string) []memory.FSAControl
	FSAControlByMRID(mrid string) (fsaID string, c memory.FSAControl, ok bool)
	FleetAggregator(fsaID string) string
}

// AdminControlEventsHandler serves GET /api/derms/control-events. Every field
// is required.
type AdminControlEventsHandler struct {
	Controls   DERControlReader
	Lifecycles DERControlLifecycleReader
	EndDevices EndDeviceGetter
	FSAs       ControlEventFSAs
	// Fleet supplies the management pairs that place a device in a lane; no
	// other field of it is read.
	Fleet *AdminFleetHandler
}

// ControlEvent is one control in a window. DeviceIDs are the EndDevice store
// ids it reached in Group. TargetW is signed watts, discharge positive, or
// null for a control with no opModTargetW. End is the second the control
// stopped being in force: its interval end, or the earlier cancel or
// supersede time.
type ControlEvent struct {
	Group     string   `json:"group"`
	GroupKind string   `json:"groupKind"`
	Scope     string   `json:"scope"`
	FSAID     string   `json:"fsaID,omitempty"`
	DeviceIDs []string `json:"deviceIDs"`
	MRID      string   `json:"mrid"`
	Origin    string   `json:"origin"`
	Direction *string  `json:"direction"`
	TargetW   *float64 `json:"targetW"`
	Modes     []string `json:"modes"`
	Status    string   `json:"status"`
	Start     int64    `json:"start"`
	End       int64    `json:"end"`
}

// ControlEventsResponse is the body of GET /api/derms/control-events. Events
// is always an array.
type ControlEventsResponse struct {
	WindowStart int64          `json:"windowStart"`
	WindowEnd   int64          `json:"windowEnd"`
	Events      []ControlEvent `json:"events"`
}

// HandleControlEvents returns the handler for GET /api/derms/control-events
// with window (900, 3600, 21600 or 86400 seconds, ending now rounded to the
// power route's bucket) and an optional scope, all or one aggregator's LFDI.
func (h *AdminControlEventsHandler) HandleControlEvents() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		q := r.URL.Query()
		window, err := strconv.ParseInt(q.Get("window"), 10, 64)
		if err != nil || !slices.Contains(powerWindows, window) {
			writeError(w, http.StatusBadRequest, "window must be 900, 3600, 21600 or 86400 seconds")
			return
		}
		aggregators := sortedAggregators(ctx, h.Fleet)
		groups, err := energyGroupsOf(ctx, h.Fleet, aggregators)
		if err != nil {
			h.fail(w, fmt.Errorf("%w: %w", errControlEventsRead, err))
			return
		}
		only := ""
		if scope := q.Get("scope"); scope != "" && scope != "all" {
			agg, ok := sep2lfdi.CanonicalTrimSpace(scope)
			if !ok {
				writeError(w, http.StatusBadRequest, "scope must be all or an aggregator LFDI of 40 hex digits")
				return
			}
			if !slices.Contains(aggregators, agg) {
				writeError(w, http.StatusNotFound, "no such aggregator")
				return
			}
			only = agg
		}

		now := nowUnix()
		bucket := powerBucketSeconds(window)
		we := now / bucket * bucket
		resp := ControlEventsResponse{WindowStart: we - window, WindowEnd: we, Events: []ControlEvent{}}
		events, err := h.events(ctx, groups, now)
		if err != nil {
			h.fail(w, err)
			return
		}
		for _, e := range events {
			if (only == "" || e.Group == only) && e.Start < we && e.End > resp.WindowStart {
				resp.Events = append(resp.Events, e)
			}
		}
		lane := func(key string) int {
			return slices.IndexFunc(groups, func(g energyGroup) bool { return g.key == key })
		}
		slices.SortStableFunc(resp.Events, func(a, b ControlEvent) int {
			return cmp.Or(cmp.Compare(a.Start, b.Start), cmp.Compare(lane(a.Group), lane(b.Group)), cmp.Compare(a.MRID, b.MRID), cmp.Compare(a.FSAID, b.FSAID))
		})
		writeJSON(w, http.StatusOK, resp)
	}
}

func (h *AdminControlEventsHandler) fail(w http.ResponseWriter, err error) {
	log.Printf("admin control events: %s", logsafe.Err(err))
	writeError(w, http.StatusInternalServerError, "control events read failed, see server log")
}

// controlEventBuilder resolves device ids to groups once per request.
type controlEventBuilder struct {
	h      *AdminControlEventsHandler
	groups []energyGroup
	now    int64
	lfdis  map[string]string // device id to LFDI, "" for a device with no record
}

func (b *controlEventBuilder) lfdi(ctx context.Context, deviceID string) (string, error) {
	if v, ok := b.lfdis[deviceID]; ok {
		return v, nil
	}
	dev, err := b.h.EndDevices.Get(ctx, deviceID)
	switch {
	case err == nil:
		b.lfdis[deviceID] = dev.LFDI
	case errors.Is(err, store.ErrNotFound):
		b.lfdis[deviceID] = ""
	default:
		return "", fmt.Errorf("%w: %w", errControlEventsRead, err)
	}
	return b.lfdis[deviceID], nil
}

// byGroup splits deviceIDs by group. A device with no EndDevice record has no
// LFDI to group by and is left out.
func (b *controlEventBuilder) byGroup(ctx context.Context, deviceIDs []string) (map[string][]string, error) {
	out := map[string][]string{}
	for _, id := range deviceIDs {
		lfdi, err := b.lfdi(ctx, id)
		if err != nil {
			return nil, err
		}
		if lfdi == "" {
			continue
		}
		c := canonicalLFDI(lfdi)
		for _, g := range b.groups {
			if g.has(c) {
				out[g.key] = append(out[g.key], id)
			}
		}
	}
	return out, nil
}

// events is every control with in-force time, device scope then FSA scope,
// before the window filter.
func (h *AdminControlEventsHandler) events(ctx context.Context, groups []energyGroup, now int64) ([]ControlEvent, error) {
	b := &controlEventBuilder{h: h, groups: groups, now: now, lfdis: map[string]string{}}
	out, err := b.deviceEvents(ctx)
	if err != nil {
		return nil, err
	}
	fsa, err := b.fsaEvents(ctx)
	if err != nil {
		return nil, err
	}
	return append(out, fsa...), nil
}

// deviceEvents is the controls issued to one device. A copy of an FSA control
// is left to fsaEvents, which reports the control once for the fleet.
func (b *controlEventBuilder) deviceEvents(ctx context.Context) ([]ControlEvent, error) {
	h := b.h
	parents, err := h.Controls.Parents(ctx)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", errControlEventsRead, err)
	}
	var out []ControlEvent
	for _, key := range parents {
		scope, ok := dercontrol.ScopeFromKey(key)
		if !ok {
			continue
		}
		page, err := h.Controls.List(ctx, key, store.ListOptions{Unbounded: true})
		if err != nil {
			return nil, fmt.Errorf("%w: %w", errControlEventsRead, err)
		}
		var groups map[string][]string
		for _, ctrl := range page.Items {
			id, ok := derhref.ControlID(ctrl.Href)
			if !ok || ctrl.Interval == nil {
				continue
			}
			if _, _, isCopy := h.FSAs.FSAControlByMRID(ctrl.MRID); isCopy {
				continue
			}
			lc, err := h.Lifecycles.Get(ctx, key, id)
			if err != nil {
				if errors.Is(err, store.ErrNotFound) {
					continue // not issued through the admin API
				}
				return nil, fmt.Errorf("%w: %w", errControlEventsRead, err)
			}
			if groups == nil {
				if groups, err = b.byGroup(ctx, []string{scope.EndDeviceID}); err != nil {
					return nil, err
				}
			}
			out = append(out, b.eventsOf(ctrl, lc, dercontrol.OriginOf(lc.Origin), controlEventDevice, "", groups)...)
		}
	}
	return out, nil
}

// fsaEvents is the controls issued on FSA program templates, from the FSA's
// record, once per group its devices fall in. A fleet FSA is one group, its
// aggregator.
func (b *controlEventBuilder) fsaEvents(ctx context.Context) ([]ControlEvent, error) {
	h := b.h
	var out []ControlEvent
	for _, fsaID := range h.FSAs.IDs(ctx) {
		devices := h.FSAs.Devices(ctx, fsaID)
		if len(devices) == 0 {
			continue
		}
		groups, err := b.fsaGroups(ctx, fsaID, devices)
		if err != nil {
			return nil, err
		}
		for _, tmpl := range h.FSAs.Templates(ctx, fsaID) {
			for _, rec := range h.FSAs.FSAControls(ctx, fsaID, tmpl.ID) {
				lc, err := b.copyLifecycle(ctx, fsaID, rec, devices)
				if err != nil {
					return nil, err
				}
				out = append(out, b.eventsOf(controlOf(rec), lc, dercontrol.OriginOf(rec.Origin), controlEventFSA, fsaID, groups)...)
			}
		}
	}
	return out, nil
}

func (b *controlEventBuilder) fsaGroups(ctx context.Context, fsaID string, devices []string) (map[string][]string, error) {
	if agg := b.h.FSAs.FleetAggregator(fsaID); agg != "" {
		return map[string][]string{canonicalLFDI(agg): devices}, nil
	}
	return b.byGroup(ctx, devices)
}

// copyLifecycle is the lifecycle of one device's copy of rec, which carries
// any supersede, with the record's own cancel folded in. A record no device
// holds a copy of reads by its own cancel alone.
func (b *controlEventBuilder) copyLifecycle(ctx context.Context, fsaID string, rec memory.FSAControl, devices []string) (dercontrol.LifecycleRecord, error) {
	var lc dercontrol.LifecycleRecord
	for _, dev := range devices {
		scope := dercontrol.Scope{EndDeviceID: dev, FSAID: fsaID, DERProgramID: rec.TemplateID}
		got, err := b.h.Lifecycles.Get(ctx, scope.Key(), rec.ID)
		if err == nil {
			lc = got
			break
		}
		if !errors.Is(err, store.ErrNotFound) {
			return lc, fmt.Errorf("%w: %w", errControlEventsRead, err)
		}
	}
	if lc.CancelledAt == nil {
		lc.CancelledAt = rec.CancelledAt
	}
	return lc, nil
}

// eventsOf is one event per group in groups, or none when the control was
// never in force for a second. groups holds the devices reached by group key.
func (b *controlEventBuilder) eventsOf(ctrl sep2.DERControl, lc dercontrol.LifecycleRecord, origin, scope, fsaID string, groups map[string][]string) []ControlEvent {
	start, end := ctrl.Interval.Start, inForceEnd(*ctrl.Interval, lc)
	if end <= start {
		return nil
	}
	st := dercontrol.DeriveStatus(b.now, ctrl.CreationTime, start, lc)
	status := eventStatusName(st.CurrentStatus)
	if st.CurrentStatus == sep2.EventStatusActive && dercontrol.ControlState(ctrl, lc, true, b.now) == dercontrol.Retired {
		status = "completed"
	}
	target := dercontrol.TargetOf(ctrl.DERControlBase)
	ev := ControlEvent{
		Scope:     scope,
		FSAID:     fsaID,
		MRID:      ctrl.MRID,
		Origin:    origin,
		Direction: targetDirectionOf(target),
		Modes:     slices.Sorted(maps.Keys(dercontrol.FunctionValues(ctrl.DERControlBase))),
		Status:    status,
		Start:     start,
		End:       end,
	}
	if target != nil {
		w := float64(target.Value) * math.Pow10(int(target.Multiplier))
		ev.TargetW = &w
	}
	var out []ControlEvent
	for _, g := range slices.Sorted(maps.Keys(groups)) {
		e := ev
		e.Group, e.DeviceIDs = g, slices.Sorted(slices.Values(groups[g]))
		e.GroupKind = controlEventAggregator
		if g == powerDirectKey {
			e.GroupKind = controlEventDirect
		}
		out = append(out, e)
	}
	return out
}
