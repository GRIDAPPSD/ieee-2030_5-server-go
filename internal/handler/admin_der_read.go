package handler

import (
	"context"
	"errors"
	"log"
	"net/http"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/dercontrol"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/derstatus"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/logsafe"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
)

// AdminDERReadHandler serves GET /api/derms/devices/{edevId}/ders: per DER of
// one EndDevice, the four stored singletons as the server holds them. Each
// reply field answers one of three ways: a value, null (the resource is
// stored but lacks the element), or, for a whole resource, reported false
// (nothing stored) or an error (the read failed).
type AdminDERReadHandler struct {
	EndDevices        store.EndDeviceReader
	DERs              store.ScopedReader[sep2.DER]
	DERStatuses       store.ScopedReader[sep2.DERStatus]
	DERCapabilities   store.ScopedReader[sep2.DERCapability]
	DERSettings       store.ScopedReader[sep2.DERSettings]
	DERAvailabilities store.ScopedReader[sep2.DERAvailability]
	// Now is the clock freshness is judged against; nil means time.Now.
	Now func() time.Time
}

// derReadFailed is what a reply says about a failed read. The cause goes to the
// log, not to the caller.
const derReadFailed = "read failed, see server log"

// notStored* name the IEEE 2030.5-2018 elements of each resource that the
// vendored core structs do not model, so a PUT carrying them drops them on
// receipt. A reader must not take their absence for "not reported".
var (
	notStoredCapability = []string{
		"rtgAbnormalCategory", "rtgMaxAh", "rtgMaxChargeRateVA", "rtgMaxDischargeRateVA",
		"rtgMaxVarNeg", "rtgMaxWh", "rtgMinPFOverExcited", "rtgMinPFUnderExcited",
		"rtgMinV", "rtgNormalCategory", "rtgOverExcitedPF", "rtgOverExcitedW",
		"rtgReactiveSusceptance", "rtgUnderExcitedPF", "rtgUnderExcitedW", "rtgVNom",
	}
	notStoredSettings = []string{
		"setESDelay", "setESHighFreq", "setESHighVolt", "setESLowFreq", "setESLowVolt",
		"setESRampTms", "setESRandomDelay", "setGradW", "setMaxA", "setMaxAh",
		"setMaxChargeRateVA", "setMaxDischargeRateVA", "setMaxV", "setMaxVA",
		"setMaxVarNeg", "setMaxWh", "setMinPFOverExcited", "setMinPFUnderExcited",
		"setMinV", "setSoftGradW", "setVNom", "setVRef", "setVRefOfs",
	}
	notStoredStatus = []string{"localControlModeStatus", "manufacturerStatus"}
)

// quantity is a value with its power-of-ten multiplier, unscaled.
type quantity struct {
	Value      int64 `json:"value"`
	Multiplier int8  `json:"multiplier"`
}

// section is the part of every resource answer that says whether it was
// read. Error is empty unless the read failed.
type section struct {
	Reported bool   `json:"reported"`
	Error    string `json:"error"`
}

type derStatusView struct {
	section
	ReadingTime int64                `json:"readingTime"`
	Freshness   *derstatus.Freshness `json:"freshness"`
	Connect     *derstatus.Connect   `json:"connect"`
	Inverter    *derstatus.Inverter  `json:"inverter"`
	// OperationalMode, StorageMode and StateOfCharge are the element values
	// as posted: a code, and for the state of charge hundredths of a percent.
	OperationalMode *uint8           `json:"operationalMode"`
	StorageMode     *uint8           `json:"storageMode"`
	StateOfCharge   *uint16          `json:"stateOfCharge"`
	Alarm           *derstatus.Alarm `json:"alarm"`
	// Since maps an element's XML name to the Unix second its current value
	// was first seen. It is null when the store keeps no such times, and an
	// element with no entry has none recorded.
	Since     map[string]int64 `json:"since"`
	NotStored []string         `json:"notStored"`
}

type derCapabilityView struct {
	section
	Type                 *uint8            `json:"type"`
	ModesSupported       *dercontrol.Modes `json:"modesSupported"`
	RTGMaxA              *int32            `json:"rtgMaxA"`
	RTGMaxW              *quantity         `json:"rtgMaxW"`
	RTGMaxVA             *quantity         `json:"rtgMaxVA"`
	RTGMaxVar            *quantity         `json:"rtgMaxVar"`
	RTGMaxV              *quantity         `json:"rtgMaxV"`
	RTGMaxChargeRateW    *quantity         `json:"rtgMaxChargeRateW"`
	RTGMaxDischargeRateW *quantity         `json:"rtgMaxDischargeRateW"`
	NotStored            []string          `json:"notStored"`
}

