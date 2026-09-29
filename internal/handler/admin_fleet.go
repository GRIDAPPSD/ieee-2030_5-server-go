package handler

import (
	"context"
	"math"
	"net/http"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

// #715: admin read API for aggregator fleet status, measurements and
// availability. A DERMS operator cannot otherwise see whether a fleet is
// connected, alarmed or delivering: DERStatus, DERAvailability and mirror
// readings are all stored, but nothing on the admin plane reads them, and the
// protocol-side mirror GET strips readings by rule (2030.5-2018 10.11.3(c)).
//
//	GET /api/derms/fleets   one entry per aggregator, with its fleet's
//	                        devices and a roll-up
//
// A fleet is the aggregator's own EndDevice plus every device the management
// pairs store says it manages.

// uomHertz is IEC 61968/V1.2 section 10.4's ReadingType.uom value for
// frequency; pkg/sep2/metering.go's Uom* constants (Watts, Vars, Volts,
// Amps) do not include it. Same value and rationale as
// test/csip/basic_029_meter_reading_test.go's test-local constant of the
// same name.
const uomHertz uint8 = 33

// FleetMeasurement is one quantity's latest reported value.
type FleetMeasurement struct {
	Value        float64 `json:"value"`
	ReadingTime  int64   `json:"readingTime"`
	QualityFlags *uint16 `json:"qualityFlags,omitempty"`
}

// FleetDeviceMeasurements holds the latest P, Q, V and f mirror readings for
// one device. P and Q are export-positive (see considerMeasurement); V and f
// have no flow direction and are reported as their raw magnitude. A nil field
// means the device has never reported that quantity.
type FleetDeviceMeasurements struct {
	P *FleetMeasurement `json:"p,omitempty"`
	Q *FleetMeasurement `json:"q,omitempty"`
	V *FleetMeasurement `json:"v,omitempty"`
	F *FleetMeasurement `json:"f,omitempty"`
}

// FleetDeviceStatus is the subset of DERStatus the DERMS tab needs:
// connection, operational mode, alarm and state of charge.
type FleetDeviceStatus struct {
	Connected       *bool   `json:"connected,omitempty"`
	OperationalMode *uint8  `json:"operationalMode,omitempty"`
	AlarmStatus     *uint32 `json:"alarmStatus,omitempty"`
	StateOfCharge   *uint16 `json:"stateOfCharge,omitempty"`
	ReadingTime     int64   `json:"readingTime,omitempty"`
}

// FleetDeviceAvailability is the subset of DERAvailability the DERMS tab
// needs: available active and reactive capacity.
type FleetDeviceAvailability struct {
	StatWAvail   *float64 `json:"statWAvail,omitempty"`
	StatVarAvail *float64 `json:"statVarAvail,omitempty"`
	ReadingTime  int64    `json:"readingTime,omitempty"`
}

// FleetDevice is one managed device's reported state.
type FleetDevice struct {
	LFDI         string                   `json:"lfdi"`
	Status       *FleetDeviceStatus       `json:"status,omitempty"`
	Availability *FleetDeviceAvailability `json:"availability,omitempty"`
	Measurements FleetDeviceMeasurements  `json:"measurements"`
}

// FleetSum is a fleet-wide total over devices that reported the value, with
// the count that did not. "Sums only reported values, with the unreported
// count beside every sum" (#715 acceptance criteria): a device with no value
// contributes to Unreported, never a synthesized zero to Sum.
type FleetSum struct {
	Sum        float64 `json:"sum"`
	Unreported int     `json:"unreported"`
}

// FleetRollup is one aggregator's fleet-wide roll-up. Connected, Alarmed and
// Stale are device counts; the sums cover only the quantities that are
// physically additive across a fleet (real and reactive power, and their
// available capacity) - voltage and frequency are per-device magnitudes, not
// fleet totals, so they are reported on each FleetDevice only.
type FleetRollup struct {
	DeviceCount  int      `json:"deviceCount"`
	Connected    int      `json:"connected"`
	Alarmed      int      `json:"alarmed"`
	Stale        int      `json:"stale"`
	P            FleetSum `json:"p"`
	Q            FleetSum `json:"q"`
	StatWAvail   FleetSum `json:"statWAvail"`
	StatVarAvail FleetSum `json:"statVarAvail"`
}

// Fleet is one aggregator's fleet: its devices and their roll-up.
type Fleet struct {
	AggregatorLFDI string        `json:"aggregatorLFDI"`
	Devices        []FleetDevice `json:"devices"`
	Rollup         FleetRollup   `json:"rollup"`
}

// AdminFleetHandler is the dependency surface for GET /api/derms/fleets.
// Managers is the concrete store, not the narrower store contract, because
// Managers() (enumerate every aggregator) is not part of it, the same reason
// AdminManagementHandler needs the concrete type for RekeyManager/RekeyManaged.
type AdminFleetHandler struct {
	Managers            *memory.EndDeviceManagementStore
	EndDevices          store.EndDeviceReader
	DERs                store.ScopedReader[sep2.DER]
	DERStatuses         store.ScopedReader[sep2.DERStatus]
	DERAvailabilities   store.ScopedReader[sep2.DERAvailability]
	MirrorUsagePoints   store.ResourceReader[sep2.MirrorUsagePoint]
	MirrorMeterReadings store.ScopedReader[sep2.MirrorMeterReading]
}

// staleAfterSeconds is how old a DERStatus reading may be before a device
// counts as stale rather than connected. 15 minutes: long enough to absorb a
// missed reporting interval at typical DER post rates, short enough that an
// operator sees a gone-quiet fleet within one tab refresh. No product
// decision has set this; it is a placeholder default, not a tuned value.
const staleAfterSeconds = 15 * 60

// connectStatusConnectedBit is ConnectStatusType.value bit 0 (IEEE 2030.5
// Annex B "ConnectStatusType"): the device reports itself connected to the
// grid.
const connectStatusConnectedBit = 1 << 0

// HandleListFleets returns a handler for GET /api/derms/fleets: one entry
// per aggregator (an LFDI that manages at least one device), each with its
// fleet's devices and roll-up.
func HandleListFleets(h *AdminFleetHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		aggregators := h.Managers.Managers(ctx)
		fleets := make([]Fleet, 0, len(aggregators))
		for _, agg := range aggregators {
			fleets = append(fleets, h.buildFleet(ctx, agg))
		}
		writeJSON(w, http.StatusOK, fleets)
	}
}

