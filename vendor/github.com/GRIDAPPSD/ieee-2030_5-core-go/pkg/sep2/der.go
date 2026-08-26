package sep2

import "encoding/xml"

// DERControlBase contains all DER operating mode parameters.
// Spec reference: IEEE 2030.5 section 10.10.
//
// Curve-reference fields (OpModVoltVar, OpModVoltWatt, OpModFreqWatt,
// OpModLVRT*, OpModHVRT*, OpModLFRT*, OpModHFRT*) are typed as *int32
// to match the established project convention introduced with
// OpModVoltVar: the value names the referenced DERCurve resource by
// the int32 hash/ID the global /dc store keys on. Per IEEE 2030.5 the
// schema element is a DERCurveLink (Link to a DERCurve); the project
// trades wire fidelity for an integer ref keyed off the same /dc store.
type DERControlBase struct {
	OpModConnect                *bool             `xml:"opModConnect,omitempty"`
	OpModEnergize               *bool             `xml:"opModEnergize,omitempty"`
	OpModFixedPFAbsorbW         *FixedPowerFactor `xml:"opModFixedPFAbsorbW,omitempty"`
	OpModFixedPFInjectW         *FixedPowerFactor `xml:"opModFixedPFInjectW,omitempty"`
	OpModFixedVar               *ReactivePower    `xml:"opModFixedVar,omitempty"`
	OpModFixedW                 *ActivePower      `xml:"opModFixedW,omitempty"`
	OpModFreqDroop              *uint16           `xml:"opModFreqDroop,omitempty"`
	OpModFreqWatt               *int32            `xml:"opModFreqWatt,omitempty"`
	OpModHFRTMustTrip           *int32            `xml:"opModHFRTMustTrip,omitempty"`
	OpModHVRTMomentaryCessation *int32            `xml:"opModHVRTMomentaryCessation,omitempty"`
	OpModHVRTMustTrip           *int32            `xml:"opModHVRTMustTrip,omitempty"`
	OpModLFRTMustTrip           *int32            `xml:"opModLFRTMustTrip,omitempty"`
	OpModLVRTMomentaryCessation *int32            `xml:"opModLVRTMomentaryCessation,omitempty"`
	OpModLVRTMustTrip           *int32            `xml:"opModLVRTMustTrip,omitempty"`
	OpModMaxLimW                *ActivePower      `xml:"opModMaxLimW,omitempty"`
	OpModTargetVar              *ReactivePower    `xml:"opModTargetVar,omitempty"`
	OpModTargetW                *ActivePower      `xml:"opModTargetW,omitempty"`
	OpModVoltVar                *int32            `xml:"opModVoltVar,omitempty"`
	OpModVoltWatt               *int32            `xml:"opModVoltWatt,omitempty"`
	RampTms                     *uint16           `xml:"rampTms,omitempty"`
}

// Copy returns an independent copy.
func (d DERControlBase) Copy() DERControlBase {
	c := d
	if d.OpModConnect != nil {
		v := *d.OpModConnect
		c.OpModConnect = &v
	}
	if d.OpModEnergize != nil {
		v := *d.OpModEnergize
		c.OpModEnergize = &v
	}
	if d.OpModFixedPFAbsorbW != nil {
		v := *d.OpModFixedPFAbsorbW
		c.OpModFixedPFAbsorbW = &v
	}
	if d.OpModFixedPFInjectW != nil {
		v := *d.OpModFixedPFInjectW
		c.OpModFixedPFInjectW = &v
	}
	if d.OpModFixedW != nil {
		v := *d.OpModFixedW
		c.OpModFixedW = &v
	}
	if d.OpModFixedVar != nil {
		v := *d.OpModFixedVar
		c.OpModFixedVar = &v
	}
	if d.OpModFreqDroop != nil {
		v := *d.OpModFreqDroop
		c.OpModFreqDroop = &v
	}
	if d.OpModFreqWatt != nil {
		v := *d.OpModFreqWatt
		c.OpModFreqWatt = &v
	}
	if d.OpModHFRTMustTrip != nil {
		v := *d.OpModHFRTMustTrip
		c.OpModHFRTMustTrip = &v
	}
	if d.OpModHVRTMomentaryCessation != nil {
		v := *d.OpModHVRTMomentaryCessation
		c.OpModHVRTMomentaryCessation = &v
	}
	if d.OpModHVRTMustTrip != nil {
		v := *d.OpModHVRTMustTrip
		c.OpModHVRTMustTrip = &v
	}
	if d.OpModLFRTMustTrip != nil {
		v := *d.OpModLFRTMustTrip
		c.OpModLFRTMustTrip = &v
	}
	if d.OpModLVRTMomentaryCessation != nil {
		v := *d.OpModLVRTMomentaryCessation
		c.OpModLVRTMomentaryCessation = &v
	}
	if d.OpModLVRTMustTrip != nil {
		v := *d.OpModLVRTMustTrip
		c.OpModLVRTMustTrip = &v
	}
	if d.OpModMaxLimW != nil {
		v := *d.OpModMaxLimW
		c.OpModMaxLimW = &v
	}
	if d.OpModTargetW != nil {
		v := *d.OpModTargetW
		c.OpModTargetW = &v
	}
	if d.OpModTargetVar != nil {
		v := *d.OpModTargetVar
		c.OpModTargetVar = &v
	}
	if d.OpModVoltVar != nil {
		v := *d.OpModVoltVar
		c.OpModVoltVar = &v
	}
	if d.OpModVoltWatt != nil {
		v := *d.OpModVoltWatt
		c.OpModVoltWatt = &v
	}
	if d.RampTms != nil {
		v := *d.RampTms
		c.RampTms = &v
	}
	return c
}

