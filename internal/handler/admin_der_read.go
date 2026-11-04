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
// receipt. A reader must not take their absence for "not reported". Status
// and capability model every element, so their lists are empty.
var (
	notStoredCapability = []string{}
	notStoredSettings   = []string{
		"setESDelay", "setESHighFreq", "setESHighVolt", "setESLowFreq", "setESLowVolt",
		"setESRampTms", "setESRandomDelay", "setGradW", "setMaxA", "setMaxAh",
		"setMaxChargeRateVA", "setMaxDischargeRateVA", "setMaxV", "setMaxVA",
		"setMaxVarNeg", "setMaxWh", "setMinPFOverExcited", "setMinPFUnderExcited",
		"setMinV", "setSoftGradW", "setVNom", "setVRef", "setVRefOfs",
	}
	notStoredStatus = []string{}
)

// section is the part of every resource answer that says whether it was
// read. Error is empty unless the read failed.
type section struct {
	Reported bool   `json:"reported"`
	Error    string `json:"error"`
}

type derStatusView struct {
	section
	// ReadingTime is null when the resource is not reported or carries none.
	ReadingTime *int64               `json:"readingTime"`
	Freshness   *derstatus.Freshness `json:"freshness"`
	Connect     *derstatus.Connect   `json:"connect"`
	Inverter    *derstatus.Inverter  `json:"inverter"`
	// OperationalMode, StorageMode and StateOfCharge are the element values
	// as posted: a code, and for the state of charge hundredths of a percent.
	OperationalMode *uint8  `json:"operationalMode"`
	StorageMode     *uint8  `json:"storageMode"`
	StateOfCharge   *uint16 `json:"stateOfCharge"`
	// LocalControlMode and ManufacturerStatus are the element values as
	// posted: a code, and the manufacturer's own string.
	LocalControlMode   *uint8           `json:"localControlModeStatus"`
	ManufacturerStatus *string          `json:"manufacturerStatus"`
	Alarm              *derstatus.Alarm `json:"alarm"`
	// Since maps an element's XML name to the Unix second its current value
	// was first seen. It is {} when the store can record times and has none
	// for this resource, and null when the resource is not reported or the
	// store cannot record times at all. An element with no entry has none
	// recorded.
	Since     map[string]int64 `json:"since"`
	NotStored []string         `json:"notStored"`
}

type derCapabilityView struct {
	section
	Type                   *uint8            `json:"type"`
	ModesSupported         *dercontrol.Modes `json:"modesSupported"`
	RTGAbnormalCategory    *uint8            `json:"rtgAbnormalCategory"`
	RTGNormalCategory      *uint8            `json:"rtgNormalCategory"`
	RTGMaxA                *frScaled         `json:"rtgMaxA"`
	RTGMaxAh               *frScaled         `json:"rtgMaxAh"`
	RTGMaxW                *frScaled         `json:"rtgMaxW"`
	RTGMaxWh               *frScaled         `json:"rtgMaxWh"`
	RTGMaxVA               *frScaled         `json:"rtgMaxVA"`
	RTGMaxVar              *frScaled         `json:"rtgMaxVar"`
	RTGMaxVarNeg           *frScaled         `json:"rtgMaxVarNeg"`
	RTGMaxV                *frScaled         `json:"rtgMaxV"`
	RTGMinV                *frScaled         `json:"rtgMinV"`
	RTGVNom                *frScaled         `json:"rtgVNom"`
	RTGMaxChargeRateW      *frScaled         `json:"rtgMaxChargeRateW"`
	RTGMaxDischargeRateW   *frScaled         `json:"rtgMaxDischargeRateW"`
	RTGMaxChargeRateVA     *frScaled         `json:"rtgMaxChargeRateVA"`
	RTGMaxDischargeRateVA  *frScaled         `json:"rtgMaxDischargeRateVA"`
	RTGOverExcitedW        *frScaled         `json:"rtgOverExcitedW"`
	RTGUnderExcitedW       *frScaled         `json:"rtgUnderExcitedW"`
	RTGOverExcitedPF       *frScaled         `json:"rtgOverExcitedPF"`
	RTGUnderExcitedPF      *frScaled         `json:"rtgUnderExcitedPF"`
	RTGMinPFOverExcited    *frScaled         `json:"rtgMinPFOverExcited"`
	RTGMinPFUnderExcited   *frScaled         `json:"rtgMinPFUnderExcited"`
	RTGReactiveSusceptance *frScaled         `json:"rtgReactiveSusceptance"`
	NotStored              []string          `json:"notStored"`
}

type derSettingsView struct {
	section
	UpdatedTime          *int64            `json:"updatedTime"`
	ModesEnabled         *dercontrol.Modes `json:"modesEnabled"`
	SetMaxW              *frScaled         `json:"setMaxW"`
	SetMaxVar            *frScaled         `json:"setMaxVar"`
	SetMaxChargeRateW    *frScaled         `json:"setMaxChargeRateW"`
	SetMaxDischargeRateW *frScaled         `json:"setMaxDischargeRateW"`
	NotStored            []string          `json:"notStored"`
}

