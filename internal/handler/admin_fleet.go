package handler

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math"
	"net/http"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
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

// flowDirectionNone is FlowDirectionType 0, "Not applicable".
const flowDirectionNone uint8 = 0

// flowDirectionNet is FlowDirectionType Net (2023: abs(Forward) - abs(Reverse),
// signed). The vendored core defines no constant for it, and 2018 reserves 4.
const flowDirectionNet uint8 = 4

// FleetMeasurement is one quantity's latest reported value. ReadingTime is
// the mirror reading's LastUpdateTime, which the server itself stamps at
// POST time (mirror.go's stampMirrorMeterReading, both the inline and the
// out-of-band path) rather than trusting the client's own clock. That is why
// staleness for P and Q is judged from this field directly (see
// accumulateRollup) rather than from the device's self-reported DERStatus.
type FleetMeasurement struct {
	Value        float64 `json:"value"`
	ReadingTime  int64   `json:"readingTime"`
	QualityFlags *uint16 `json:"qualityFlags,omitempty"`
	// DirectionUnknown is true for a P or Q reading whose flowDirection was absent
	// or not Forward or Reverse, so Value is the raw wire magnitude and not
	// export-positive (#733).
	DirectionUnknown bool `json:"directionUnknown,omitempty"`
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
// connection, operational mode, alarm and state of charge. ReadingTime is
// DERStatus.readingTime AS THE CLIENT REPORTED IT: DERStatus is stored
// through the generic singleton PUT (pkg/sep2srv/handlers/singleton), which
// stamps no server-side receipt time of its own, so this is a client-sent
// clock and nothing here corrects for a device that misreports it, reports
// it far in the future, or never sends a DERStatus at all (#715 fix round 2,
// item 4). It is still the only signal available for connection, alarm and
// state of charge, which have no other source, so it is used as-is for
// those and for the DERAvailability sums below, which have the identical
// limitation.
type FleetDeviceStatus struct {
	Connected       *bool   `json:"connected,omitempty"`
	OperationalMode *uint8  `json:"operationalMode,omitempty"`
	AlarmStatus     *uint32 `json:"alarmStatus,omitempty"`
	StateOfCharge   *uint16 `json:"stateOfCharge,omitempty"`
	ReadingTime     int64   `json:"readingTime,omitempty"`
}

// FleetDeviceAvailability is the subset of DERAvailability the DERMS tab
// needs: available active and reactive capacity. ReadingTime is
// DERAvailability.readingTime as the client reported it: like
// FleetDeviceStatus.ReadingTime, DERAvailability is stored through the same
// generic singleton PUT with no server-stamped receipt time, so there is no
// better signal for it, and the roll-up falls back to the device's
// DERStatus-derived staleness for these two sums (#715 fix round 2, item 4).
type FleetDeviceAvailability struct {
	StatWAvail   *float64 `json:"statWAvail,omitempty"`
	StatVarAvail *float64 `json:"statVarAvail,omitempty"`
	ReadingTime  int64    `json:"readingTime,omitempty"`
}

// FleetDevice is one managed device's reported state.
type FleetDevice struct {
	LFDI string `json:"lfdi"`
	// EdevID and Href come from the stored EndDevice record. A managed LFDI
	// with no EndDevice record carries neither; they are never synthesized.
	EdevID       string                   `json:"edevId,omitempty"`
	Href         string                   `json:"href,omitempty"`
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
	// Stale counts a device whose latest known status has gone stale
	// (staleAfterSeconds). Its value, if any, is excluded from Sum and from
	// Unreported: it is neither summed as current nor treated as never
	// reported, since both would misstate what the fleet is actually
	// delivering right now.
	Stale int `json:"stale"`
	// DirectionUnknown is true when a reading that contributed to Sum had
	// no flowDirection. Sum is still the total of every contributing
	// reading, but its sign then cannot be read as export or import (#733).
	// Always false for the capacity sums, which carry no direction.
	DirectionUnknown bool `json:"directionUnknown"`
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

// fleetManagerReader is the read surface AdminFleetHandler needs from the
// management-pair store: ManagedBy (part of store.EndDeviceManagementReader)
// plus Managers, which enumerates every aggregator and is not part of any
// store interface (see memory.EndDeviceManagementStore.Managers's doc
// comment). Narrowed here, at the consumer, rather than holding the full
// concrete store: a test double can then exercise a ManagedBy failure, which
// the concrete store's own implementation never produces.
type fleetManagerReader interface {
	ManagedBy(ctx context.Context, managerLFDI string) ([]string, error)
	Managers(ctx context.Context) []string
}

// SEP2Edition selects which IEEE 2030.5 edition's flowDirection semantics
// govern the export-positive sign mapping (#715 fix round 3, item 2,
// operator decision on #715). The zero value is Edition2018, so an
// AdminFleetHandler built without setting Edition behaves exactly as it did
// before this field existed.
type SEP2Edition string

const (
	// Edition2018: ReadingType.flowDirection Forward means import (mapped
	// negative) and Reverse means export (mapped positive). This is the
	// edition CSIP conformance is written against, and what the zero value
	// of Edition (empty string) also resolves to below, so an
	// AdminFleetHandler with Edition unset behaves exactly as it did
	// before this field existed.
	Edition2018 SEP2Edition = "2018"
	// Edition2023: the Forward/Reverse export mapping flips ONLY when the
	// posting MirrorUsagePoint's roleFlags has isDER set; a non-DER mirror
	// keeps the Edition2018 mapping even under this edition.
	Edition2023 SEP2Edition = "2023"
)

// roleFlagIsDER is RoleFlagsType bit 3 (sep.xsd RoleFlagsType; see
// mirror.go's RoleFlagsValue doc comment in core: bit 0 isMirror, bit 1
// isPremisesAggregationPoint, bit 2 isPEV, bit 3 isDER, ...): the posting
// MirrorUsagePoint represents a DER. #715 fix round 3 item 2 uses it to
// decide whether Edition2023's flipped Forward/Reverse mapping applies.
const roleFlagIsDER = 1 << 3

// AdminFleetHandler is the dependency surface for GET /api/derms/fleets.
type AdminFleetHandler struct {
	Managers            fleetManagerReader
	EndDevices          store.EndDeviceReader
	DERs                store.ScopedReader[sep2.DER]
	DERStatuses         store.ScopedReader[sep2.DERStatus]
	DERAvailabilities   store.ScopedReader[sep2.DERAvailability]
	MirrorUsagePoints   store.ResourceReader[sep2.MirrorUsagePoint]
	MirrorMeterReadings store.ScopedReader[sep2.MirrorMeterReading]
	// Edition is the declared IEEE 2030.5 edition (config SEP2_EDITION,
	// internal/config.Config.EffectiveSEP2Edition); the zero value,
	// Edition2018, is what a handler built with none of this field set
	// already did.
	Edition SEP2Edition
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
// fleet's devices and roll-up. A backend failure building any one fleet
// fails the whole request (500) rather than reading as a smaller, or empty,
// fleet: a partial listing is not a fact this route is entitled to assert.
func HandleListFleets(h *AdminFleetHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		aggregators := h.Managers.Managers(ctx)
		fleets := make([]Fleet, 0, len(aggregators))
		for _, agg := range aggregators {
			fleet, err := h.buildFleet(ctx, agg)
			if err != nil {
				log.Printf("admin fleet: build fleet %q: %v", agg, err)
				writeError(w, http.StatusInternalServerError, "fleet read failed, see server log")
				return
			}
			fleets = append(fleets, fleet)
		}
		writeJSON(w, http.StatusOK, fleets)
	}
}

// buildFleet assembles one aggregator's fleet: its own EndDevice plus every
// device it manages (design doc: a fleet is keyed by the aggregator's LFDI).
func (h *AdminFleetHandler) buildFleet(ctx context.Context, aggregatorLFDI string) (Fleet, error) {
	managed, err := h.Managers.ManagedBy(ctx, aggregatorLFDI)
	if err != nil {
		return Fleet{}, fmt.Errorf("ManagedBy(%q): %w", aggregatorLFDI, err)
	}
	memberLFDIs := make([]string, 0, len(managed)+1)
	memberLFDIs = append(memberLFDIs, aggregatorLFDI)
	memberLFDIs = append(memberLFDIs, managed...)

	devices := make([]FleetDevice, 0, len(memberLFDIs))
	now := nowUnix()
	rollup := FleetRollup{DeviceCount: len(memberLFDIs)}
	for _, lfdi := range memberLFDIs {
		dev, err := h.buildDevice(ctx, lfdi)
		if err != nil {
			return Fleet{}, fmt.Errorf("device %q: %w", lfdi, err)
		}
		devices = append(devices, dev)
		accumulateRollup(&rollup, dev, now)
	}

	return Fleet{AggregatorLFDI: aggregatorLFDI, Devices: devices, Rollup: rollup}, nil
}

// nowUnix is a var so a test can pin the clock without threading time
// through every call in the roll-up path.
var nowUnix = func() int64 { return time.Now().Unix() }

// buildDevice reads one device's latest DERStatus, DERAvailability and
// mirror measurements. A device with no EndDevice record (named as managed
// but never registered) still gets its mirror measurements: the trap in the
// #715 acceptance criteria is that a reading is attributed by the mirror's
// own deviceLFDI, never by whether the EndDevice store happens to know the
// device. Any store error other than store.ErrNotFound is returned, so the
// route fails (500) instead of presenting a device as having no data.
func (h *AdminFleetHandler) buildDevice(ctx context.Context, lfdi string) (FleetDevice, error) {
	meas, err := h.deviceMeasurements(ctx, lfdi)
	if err != nil {
		return FleetDevice{}, err
	}
	fd := FleetDevice{LFDI: lfdi, Measurements: meas}

	if h.EndDevices == nil {
		return fd, nil
	}
	dev, err := h.EndDevices.GetByLFDI(ctx, lfdi)
	if err != nil {
		// ErrNotFound is the ordinary case for a managed LFDI that was never
		// registered as an EndDevice: not a failure. Any other error is a
		// genuine lookup failure, distinct from "this device has no status"
		// (pkg/store's absent-versus-failed contract).
		if errors.Is(err, store.ErrNotFound) {
			return fd, nil
		}
		log.Printf("admin fleet: EndDevices.GetByLFDI(%q): %v", lfdi, err)
		return FleetDevice{}, fmt.Errorf("EndDevices.GetByLFDI(%q): %w", lfdi, err)
	}
	edevID := pathTail(dev.Href)
	fd.EdevID = edevID
	fd.Href = dev.Href
	if h.DERs == nil {
		return fd, nil
	}
	ders, err := h.DERs.List(ctx, edevID, store.ListOptions{Unbounded: true})
	if err != nil {
		// A scoped List never fails for an unknown parent (empty result,
		// nil error, per store.ScopedReader.List): any error here is a
		// genuine backend failure.
		log.Printf("admin fleet: DERs.List(%q): %v", edevID, err)
		return FleetDevice{}, fmt.Errorf("DERs.List(%q): %w", edevID, err)
	}

	var status *FleetDeviceStatus
	var avail *FleetDeviceAvailability
	for _, der := range ders.Items {
		derID := pathTail(der.Href)
		parentKey := edevID + "/" + derID
		s, err := h.latestStatus(ctx, parentKey)
		if err != nil {
			return FleetDevice{}, err
		}
		if s != nil && (status == nil || s.ReadingTime > status.ReadingTime) {
			status = s
		}
		a, err := h.latestAvailability(ctx, parentKey)
		if err != nil {
			return FleetDevice{}, err
		}
		if a != nil && (avail == nil || a.ReadingTime > avail.ReadingTime) {
			avail = a
		}
	}
	fd.Status = status
	fd.Availability = avail
	return fd, nil
}

// latestStatus returns nil, nil when the DER has no status yet
// (store.ErrNotFound) and a non-nil error for any other read failure.
func (h *AdminFleetHandler) latestStatus(ctx context.Context, parentKey string) (*FleetDeviceStatus, error) {
	if h.DERStatuses == nil {
		return nil, nil
	}
	s, err := h.DERStatuses.Get(ctx, parentKey, singletonKey)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, nil
		}
		log.Printf("admin fleet: DERStatuses.Get(%q): %v", parentKey, err)
		return nil, fmt.Errorf("DERStatuses.Get(%q): %w", parentKey, err)
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
	return out, nil
}

// latestAvailability returns nil, nil when the DER has no availability yet
// (store.ErrNotFound) and a non-nil error for any other read failure.
func (h *AdminFleetHandler) latestAvailability(ctx context.Context, parentKey string) (*FleetDeviceAvailability, error) {
	if h.DERAvailabilities == nil {
		return nil, nil
	}
	a, err := h.DERAvailabilities.Get(ctx, parentKey, singletonKey)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, nil
		}
		log.Printf("admin fleet: DERAvailabilities.Get(%q): %v", parentKey, err)
		return nil, fmt.Errorf("DERAvailabilities.Get(%q): %w", parentKey, err)
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
	return out, nil
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
// an inline overwrite never touches) - so both are read. The two are
// gathered into one slice before inheritReadingTypeByMRID runs on it, since
// inheritance does not depend on which one comes first: see that function's
// doc comment for why an inline reading is NOT always the mRID's creating
// POST (#715 fix round 2, item 1).
//
// It does NOT read readings nested inside MirrorReadingSet (the shape the
// CSIP aggregator implementation guide's own example uses, #715 fix round 3
// item 3), because vendor/github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2's
// MirrorMeterReading does not implement that field at all: its own doc
// comment names canonical sequence positions 1, 2, 4, 7 and 8 as implemented
// and leaves position 5 (MirrorReadingSet) out. encoding/xml silently drops
// an element with no matching struct field, so a client's MirrorReadingSet
// is lost before it ever reaches pkg/store, not merely unread by this admin
// route. Reading it here needs a core change first, which is out of scope
// for this branch (core is read-only per this dispatch's hard rules), so
// this is reported rather than attempted.
func (h *AdminFleetHandler) deviceMeasurements(ctx context.Context, lfdi string) (FleetDeviceMeasurements, error) {
	var out FleetDeviceMeasurements
	mirrors, err := mirrorReadingsFor(ctx, "admin GET /api/derms/fleets", h.MirrorUsagePoints, h.MirrorMeterReadings, func(device string) bool { return device == lfdi })
	if err != nil {
		return FleetDeviceMeasurements{}, err
	}
	for _, m := range mirrors {
		for i := range m.readings {
			considerMeasurement(&out, m.readings[i], h.Edition, m.isDER)
		}
	}
	return out, nil
}

// deviceMirror is one MirrorUsagePoint attributed to a device, with its
// inline and out-of-band readings gathered and their ReadingType inherited.
type deviceMirror struct {
	isDER    bool
	postRate *uint32
	readings []sep2.MirrorMeterReading
}

// mirrorReadingsFor gathers every mirror whose stored deviceLFDI satisfies
// match, for the fleet route and the DER control delivery figure alike. A nil
// mups reader yields no mirrors; any store error is returned, never read as
// "no readings". logPrefix names the calling route in the log line.
func mirrorReadingsFor(ctx context.Context, logPrefix string, mups store.ResourceReader[sep2.MirrorUsagePoint], mmrs store.ScopedReader[sep2.MirrorMeterReading], match func(deviceLFDI string) bool) ([]deviceMirror, error) {
	if mups == nil {
		return nil, nil
	}
	result, err := mups.List(ctx, store.ListOptions{Unbounded: true})
	if err != nil {
		// List never fails for an empty collection (nil error, zero
		// items): any error here is a genuine backend failure, and every
		// device's measurements are unreachable until it clears, not merely
		// this one device's.
		log.Printf("%s: MirrorUsagePoints.List: %v", logPrefix, err)
		return nil, fmt.Errorf("MirrorUsagePoints.List: %w", err)
	}
	var out []deviceMirror
	for _, mup := range result.Items {
		// The attribution point: a reading is credited to a device by the
		// mirror's own stored deviceLFDI, which #720 requires to be either
		// the poster itself or a device the poster currently manages.
		if !match(mup.DeviceLFDI) {
			continue
		}
		readings := append([]sep2.MirrorMeterReading{}, mup.MirrorMeterReading...)
		if mmrs != nil {
			mupID := pathTail(mup.Href)
			page, err := mmrs.List(ctx, mupID, store.ListOptions{Unbounded: true})
			if err != nil {
				log.Printf("%s: MirrorMeterReadings.List(%q): %v", logPrefix, mupID, err)
				return nil, fmt.Errorf("MirrorMeterReadings.List(%q): %w", mupID, err)
			}
			readings = append(readings, page.Items...)
		}
		inheritReadingTypeByMRID(readings)
		out = append(out, deviceMirror{isDER: mup.RoleFlags&roleFlagIsDER != 0, postRate: mup.PostRate, readings: readings})
	}
	return out, nil
}

// inheritReadingTypeByMRID fills a reading's ReadingType from another
// reading in readings sharing the same mRID. 2023 rule (n) / rule (h)(3): a
// MirrorMeterReading POST that reuses an mRID already established for this
// MirrorUsagePoint may omit ReadingType, and the reading is still valid,
// under the type its series was created with.
//
// This does NOT assume readings arrives in the order each reading was
// created: a rule (a)(4) re-POST of a MirrorUsagePoint rewrites its inline
// MirrorMeterReading with fresh server-owned fields (mirror.go's
// stampServerOwnedMirrorFields), so a later, untyped inline reading can sit
// earlier in the slice deviceMeasurements assembles (inline entries first,
// then out-of-band) than an established out-of-band reading of the same
// mRID that predates it. Resolving every mRID's type in one pass over the
// whole slice before filling any of them in a second pass is what makes the
// result independent of that arrangement.
func inheritReadingTypeByMRID(readings []sep2.MirrorMeterReading) {
	typeByMRID := make(map[string]*sep2.ReadingType, len(readings))
	for i := range readings {
		if readings[i].ReadingType != nil {
			if _, ok := typeByMRID[readings[i].MRID]; !ok {
				typeByMRID[readings[i].MRID] = readings[i].ReadingType
			}
		}
	}
	for i := range readings {
		if readings[i].ReadingType == nil {
			if rt, ok := typeByMRID[readings[i].MRID]; ok {
				readings[i].ReadingType = rt
			}
		}
	}
}

// considerMeasurement folds one MirrorMeterReading into out, keeping the
// newest (by LastUpdateTime) reading per quantity. A reading missing its
// type, value or unit is skipped: there is nothing to attribute it to.
//
// P and Q are mapped to export-positive: export-positive = sign * value,
// where sign depends on edition, isDER and the declared flowDirection (#715
// fix round 3 item 2, operator decision on #715):
//
//   - Edition2018, or Edition2023 with isDER false: Forward -> -1 (import),
//     Reverse -> +1 (export). This is the mapping CSIP conformance is
//     written against.
//   - Edition2023 with isDER true: the pair flips, Forward -> +1 (export),
//     Reverse -> -1 (import).
//
// A negative value under Forward or Reverse is never folded to its magnitude
// (#802): under 2018 it is kept as the sender's export-positive sign, and
// under 2023, which says such values "SHALL be positive", it is passed
// through and marked DirectionUnknown, like a reading with no flowDirection
// (#733). A zero under flowDirection 0 (none) is 0 W, unflagged.
// Net (4) is mapped only for Edition2023 with isDER true: the signed value is
// already export-positive and is kept as sent (#776). Under 2018 (reserved)
// or a non-DER mirror it stays flagged.
func considerMeasurement(out *FleetDeviceMeasurements, mmr sep2.MirrorMeterReading, edition SEP2Edition, isDER bool) {
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
	mapped := false
	if directional {
		value, mapped = exportPositive(value, rt.FlowDirection, edition, isDER)
	}

	m := &FleetMeasurement{
		Value:            value,
		ReadingTime:      mmr.LastUpdateTime,
		DirectionUnknown: directional && !mapped,
	}
	if mmr.Reading.QualityFlags != nil {
		v := uint16(*mmr.Reading.QualityFlags)
		m.QualityFlags = &v
	}
	if *slot == nil || m.ReadingTime >= (*slot).ReadingTime {
		*slot = m
	}
}

// exportPositive maps a scaled P or Q value to export-positive under the
// rule in considerMeasurement's doc comment. mapped is false when the
// direction is absent or not mappable; value is then returned unchanged.
//
// The 2018 mapping (Reverse is export) rests on 2018 Table E.2, which gives
// DER active power as ReadingType::flowDirection = 19 (Reverse).
func exportPositive(value float64, flowDirection *uint8, edition SEP2Edition, isDER bool) (float64, bool) {
	if flowDirection == nil {
		return value, false
	}
	switch *flowDirection {
	case flowDirectionNone:
		// A zero has no direction to declare, so 0 W under "none" is a
		// reading, while any other value under it has no sign to read.
		return value, value == 0
	case sep2.FlowDirectionForward, sep2.FlowDirectionReverse:
		if value < 0 {
			// 2023 says these values "SHALL be positive", so a negative one
			// is flagged. 2018 has no such clause, and by operator decision
			// (2026-10-01) a negative value is the sender's own
			// export-positive sign and is kept: the EPRI reference client
			// posts a negative value under Forward.
			return value, edition != Edition2023
		}
		export := *flowDirection == sep2.FlowDirectionReverse
		if edition == Edition2023 && isDER {
			export = !export
		}
		if export {
			return value, true
		}
		return -value, true
	case flowDirectionNet:
		return value, edition == Edition2023 && isDER
	}
	return value, false
}

// scaledValue applies a sep2 multiplier+value pair's power-of-ten scale.
func scaledValue(value float64, multiplier int8) float64 {
	return value * math.Pow10(int(multiplier))
}

// accumulateRollup folds one device into its fleet's roll-up: the three
// status counts (connected, alarmed, stale) and the four additive sums, each
// with the count of devices that did not report it.
//
// Three different staleness signals feed the sums, because three different
// qualities of timestamp are available. P and Q have the best one: their own
// FleetMeasurement.ReadingTime is server-stamped (mirror.go's
// stampServerOwnedMirrorFields overwrites it at receipt regardless of what
// the client sent), so each is judged on its own freshness (measurementStale,
// #715 fix round 2 item 4). DERStatus and DERAvailability have no server
// stamp at all - both go through the same generic singleton PUT
// (pkg/sep2srv/handlers/singleton.HandleSingletonGetPut), which stores
// whatever readingTime the client sent - so deviceStale and availabilityStale
// are each judged from their OWN resource's readingTime rather than sharing
// one signal between two independently client-set records (#715 fix round 4
// item 1): before this, a fresh DERAvailability under a stale DERStatus was
// dropped, and a stale DERAvailability under a fresh DERStatus was summed as
// current, neither of which is a fact about DERAvailability's own age.
func accumulateRollup(rollup *FleetRollup, dev FleetDevice, now int64) {
	// A device with no status at all is never-reported, not stale: it is
	// not counted in any of connected/alarmed/stale, and its sums land in
	// Unreported below, the same as any other missing value. The same rule
	// applies to availabilityStale below for DERAvailability.
	deviceStale := dev.Status != nil && clientClockStale(dev.Status.ReadingTime, now)

	switch {
	case dev.Status == nil:
	case deviceStale:
		rollup.Stale++
	default:
		if dev.Status.Connected != nil && *dev.Status.Connected {
			rollup.Connected++
		}
	}
	if dev.Status != nil && dev.Status.AlarmStatus != nil && *dev.Status.AlarmStatus != 0 {
		rollup.Alarmed++
	}

	var statW, statVar *float64
	availabilityStale := dev.Availability != nil && clientClockStale(dev.Availability.ReadingTime, now)
	if dev.Availability != nil {
		statW, statVar = dev.Availability.StatWAvail, dev.Availability.StatVarAvail
	}
	addToSum(&rollup.P, measurementValue(dev.Measurements.P), measurementStale(dev.Measurements.P, now), measurementUndirected(dev.Measurements.P))
	addToSum(&rollup.Q, measurementValue(dev.Measurements.Q), measurementStale(dev.Measurements.Q, now), measurementUndirected(dev.Measurements.Q))
	addToSum(&rollup.StatWAvail, statW, availabilityStale, false)
	addToSum(&rollup.StatVarAvail, statVar, availabilityStale, false)
}

// clientClockStale reports whether now minus a client-authored readingTime
// exceeds staleAfterSeconds, flooring a negative age (a future-dated
// reading) at 0 rather than letting it read as more fresh than fresh (#715
// fix round 4 item 2): DERStatus.readingTime and DERAvailability.readingTime
// are both entirely client-set (see accumulateRollup's doc comment), and the
// standard sets no coordinated-clock guarantee for either - devices are only
// asked (SHOULD, not SHALL) to stay within a display tolerance, and
// "intentionally uncoordinated time" is a valid quality class. A future date
// is therefore this server's own policy to bound, not a client error to
// refuse: a status or availability dated arbitrarily far in the future is
// judged exactly current, never negative-age, under the same
// staleAfterSeconds threshold every other client-set clock in this file
// uses.
func clientClockStale(readingTime, now int64) bool {
	return clampedAge(readingTime, now) > staleAfterSeconds
}

// clampedAge returns now minus readingTime, floored at 0 (#715 fix round 4
// item 2): a future-dated client-set readingTime must never produce a
// negative age, which would read as "more fresh than fresh" wherever an age
// rather than a stale/not-stale boolean is consulted. Split out from
// clientClockStale so the floor itself is directly testable: the boolean
// clientClockStale returns is identical whether a future date floors to 0 or
// stays negative (both fail the ">" staleness comparison), so a test on the
// boolean alone cannot prove the floor exists.
func clampedAge(readingTime, now int64) int64 {
	age := now - readingTime
	if age < 0 {
		return 0
	}
	return age
}

// measurementStale reports whether m's own server-stamped reading time is
// older than staleAfterSeconds. A nil m is unreported, not stale: addToSum
// checks staleness before the unreported case, so this must answer false for
// a nil m or an absent value would be miscounted as stale. Server-stamped,
// unlike clientClockStale's inputs, so it is never negative and needs no
// floor.
func measurementStale(m *FleetMeasurement, now int64) bool {
	if m == nil {
		return false
	}
	return now-m.ReadingTime > staleAfterSeconds
}

func measurementValue(m *FleetMeasurement) *float64 {
	if m == nil {
		return nil
	}
	return &m.Value
}

func measurementUndirected(m *FleetMeasurement) bool {
	return m != nil && m.DirectionUnknown
}

// addToSum adds value to sum.Sum when the device is neither stale nor
// missing the value. A stale device's value, if any, counts toward
// sum.Stale instead of Sum, ahead of the unreported check: staleness is a
// fact about the device's last known state, distinct from never having
// reported at all. A missing value never contributes a synthesized zero to
// Sum. undirected marks a contributing value that had no flowDirection, which
// flags the whole sum DirectionUnknown (#733).
func addToSum(sum *FleetSum, value *float64, stale bool, undirected bool) {
	switch {
	case stale:
		sum.Stale++
	case value == nil:
		sum.Unreported++
	default:
		sum.Sum += *value
		if undirected {
			sum.DirectionUnknown = true
		}
	}
}