// DERControl is a specific DER control action.
type DERControl struct {
	XMLName xml.Name `xml:"urn:ieee:std:2030.5:ns DERControl"`
	RandomizableEvent
	DERControlBase *DERControlBase `xml:"DERControlBase,omitempty"`
}

// Copy returns an independent copy.
func (d DERControl) Copy() DERControl {
	c := d
	if d.ResponseRequired != nil {
		v := *d.ResponseRequired
		c.ResponseRequired = &v
	}
	if d.EventStatus != nil {
		es := d.EventStatus.Copy()
		c.EventStatus = &es
	}
	if d.Interval != nil {
		i := *d.Interval
		c.Interval = &i
	}
	if d.RandomizeDuration != nil {
		v := *d.RandomizeDuration
		c.RandomizeDuration = &v
	}
	if d.RandomizeStart != nil {
		v := *d.RandomizeStart
		c.RandomizeStart = &v
	}
	if d.DERControlBase != nil {
		cb := d.DERControlBase.Copy()
		c.DERControlBase = &cb
	}
	return c
}

// DERControlList is a list of DERControl resources.
type DERControlList struct {
	XMLName xml.Name `xml:"urn:ieee:std:2030.5:ns DERControlList"`
	ListResource
	DERControl []DERControl `xml:"DERControl,omitempty"`
}

// DefaultDERControl is the default operating parameters when no active control.
// Spec reference: IEEE 2030.5 section 10.11.
//
// SetGradW and SetSoftGradW are device-level default ramp rates
// (hundredths of percent per second) per IEEE 2030.5 section 10.11. They live
// on DefaultDERControl directly rather than DERControlBase because the
// spec scopes them as device defaults, not per-event overrides.
type DefaultDERControl struct {
	XMLName xml.Name `xml:"urn:ieee:std:2030.5:ns DefaultDERControl"`
	SubscribableResource
	MRID           string          `xml:"mRID,omitempty"`
	DERControlBase *DERControlBase `xml:"DERControlBase,omitempty"`
	SetGradW       *uint16         `xml:"setGradW,omitempty"`
	SetSoftGradW   *uint16         `xml:"setSoftGradW,omitempty"`
}

// Copy returns an independent copy.
func (d DefaultDERControl) Copy() DefaultDERControl {
	c := d
	if d.DERControlBase != nil {
		cb := d.DERControlBase.Copy()
		c.DERControlBase = &cb
	}
	if d.SetGradW != nil {
		v := *d.SetGradW
		c.SetGradW = &v
	}
	if d.SetSoftGradW != nil {
		v := *d.SetSoftGradW
		c.SetSoftGradW = &v
	}
	return c
}

// DERProgram contains controls and curves for DER devices.
//
// Field order matches the SubscribableIdentifiedObject + DERProgram
// combined xsd:sequence: mRID, description (base; version omitted, not
// yet modeled) THEN ActiveDERControlListLink, DefaultDERControlLink,
// DERControlListLink, DERCurveListLink, primacy (DERProgram's own
// sequence, primacy LAST).
type DERProgram struct {
	XMLName xml.Name `xml:"urn:ieee:std:2030.5:ns DERProgram"`
	SubscribableResource
	MRID        string `xml:"mRID,omitempty"`
	Description string `xml:"description,omitempty"`

	ActiveDERControlListLink *ListLink `xml:"ActiveDERControlListLink,omitempty"`
	DefaultDERControlLink    *Link     `xml:"DefaultDERControlLink,omitempty"`
	DERControlListLink       *ListLink `xml:"DERControlListLink,omitempty"`
	DERCurveListLink         *ListLink `xml:"DERCurveListLink,omitempty"`
	Primacy                  uint8     `xml:"primacy"`
}