// buildFleet assembles one aggregator's fleet: its own EndDevice plus every
// device it manages (design doc: a fleet is keyed by the aggregator's LFDI).
func (h *AdminFleetHandler) buildFleet(ctx context.Context, aggregatorLFDI string) Fleet {
	managed, _ := h.Managers.ManagedBy(ctx, aggregatorLFDI)
	memberLFDIs := make([]string, 0, len(managed)+1)
	memberLFDIs = append(memberLFDIs, aggregatorLFDI)
	memberLFDIs = append(memberLFDIs, managed...)

	devices := make([]FleetDevice, 0, len(memberLFDIs))
	now := nowUnix()
	rollup := FleetRollup{DeviceCount: len(memberLFDIs)}
	for _, lfdi := range memberLFDIs {
		dev := h.buildDevice(ctx, lfdi)
		devices = append(devices, dev)
		accumulateRollup(&rollup, dev, now)
	}

	return Fleet{AggregatorLFDI: aggregatorLFDI, Devices: devices, Rollup: rollup}
}

// nowUnix is a var so a test can pin the clock without threading time
// through every call in the roll-up path.
var nowUnix = func() int64 { return time.Now().Unix() }

// buildDevice reads one device's latest DERStatus, DERAvailability and
// mirror measurements. A device with no EndDevice record (named as managed
// but never registered) still gets its mirror measurements: the trap in the
// #715 acceptance criteria is that a reading is attributed by the mirror's
// own deviceLFDI, never by whether the EndDevice store happens to know the
// device.
func (h *AdminFleetHandler) buildDevice(ctx context.Context, lfdi string) FleetDevice {
	fd := FleetDevice{LFDI: lfdi, Measurements: h.deviceMeasurements(ctx, lfdi)}

	if h.EndDevices == nil {
		return fd
	}
	dev, err := h.EndDevices.GetByLFDI(ctx, lfdi)
	if err != nil {
		return fd
	}
	edevID := pathTail(dev.Href)
	if h.DERs == nil {
		return fd
	}
	ders, err := h.DERs.List(ctx, edevID, store.ListOptions{Unbounded: true})
	if err != nil {
		return fd
	}

	var status *FleetDeviceStatus
	var avail *FleetDeviceAvailability
	for _, der := range ders.Items {
		derID := pathTail(der.Href)
		parentKey := edevID + "/" + derID
		if s := h.latestStatus(ctx, parentKey); s != nil && (status == nil || s.ReadingTime > status.ReadingTime) {
			status = s
		}
		if a := h.latestAvailability(ctx, parentKey); a != nil && (avail == nil || a.ReadingTime > avail.ReadingTime) {
			avail = a
		}
	}
	fd.Status = status
	fd.Availability = avail
	return fd
}