type derSettingsView struct {
	section
	UpdatedTime          *int64            `json:"updatedTime"`
	ModesEnabled         *dercontrol.Modes `json:"modesEnabled"`
	SetMaxW              *quantity         `json:"setMaxW"`
	SetMaxVar            *quantity         `json:"setMaxVar"`
	SetMaxChargeRateW    *quantity         `json:"setMaxChargeRateW"`
	SetMaxDischargeRateW *quantity         `json:"setMaxDischargeRateW"`
	NotStored            []string          `json:"notStored"`
}

type derAvailabilityView struct {
	section
	ReadingTime          int64                `json:"readingTime"`
	Freshness            *derstatus.Freshness `json:"freshness"`
	AvailabilityDuration *uint32              `json:"availabilityDuration"`
	MaxChargeDuration    *uint32              `json:"maxChargeDuration"`
	ReservePercent       *uint16              `json:"reservePercent"`
	ReserveChargePercent *uint16              `json:"reserveChargePercent"`
	StatWAvail           *quantity            `json:"statWAvail"`
	StatVarAvail         *quantity            `json:"statVarAvail"`
	Since                map[string]int64     `json:"since"`
}

type derView struct {
	ID           string              `json:"id"`
	Href         string              `json:"href"`
	Status       derStatusView       `json:"status"`
	Capability   derCapabilityView   `json:"capability"`
	Settings     derSettingsView     `json:"settings"`
	Availability derAvailabilityView `json:"availability"`
}

type derReadReply struct {
	Device          string    `json:"device"`
	Now             int64     `json:"now"`
	PollRateSeconds uint32    `json:"pollRateSeconds"`
	DERs            []derView `json:"ders"`
}

// HandleList serves GET /api/derms/devices/{edevId}/ders. An unknown
// EndDevice is 404 and a failure listing its DERs is 500; a failure reading
// one resource is answered inside that resource's section.
func (h *AdminDERReadHandler) HandleList() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		edevID := r.PathValue("edevId")
		if _, err := h.EndDevices.Get(ctx, edevID); err != nil {
			if errors.Is(err, store.ErrNotFound) {
				writeError(w, http.StatusNotFound, "device not found")
				return
			}
			log.Printf("admin der read: EndDevices.Get(%s): %s", logsafe.Str(edevID), logsafe.Err(err))
			writeError(w, http.StatusInternalServerError, derReadFailed)
			return
		}
		ders, err := h.DERs.List(ctx, edevID, store.ListOptions{Unbounded: true})
		if err != nil {
			log.Printf("admin der read: DERs.List(%s): %s", logsafe.Str(edevID), logsafe.Err(err))
			writeError(w, http.StatusInternalServerError, derReadFailed)
			return
		}
		now := time.Now
		if h.Now != nil {
			now = h.Now
		}
		at := now().Unix()
		reply := derReadReply{
			Device: edevID, Now: at, PollRateSeconds: derstatus.DefaultPollRateSeconds,
			DERs: make([]derView, 0, len(ders.Items)),
		}
		for _, der := range ders.Items {
			derID := pathTail(der.Href)
			key := edevID + "/" + derID
			reply.DERs = append(reply.DERs, derView{
				ID: derID, Href: der.Href,
				Status:       h.status(ctx, key, at),
				Capability:   h.capability(ctx, key),
				Settings:     h.settings(ctx, key),
				Availability: h.availability(ctx, key, at),
			})
		}
		writeJSON(w, http.StatusOK, reply)
	}
}

// read fetches the singleton stored under key. It reports whether one was
// read; a failure is logged and answered through sec.
func read[T store.Copier[T]](ctx context.Context, name string, rd store.ScopedReader[T], key string, sec *section) (T, bool) {
	var zero T
	if store.IsAbsent(rd) {
		sec.Error = "resource store not configured"
		return zero, false
	}
	v, err := rd.Get(ctx, key, singletonKey)
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			log.Printf("admin der read: %s.Get(%s): %s", logsafe.Str(name), logsafe.Str(key), logsafe.Err(err))
			sec.Error = derReadFailed
		}
		return zero, false
	}
	sec.Reported = true
	return v, true
}

// sinceOf returns the first-seen times rd keeps for key, or nil when rd keeps
// none.
func sinceOf(rd any, key string) map[string]int64 {
	sr, ok := rd.(store.ElementSinceReader)
	if !ok || store.IsAbsent(sr) {
		return nil
	}
	out := map[string]int64{}
	for name, t := range sr.ElementSince(key, singletonKey) {
		out[name] = t.Unix()
	}
	return out
}