// Copy returns an independent copy.
func (p DERProgram) Copy() DERProgram {
	c := p
	if p.ActiveDERControlListLink != nil {
		l := *p.ActiveDERControlListLink
		c.ActiveDERControlListLink = &l
	}
	if p.DefaultDERControlLink != nil {
		l := *p.DefaultDERControlLink
		c.DefaultDERControlLink = &l
	}
	if p.DERControlListLink != nil {
		l := *p.DERControlListLink
		c.DERControlListLink = &l
	}
	if p.DERCurveListLink != nil {
		l := *p.DERCurveListLink
		c.DERCurveListLink = &l
	}
	return c
}

// DERProgramList is a list of DERProgram resources.
type DERProgramList struct {
	XMLName xml.Name `xml:"urn:ieee:std:2030.5:ns DERProgramList"`
	ListResource
	DERProgram []DERProgram `xml:"DERProgram,omitempty"`
}

// DER represents device-level DER capability.
type DER struct {
	XMLName xml.Name `xml:"urn:ieee:std:2030.5:ns DER"`
	SubscribableResource
	AssociatedDERProgramListLink *ListLink `xml:"AssociatedDERProgramListLink,omitempty"`
	DERAvailabilityLink          *Link     `xml:"DERAvailabilityLink,omitempty"`
	DERCapabilityLink            *Link     `xml:"DERCapabilityLink,omitempty"`
	DERSettingsLink              *Link     `xml:"DERSettingsLink,omitempty"`
	DERStatusLink                *Link     `xml:"DERStatusLink,omitempty"`
}

// Copy returns an independent copy.
func (d DER) Copy() DER {
	c := d
	if d.AssociatedDERProgramListLink != nil {
		l := *d.AssociatedDERProgramListLink
		c.AssociatedDERProgramListLink = &l
	}
	if d.DERAvailabilityLink != nil {
		l := *d.DERAvailabilityLink
		c.DERAvailabilityLink = &l
	}
	if d.DERCapabilityLink != nil {
		l := *d.DERCapabilityLink
		c.DERCapabilityLink = &l
	}
	if d.DERSettingsLink != nil {
		l := *d.DERSettingsLink
		c.DERSettingsLink = &l
	}
	if d.DERStatusLink != nil {
		l := *d.DERStatusLink
		c.DERStatusLink = &l
	}
	return c
}

// DERList is a list of DER resources.
type DERList struct {
	XMLName xml.Name `xml:"urn:ieee:std:2030.5:ns DERList"`
	ListResource
	DER []DER `xml:"DER,omitempty"`
}

// DERCapability describes device-level DER capabilities.
//
// Field order matches the sep.xsd DERCapability sequence (subset present
// here): modesSupported, rtgMaxA, rtgMaxChargeRateW, rtgMaxDischargeRateW,
// rtgMaxVar, rtgMaxW, type. rtgMaxW is required (minOccurs=1) and sorts
// near the end of the full XSD sequence, not immediately after
// modesSupported.
type DERCapability struct {
	XMLName xml.Name `xml:"urn:ieee:std:2030.5:ns DERCapability"`
	Resource
	ModesSupported       *DERControlType `xml:"modesSupported,omitempty"`
	RTGMaxA              *int32          `xml:"rtgMaxA,omitempty"`
	RTGMaxChargeRateW    *ActivePower    `xml:"rtgMaxChargeRateW,omitempty"`
	RTGMaxDischargeRateW *ActivePower    `xml:"rtgMaxDischargeRateW,omitempty"`
	RTGMaxVar            *ReactivePower  `xml:"rtgMaxVar,omitempty"`
	RTGMaxW              *ActivePower    `xml:"rtgMaxW,omitempty"`
	Type                 *uint8          `xml:"type,omitempty"`
}