type derAvailabilityView struct {
	section
	ReadingTime          *int64               `json:"readingTime"`
	Freshness            *derstatus.Freshness `json:"freshness"`
	AvailabilityDuration *uint32              `json:"availabilityDuration"`
	MaxChargeDuration    *uint32              `json:"maxChargeDuration"`
	ReservePercent       *uint16              `json:"reservePercent"`
	ReserveChargePercent *uint16              `json:"reserveChargePercent"`
	StatWAvail           *frScaled            `json:"statWAvail"`
	StatVarAvail         *frScaled            `json:"statVarAvail"`
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
	v.ReadingTime = readingTimeOf(s.ReadingTime)
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
	if s.LocalControlModeStatus != nil {
		v.LocalControlMode = &s.LocalControlModeStatus.Value
	}
	if s.ManufacturerStatus != nil {
		v.ManufacturerStatus = &s.ManufacturerStatus.Value
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
	v.RTGAbnormalCategory = c.RTGAbnormalCategory
	v.RTGNormalCategory = c.RTGNormalCategory
	v.RTGMaxA = unsignedScaled(c.RTGMaxA)
	v.RTGMaxAh = unsignedScaled(c.RTGMaxAh)
	v.RTGMaxW = signedScaled(c.RTGMaxW)
	v.RTGMaxWh = unsignedScaled(c.RTGMaxWh)
	v.RTGMaxVA = unsignedScaled(c.RTGMaxVA)
	v.RTGMaxVar = signedScaled(c.RTGMaxVar)
	v.RTGMaxVarNeg = signedScaled(c.RTGMaxVarNeg)
	v.RTGMaxV = unsignedScaled(c.RTGMaxV)
	v.RTGMinV = unsignedScaled(c.RTGMinV)
	v.RTGVNom = unsignedScaled(c.RTGVNom)
	v.RTGMaxChargeRateW = signedScaled(c.RTGMaxChargeRateW)
	v.RTGMaxDischargeRateW = signedScaled(c.RTGMaxDischargeRateW)
	v.RTGMaxChargeRateVA = unsignedScaled(c.RTGMaxChargeRateVA)
	v.RTGMaxDischargeRateVA = unsignedScaled(c.RTGMaxDischargeRateVA)
	v.RTGOverExcitedW = signedScaled(c.RTGOverExcitedW)
	v.RTGUnderExcitedW = signedScaled(c.RTGUnderExcitedW)
	v.RTGOverExcitedPF = powerFactorOf(c.RTGOverExcitedPF)
	v.RTGUnderExcitedPF = powerFactorOf(c.RTGUnderExcitedPF)
	v.RTGMinPFOverExcited = powerFactorOf(c.RTGMinPFOverExcited)
	v.RTGMinPFUnderExcited = powerFactorOf(c.RTGMinPFUnderExcited)
	v.RTGReactiveSusceptance = unsignedScaled(c.RTGReactiveSusceptance)
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
	v.SetMaxW = signedScaled(s.SetMaxW)
	v.SetMaxVar = signedScaled(s.SetMaxVar)
	v.SetMaxChargeRateW = signedScaled(s.SetMaxChargeRateW)
	v.SetMaxDischargeRateW = signedScaled(s.SetMaxDischargeRateW)
	return v
}

func (h *AdminDERReadHandler) availability(ctx context.Context, key string, now int64) derAvailabilityView {
	v := derAvailabilityView{}
	a, ok := read(ctx, "DERAvailabilities", h.DERAvailabilities, key, &v.section)
	if !ok {
		return v
	}
	fresh := derstatus.Assess(a.ReadingTime, now, derstatus.DefaultPollRateSeconds)
	v.ReadingTime = readingTimeOf(a.ReadingTime)
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
	v.StatWAvail = signedScaled(a.StatWAvail)
	v.StatVarAvail = signedScaled(a.StatVarAvail)
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

// signedQty and unsignedQty are the core quantity types that carry a
// power-of-ten multiplier and a 16-bit value. Conversion ignores struct tags,
// so one body serves every type; a new core quantity of either shape needs
// only an entry here.
type signedQty interface {
	~struct {
		Multiplier int8  `xml:"multiplier"`
		Value      int16 `xml:"value"`
	}
}

type unsignedQty interface {
	~struct {
		Multiplier int8   `xml:"multiplier"`
		Value      uint16 `xml:"value"`
	}
}

// signedScaled and unsignedScaled are nil for an absent quantity, never 0.
func signedScaled[T signedQty](p *T) *frScaled {
	if p == nil {
		return nil
	}
	q := struct {
		Multiplier int8
		Value      int16
	}(*p)
	return newScaled(int64(q.Value), q.Multiplier)
}

func unsignedScaled[T unsignedQty](p *T) *frScaled {
	if p == nil {
		return nil
	}
	q := struct {
		Multiplier int8
		Value      uint16
	}(*p)
	return newScaled(int64(q.Value), q.Multiplier)
}

// powerFactorOf serves a power factor as its displacement and multiplier.
func powerFactorOf(p *sep2.PowerFactor) *frScaled {
	if p == nil {
		return nil
	}
	return newScaled(int64(p.Displacement), p.Multiplier)
}

// readingTimeOf is nil for a resource that carries no readingTime, which is
// never read as the Unix epoch.
func readingTimeOf(t int64) *int64 {
	if t == 0 {
		return nil
	}
	return &t
}