func (h *AdminDERReadHandler) status(ctx context.Context, key string, now int64) derStatusView {
	v := derStatusView{NotStored: notStoredStatus}
	s, ok := read(ctx, "DERStatuses", h.DERStatuses, key, &v.section)
	if !ok {
		return v
	}
	fresh := derstatus.Assess(s.ReadingTime, now, derstatus.DefaultPollRateSeconds)
	v.ReadingTime = s.ReadingTime
	v.Freshness = &fresh
	v.Connect = derstatus.ConnectOf(s)
	v.Inverter = derstatus.InverterOf(s)
	v.Alarm = derstatus.AlarmOf(s)
	if s.OperationalModeStatus != nil {
		v.OperationalMode = &s.OperationalModeStatus.Value
	}
	if s.StorageModeStatus != nil {
		v.StorageMode = &s.StorageModeStatus.Value
	}
	if s.StateOfChargeStatus != nil {
		v.StateOfCharge = &s.StateOfChargeStatus.Value
	}
	v.Since = sinceOf(h.DERStatuses, key)
	return v
}

func (h *AdminDERReadHandler) capability(ctx context.Context, key string) derCapabilityView {
	v := derCapabilityView{NotStored: notStoredCapability}
	c, ok := read(ctx, "DERCapabilities", h.DERCapabilities, key, &v.section)
	if !ok {
		return v
	}
	v.Type = c.Type
	v.ModesSupported = modesOf(c.ModesSupported)
	v.RTGMaxA = c.RTGMaxA
	v.RTGMaxW = activePower(c.RTGMaxW)
	v.RTGMaxVA = apparentPower(c.RTGMaxVA)
	v.RTGMaxVar = reactivePower(c.RTGMaxVar)
	v.RTGMaxV = voltage(c.RTGMaxV)
	v.RTGMaxChargeRateW = activePower(c.RTGMaxChargeRateW)
	v.RTGMaxDischargeRateW = activePower(c.RTGMaxDischargeRateW)
	return v
}

func (h *AdminDERReadHandler) settings(ctx context.Context, key string) derSettingsView {
	v := derSettingsView{NotStored: notStoredSettings}
	s, ok := read(ctx, "DERSettings", h.DERSettings, key, &v.section)
	if !ok {
		return v
	}
	if s.UpdatedTime != 0 {
		v.UpdatedTime = &s.UpdatedTime
	}
	v.ModesEnabled = modesOf(s.ModesEnabled)
	v.SetMaxW = activePower(s.SetMaxW)
	v.SetMaxVar = reactivePower(s.SetMaxVar)
	v.SetMaxChargeRateW = activePower(s.SetMaxChargeRateW)
	v.SetMaxDischargeRateW = activePower(s.SetMaxDischargeRateW)
	return v
}

func (h *AdminDERReadHandler) availability(ctx context.Context, key string, now int64) derAvailabilityView {
	v := derAvailabilityView{}
	a, ok := read(ctx, "DERAvailabilities", h.DERAvailabilities, key, &v.section)
	if !ok {
		return v
	}
	fresh := derstatus.Assess(a.ReadingTime, now, derstatus.DefaultPollRateSeconds)
	v.ReadingTime = a.ReadingTime
	v.Freshness = &fresh
	v.AvailabilityDuration = a.AvailabilityDuration
	v.MaxChargeDuration = a.MaxChargeDuration
	if a.ReservePercent != nil {
		p := uint16(*a.ReservePercent)
		v.ReservePercent = &p
	}
	if a.ReserveChargePercent != nil {
		p := uint16(*a.ReserveChargePercent)
		v.ReserveChargePercent = &p
	}
	v.StatWAvail = activePower(a.StatWAvail)
	v.StatVarAvail = reactivePower(a.StatVarAvail)
	v.Since = sinceOf(h.DERAvailabilities, key)
	return v
}

func modesOf(t *sep2.DERControlType) *dercontrol.Modes {
	if t == nil {
		return nil
	}
	m := dercontrol.ModesOf(*t)
	return &m
}

func activePower(p *sep2.ActivePower) *quantity {
	if p == nil {
		return nil
	}
	return &quantity{Value: int64(p.Value), Multiplier: p.Multiplier}
}

func reactivePower(p *sep2.ReactivePower) *quantity {
	if p == nil {
		return nil
	}
	return &quantity{Value: int64(p.Value), Multiplier: p.Multiplier}
}

func apparentPower(p *sep2.ApparentPower) *quantity {
	if p == nil {
		return nil
	}
	return &quantity{Value: int64(p.Value), Multiplier: p.Multiplier}
}

func voltage(p *sep2.VoltageRMS) *quantity {
	if p == nil {
		return nil
	}
	return &quantity{Value: int64(p.Value), Multiplier: p.Multiplier}
}