// Copy returns an independent copy.
func (d DERCapability) Copy() DERCapability {
	c := d
	if d.ModesSupported != nil {
		v := *d.ModesSupported
		c.ModesSupported = &v
	}
	if d.RTGMaxW != nil {
		v := *d.RTGMaxW
		c.RTGMaxW = &v
	}
	if d.RTGMaxA != nil {
		v := *d.RTGMaxA
		c.RTGMaxA = &v
	}
	if d.RTGMaxVar != nil {
		v := *d.RTGMaxVar
		c.RTGMaxVar = &v
	}
	if d.RTGMaxChargeRateW != nil {
		v := *d.RTGMaxChargeRateW
		c.RTGMaxChargeRateW = &v
	}
	if d.RTGMaxDischargeRateW != nil {
		v := *d.RTGMaxDischargeRateW
		c.RTGMaxDischargeRateW = &v
	}
	if d.Type != nil {
		v := *d.Type
		c.Type = &v
	}
	return c
}

// DERSettings describes current device DER settings.
//
// Field order matches the sep.xsd DERSettings sequence (subset present
// here): modesEnabled, setMaxChargeRateW, setMaxDischargeRateW, setMaxVar,
// setMaxW, updatedTime (last).
type DERSettings struct {
	XMLName xml.Name `xml:"urn:ieee:std:2030.5:ns DERSettings"`
	SubscribableResource
	ModesEnabled         *DERControlType `xml:"modesEnabled,omitempty"`
	SetMaxChargeRateW    *ActivePower    `xml:"setMaxChargeRateW,omitempty"`
	SetMaxDischargeRateW *ActivePower    `xml:"setMaxDischargeRateW,omitempty"`
	SetMaxVar            *ReactivePower  `xml:"setMaxVar,omitempty"`
	SetMaxW              *ActivePower    `xml:"setMaxW,omitempty"`
	UpdatedTime          int64           `xml:"updatedTime,omitempty"`
}

// Copy returns an independent copy.
func (d DERSettings) Copy() DERSettings {
	c := d
	if d.ModesEnabled != nil {
		v := *d.ModesEnabled
		c.ModesEnabled = &v
	}
	if d.SetMaxW != nil {
		v := *d.SetMaxW
		c.SetMaxW = &v
	}
	if d.SetMaxVar != nil {
		v := *d.SetMaxVar
		c.SetMaxVar = &v
	}
	if d.SetMaxChargeRateW != nil {
		v := *d.SetMaxChargeRateW
		c.SetMaxChargeRateW = &v
	}
	if d.SetMaxDischargeRateW != nil {
		v := *d.SetMaxDischargeRateW
		c.SetMaxDischargeRateW = &v
	}
	return c
}

// ConnectStatusType reports connection state. Its value element is
// HexBinary8 in sep.xsd (sep.xsd:4471), unlike the sibling
// InverterStatusType and OperationalModeStatusType value elements, which
// are UInt8 (sep.xsd:4500, sep.xsd:4559) and stay decimal.
type ConnectStatusType struct {
	DateTime int64      `xml:"dateTime"`
	Value    HexBinary8 `xml:"value"`
}

// InverterStatusType reports inverter state.
type InverterStatusType struct {
	DateTime int64 `xml:"dateTime"`
	Value    uint8 `xml:"value"`
}

// OperationalModeStatusType reports operational mode.
type OperationalModeStatusType struct {
	DateTime int64 `xml:"dateTime"`
	Value    uint8 `xml:"value"`
}

// StateOfChargeStatusType reports state of charge. Its value element is
// PerCent in sep.xsd (sep.xsd:4566-4582, sep.xsd:5945-5952), a UInt16 in
// hundredths of a percent spanning 0 to 10000, where 10000 is 100%. That
// is why Value is uint16 rather than the uint8 the sibling
// InverterStatusType and OperationalModeStatusType value elements use.
type StateOfChargeStatusType struct {
	DateTime int64  `xml:"dateTime"`
	Value    uint16 `xml:"value"`
}

// StorageModeStatusType reports storage mode: 0 charging, 1 discharging,
// 2 holding, all other values reserved (sep.xsd:4583-4602). Its value
// element is UInt8.
type StorageModeStatusType struct {
	DateTime int64 `xml:"dateTime"`
	Value    uint8 `xml:"value"`
}

// DERStatus reports current DER operational status.
//
// Field order matches the sep.xsd DERStatus sequence (subset present
// here): alarmStatus, genConnectStatus, inverterStatus,
// operationalModeStatus, readingTime, stateOfChargeStatus,
// storageModeStatus.
type DERStatus struct {
	XMLName xml.Name `xml:"urn:ieee:std:2030.5:ns DERStatus"`
	SubscribableResource
	AlarmStatus           *HexBinary32               `xml:"alarmStatus,omitempty"`
	GenConnectStatus      *ConnectStatusType         `xml:"genConnectStatus,omitempty"`
	InverterStatus        *InverterStatusType        `xml:"inverterStatus,omitempty"`
	OperationalModeStatus *OperationalModeStatusType `xml:"operationalModeStatus,omitempty"`
	ReadingTime           int64                      `xml:"readingTime,omitempty"`
	StateOfChargeStatus   *StateOfChargeStatusType   `xml:"stateOfChargeStatus,omitempty"`
	StorageModeStatus     *StorageModeStatusType     `xml:"storageModeStatus,omitempty"`
}