func (h *AdminFleetHandler) latestStatus(ctx context.Context, parentKey string) *FleetDeviceStatus {
	if h.DERStatuses == nil {
		return nil
	}
	s, err := h.DERStatuses.Get(ctx, parentKey, singletonKey)
	if err != nil {
		return nil
	}
	out := &FleetDeviceStatus{ReadingTime: s.ReadingTime}
	if s.GenConnectStatus != nil {
		connected := s.GenConnectStatus.Value&connectStatusConnectedBit != 0
		out.Connected = &connected
	}
	if s.OperationalModeStatus != nil {
		v := s.OperationalModeStatus.Value
		out.OperationalMode = &v
	}
	if s.AlarmStatus != nil {
		v := uint32(*s.AlarmStatus)
		out.AlarmStatus = &v
	}
	if s.StateOfChargeStatus != nil {
		v := s.StateOfChargeStatus.Value
		out.StateOfCharge = &v
	}
	return out
}

func (h *AdminFleetHandler) latestAvailability(ctx context.Context, parentKey string) *FleetDeviceAvailability {
	if h.DERAvailabilities == nil {
		return nil
	}
	a, err := h.DERAvailabilities.Get(ctx, parentKey, singletonKey)
	if err != nil {
		return nil
	}
	out := &FleetDeviceAvailability{ReadingTime: a.ReadingTime}
	if a.StatWAvail != nil {
		v := scaledValue(float64(a.StatWAvail.Value), a.StatWAvail.Multiplier)
		out.StatWAvail = &v
	}
	if a.StatVarAvail != nil {
		v := scaledValue(float64(a.StatVarAvail.Value), a.StatVarAvail.Multiplier)
		out.StatVarAvail = &v
	}
	return out
}

// singletonKey mirrors pkg/sep2srv/handlers/singleton.SingletonKey. Not
// imported directly: that package sits under pkg/sep2srv, which this
// admin-plane package does not otherwise depend on, and the value is part of
// the storage convention (every DER singleton sub-resource is stored under
// this one key per parent), not of that package's behavior.
const singletonKey = "default"

// deviceMeasurements finds every MirrorUsagePoint whose stored DeviceLFDI is
// lfdi and returns the latest P, Q, V and f reading across all of them.
// Readings can be stored two ways - inline on the MirrorUsagePoint (the
// POST /mup body) and out-of-band (POST /mup/{id}/mr, a separate collection
// an inline overwrite never touches) - so both are read.
func (h *AdminFleetHandler) deviceMeasurements(ctx context.Context, lfdi string) FleetDeviceMeasurements {
	var out FleetDeviceMeasurements
	if h.MirrorUsagePoints == nil {
		return out
	}
	result, err := h.MirrorUsagePoints.List(ctx, store.ListOptions{Unbounded: true})
	if err != nil {
		return out
	}
	for _, mup := range result.Items {
		if mup.DeviceLFDI != lfdi {
			continue
		}
		for i := range mup.MirrorMeterReading {
			considerMeasurement(&out, mup.MirrorMeterReading[i])
		}
		if h.MirrorMeterReadings == nil {
			continue
		}
		mupID := pathTail(mup.Href)
		mmrs, err := h.MirrorMeterReadings.List(ctx, mupID, store.ListOptions{Unbounded: true})
		if err != nil {
			continue
		}
		for i := range mmrs.Items {
			considerMeasurement(&out, mmrs.Items[i])
		}
	}
	return out
}