// Copy returns an independent copy.
func (d DERStatus) Copy() DERStatus {
	c := d
	if d.GenConnectStatus != nil {
		v := *d.GenConnectStatus
		c.GenConnectStatus = &v
	}
	if d.InverterStatus != nil {
		v := *d.InverterStatus
		c.InverterStatus = &v
	}
	if d.OperationalModeStatus != nil {
		v := *d.OperationalModeStatus
		c.OperationalModeStatus = &v
	}
	if d.AlarmStatus != nil {
		v := *d.AlarmStatus
		c.AlarmStatus = &v
	}
	if d.StateOfChargeStatus != nil {
		v := *d.StateOfChargeStatus
		c.StateOfChargeStatus = &v
	}
	if d.StorageModeStatus != nil {
		v := *d.StorageModeStatus
		c.StorageModeStatus = &v
	}
	return c
}

// DERAvailability reports device availability.
type DERAvailability struct {
	XMLName xml.Name `xml:"urn:ieee:std:2030.5:ns DERAvailability"`
	SubscribableResource
	AvailabilityDuration *uint32        `xml:"availabilityDuration,omitempty"`
	MaxChargeDuration    *uint32        `xml:"maxChargeDuration,omitempty"`
	ReadingTime          int64          `xml:"readingTime,omitempty"`
	StatVarAvail         *ReactivePower `xml:"statVarAvail,omitempty"`
	StatWAvail           *ActivePower   `xml:"statWAvail,omitempty"`
}

// Copy returns an independent copy.
func (d DERAvailability) Copy() DERAvailability {
	c := d
	if d.AvailabilityDuration != nil {
		v := *d.AvailabilityDuration
		c.AvailabilityDuration = &v
	}
	if d.MaxChargeDuration != nil {
		v := *d.MaxChargeDuration
		c.MaxChargeDuration = &v
	}
	if d.StatVarAvail != nil {
		v := *d.StatVarAvail
		c.StatVarAvail = &v
	}
	if d.StatWAvail != nil {
		v := *d.StatWAvail
		c.StatWAvail = &v
	}
	return c
}

// CurveData is a single x,y point on a DER curve.
type CurveData struct {
	XValue int32 `xml:"xvalue"`
	YValue int32 `xml:"yvalue"`
}

// DERCurve defines volt-var, freq-watt, etc. curves.
type DERCurve struct {
	XMLName xml.Name `xml:"urn:ieee:std:2030.5:ns DERCurve"`
	Resource
	MRID        string      `xml:"mRID,omitempty"`
	Description string      `xml:"description,omitempty"`
	CurveType   uint8       `xml:"curveType"`
	CurveData   []CurveData `xml:"CurveData,omitempty"`
	RampDecTms  *uint16     `xml:"rampDecTms,omitempty"`
	RampIncTms  *uint16     `xml:"rampIncTms,omitempty"`
	RampPT1Tms  *uint16     `xml:"rampPT1Tms,omitempty"`
}

// Copy returns an independent copy.
func (d DERCurve) Copy() DERCurve {
	c := d
	if d.CurveData != nil {
		c.CurveData = make([]CurveData, len(d.CurveData))
		copy(c.CurveData, d.CurveData)
	}
	if d.RampDecTms != nil {
		v := *d.RampDecTms
		c.RampDecTms = &v
	}
	if d.RampIncTms != nil {
		v := *d.RampIncTms
		c.RampIncTms = &v
	}
	if d.RampPT1Tms != nil {
		v := *d.RampPT1Tms
		c.RampPT1Tms = &v
	}
	return c
}

// DERCurveList is a list of DERCurve resources.
type DERCurveList struct {
	XMLName xml.Name `xml:"urn:ieee:std:2030.5:ns DERCurveList"`
	ListResource
	DERCurve []DERCurve `xml:"DERCurve,omitempty"`
}

// DER curve type constants.
const (
	CurveTypeOpModVoltVar  uint8 = 0
	CurveTypeOpModFreqWatt uint8 = 1
	CurveTypeOpModWattPF   uint8 = 2
	CurveTypeOpModVoltWatt uint8 = 3
)