// considerMeasurement folds one MirrorMeterReading into out, keeping the
// newest (by LastUpdateTime) reading per quantity. A reading missing its
// type, value or unit is skipped: there is nothing to attribute it to.
//
// P and Q are mapped to export-positive (design doc's Q3 sign convention):
// FlowDirectionReverse ("received from customer") is the fleet exporting and
// is already export-positive; FlowDirectionForward ("delivered to customer")
// is the fleet importing and is negated. A reading with no flowDirection is
// passed through unmapped rather than guessing a sign.
func considerMeasurement(out *FleetDeviceMeasurements, mmr sep2.MirrorMeterReading) {
	if mmr.ReadingType == nil || mmr.ReadingType.Uom == nil || mmr.Reading == nil || mmr.Reading.Value == nil {
		return
	}
	rt := mmr.ReadingType
	var slot **FleetMeasurement
	directional := false
	switch *rt.Uom {
	case sep2.UomWatts:
		slot, directional = &out.P, true
	case sep2.UomVars:
		slot, directional = &out.Q, true
	case sep2.UomVolts:
		slot = &out.V
	case uomHertz:
		slot = &out.F
	default:
		return
	}

	multiplier := int8(0)
	if rt.PowerOfTenMultiplier != nil {
		multiplier = *rt.PowerOfTenMultiplier
	}
	value := scaledValue(float64(*mmr.Reading.Value), multiplier)
	if directional && rt.FlowDirection != nil && *rt.FlowDirection == sep2.FlowDirectionForward {
		value = -value
	}

	m := &FleetMeasurement{Value: value, ReadingTime: mmr.LastUpdateTime}
	if mmr.Reading.QualityFlags != nil {
		v := uint16(*mmr.Reading.QualityFlags)
		m.QualityFlags = &v
	}
	if *slot == nil || m.ReadingTime >= (*slot).ReadingTime {
		*slot = m
	}
}

// scaledValue applies a sep2 multiplier+value pair's power-of-ten scale.
func scaledValue(value float64, multiplier int8) float64 {
	return value * math.Pow10(int(multiplier))
}

// accumulateRollup folds one device into its fleet's roll-up: the three
// status counts (connected, alarmed, stale) and the four additive sums, each
// with the count of devices that did not report it.
func accumulateRollup(rollup *FleetRollup, dev FleetDevice, now int64) {
	switch {
	case dev.Status == nil:
		// Never reported: not counted in any of connected/alarmed/stale.
		// "Never reported" and "reported, but the reading has gone stale"
		// are different facts, and only the roll-up's sums carry an explicit
		// unreported count (per the acceptance criteria); the status counts
		// do not partition the fleet.
	case now-dev.Status.ReadingTime > staleAfterSeconds:
		rollup.Stale++
	default:
		if dev.Status.Connected != nil && *dev.Status.Connected {
			rollup.Connected++
		}
	}
	if dev.Status != nil && dev.Status.AlarmStatus != nil && *dev.Status.AlarmStatus != 0 {
		rollup.Alarmed++
	}

	addToSum(&rollup.P, measurementValue(dev.Measurements.P))
	addToSum(&rollup.Q, measurementValue(dev.Measurements.Q))
	if dev.Availability != nil {
		addToSum(&rollup.StatWAvail, dev.Availability.StatWAvail)
		addToSum(&rollup.StatVarAvail, dev.Availability.StatVarAvail)
	} else {
		rollup.StatWAvail.Unreported++
		rollup.StatVarAvail.Unreported++
	}
}

func measurementValue(m *FleetMeasurement) *float64 {
	if m == nil {
		return nil
	}
	return &m.Value
}

// addToSum adds value to sum.Sum when reported, or counts it as unreported.
// A missing value never contributes a synthesized zero to Sum.
func addToSum(sum *FleetSum, value *float64) {
	if value == nil {
		sum.Unreported++
		return
	}
	sum.Sum += *value
}
