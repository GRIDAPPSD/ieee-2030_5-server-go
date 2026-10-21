package sep2

import "encoding/xml"

// DERControlBase contains all DER operating mode parameters.
// Spec reference: IEEE 2030.5 section 10.10.
type DERControlBase struct {
	OpModConnect        *bool             `xml:"opModConnect,omitempty"`
	OpModEnergize       *bool             `xml:"opModEnergize,omitempty"`
	OpModFixedPFAbsorbW *FixedPowerFactor `xml:"opModFixedPFAbsorbW,omitempty"`
	OpModFixedPFInjectW *FixedPowerFactor `xml:"opModFixedPFInjectW,omitempty"`
	OpModFixedVar       *FixedVar         `xml:"opModFixedVar,omitempty"`
	// OpModFixedW is a signed percentage of %setMaxChargeRateW when
	// negative, or %setMaxW / %setMaxDischargeRateW when positive, in
	// hundredths of a percent. IEEE 2030.5-2018 Annex B.2.22, opModFixedW
	// attribute (SignedPerCent).
	OpModFixedW                 *SignedPerCent `xml:"opModFixedW,omitempty"`
	OpModFreqDroop              *FreqDroopType `xml:"opModFreqDroop,omitempty"`
	OpModFreqWatt               *DERCurveLink  `xml:"opModFreqWatt,omitempty"`
	OpModHFRTMayTrip            *DERCurveLink  `xml:"opModHFRTMayTrip,omitempty"`
	OpModHFRTMustTrip           *DERCurveLink  `xml:"opModHFRTMustTrip,omitempty"`
	OpModHVRTMayTrip            *DERCurveLink  `xml:"opModHVRTMayTrip,omitempty"`
	OpModHVRTMomentaryCessation *DERCurveLink  `xml:"opModHVRTMomentaryCessation,omitempty"`
	OpModHVRTMustTrip           *DERCurveLink  `xml:"opModHVRTMustTrip,omitempty"`
	OpModLFRTMayTrip            *DERCurveLink  `xml:"opModLFRTMayTrip,omitempty"`
	OpModLFRTMustTrip           *DERCurveLink  `xml:"opModLFRTMustTrip,omitempty"`
	OpModLVRTMayTrip            *DERCurveLink  `xml:"opModLVRTMayTrip,omitempty"`
	OpModLVRTMomentaryCessation *DERCurveLink  `xml:"opModLVRTMomentaryCessation,omitempty"`
	OpModLVRTMustTrip           *DERCurveLink  `xml:"opModLVRTMustTrip,omitempty"`
	// OpModMaxLimW is a percentage of %setMaxW, in hundredths of a
	// percent, capping active power generation at the coupling point.
	// IEEE 2030.5-2018 Annex B.2.22, opModMaxLimW attribute (PerCent).
	OpModMaxLimW   *PerCent       `xml:"opModMaxLimW,omitempty"`
	OpModTargetVar *ReactivePower `xml:"opModTargetVar,omitempty"`
	OpModTargetW   *ActivePower   `xml:"opModTargetW,omitempty"`
	OpModVoltVar   *DERCurveLink  `xml:"opModVoltVar,omitempty"`
	OpModVoltWatt  *DERCurveLink  `xml:"opModVoltWatt,omitempty"`
	OpModWattPF    *DERCurveLink  `xml:"opModWattPF,omitempty"`
	OpModWattVar   *DERCurveLink  `xml:"opModWattVar,omitempty"`
	RampTms        *uint16        `xml:"rampTms,omitempty"`
}

// Copy returns an independent copy.
func (d DERControlBase) Copy() DERControlBase {
	c := d
	c.OpModConnect = clonePtr(d.OpModConnect)
	c.OpModEnergize = clonePtr(d.OpModEnergize)
	c.OpModFixedPFAbsorbW = clonePtr(d.OpModFixedPFAbsorbW)
	c.OpModFixedPFInjectW = clonePtr(d.OpModFixedPFInjectW)
	c.OpModFixedW = clonePtr(d.OpModFixedW)
	c.OpModFixedVar = clonePtr(d.OpModFixedVar)
	c.OpModFreqDroop = clonePtr(d.OpModFreqDroop)
	c.OpModFreqWatt = clonePtr(d.OpModFreqWatt)
	c.OpModHFRTMayTrip = clonePtr(d.OpModHFRTMayTrip)
	c.OpModHFRTMustTrip = clonePtr(d.OpModHFRTMustTrip)
	c.OpModHVRTMayTrip = clonePtr(d.OpModHVRTMayTrip)
	c.OpModHVRTMomentaryCessation = clonePtr(d.OpModHVRTMomentaryCessation)
	c.OpModHVRTMustTrip = clonePtr(d.OpModHVRTMustTrip)
	c.OpModLFRTMayTrip = clonePtr(d.OpModLFRTMayTrip)
	c.OpModLFRTMustTrip = clonePtr(d.OpModLFRTMustTrip)
	c.OpModLVRTMayTrip = clonePtr(d.OpModLVRTMayTrip)
	c.OpModLVRTMomentaryCessation = clonePtr(d.OpModLVRTMomentaryCessation)
	c.OpModLVRTMustTrip = clonePtr(d.OpModLVRTMustTrip)
	c.OpModMaxLimW = clonePtr(d.OpModMaxLimW)
	c.OpModTargetW = clonePtr(d.OpModTargetW)
	c.OpModTargetVar = clonePtr(d.OpModTargetVar)
	c.OpModVoltVar = clonePtr(d.OpModVoltVar)
	c.OpModVoltWatt = clonePtr(d.OpModVoltWatt)
	c.OpModWattPF = clonePtr(d.OpModWattPF)
	c.OpModWattVar = clonePtr(d.OpModWattVar)
	c.RampTms = clonePtr(d.RampTms)
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
	c.Event = d.Event.Copy()
	c.RandomizeDuration = clonePtr(d.RandomizeDuration)
	c.RandomizeStart = clonePtr(d.RandomizeStart)
	c.DERControlBase = cloneCopyPtr(d.DERControlBase)
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
	Version        *uint16         `xml:"version,omitempty"`
	DERControlBase *DERControlBase `xml:"DERControlBase,omitempty"`
	SetGradW       *uint16         `xml:"setGradW,omitempty"`
	SetSoftGradW   *uint16         `xml:"setSoftGradW,omitempty"`
}

// Copy returns an independent copy.
func (d DefaultDERControl) Copy() DefaultDERControl {
	c := d
	c.SubscribableResource = d.SubscribableResource.Copy()
	c.Version = clonePtr(d.Version)
	c.DERControlBase = cloneCopyPtr(d.DERControlBase)
	c.SetGradW = clonePtr(d.SetGradW)
	c.SetSoftGradW = clonePtr(d.SetSoftGradW)
	return c
}

// DERProgram contains controls and curves for DER devices.
//
// Field order matches the SubscribableIdentifiedObject + DERProgram
// combined xsd:sequence: mRID, description, version (base) THEN
// ActiveDERControlListLink, DefaultDERControlLink, DERControlListLink,
// DERCurveListLink, primacy (DERProgram's own sequence, primacy LAST).
type DERProgram struct {
	XMLName xml.Name `xml:"urn:ieee:std:2030.5:ns DERProgram"`
	SubscribableResource
	MRID        string  `xml:"mRID,omitempty"`
	Description string  `xml:"description,omitempty"`
	Version     *uint16 `xml:"version,omitempty"`

	ActiveDERControlListLink *ListLink `xml:"ActiveDERControlListLink,omitempty"`
	DefaultDERControlLink    *Link     `xml:"DefaultDERControlLink,omitempty"`
	DERControlListLink       *ListLink `xml:"DERControlListLink,omitempty"`
	DERCurveListLink         *ListLink `xml:"DERCurveListLink,omitempty"`
	Primacy                  uint8     `xml:"primacy"`
}

// Copy returns an independent copy.
func (p DERProgram) Copy() DERProgram {
	c := p
	c.SubscribableResource = p.SubscribableResource.Copy()
	c.Version = clonePtr(p.Version)
	c.ActiveDERControlListLink = clonePtr(p.ActiveDERControlListLink)
	c.DefaultDERControlLink = clonePtr(p.DefaultDERControlLink)
	c.DERControlListLink = clonePtr(p.DERControlListLink)
	c.DERCurveListLink = clonePtr(p.DERCurveListLink)
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
	c.SubscribableResource = d.SubscribableResource.Copy()
	c.AssociatedDERProgramListLink = clonePtr(d.AssociatedDERProgramListLink)
	c.DERAvailabilityLink = clonePtr(d.DERAvailabilityLink)
	c.DERCapabilityLink = clonePtr(d.DERCapabilityLink)
	c.DERSettingsLink = clonePtr(d.DERSettingsLink)
	c.DERStatusLink = clonePtr(d.DERStatusLink)
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
// rtgMaxV, rtgMaxVA, rtgMaxVar, rtgMaxW, type. rtgMaxW is required
// (minOccurs=1) and sorts near the end of the full XSD sequence, not
// immediately after modesSupported.
type DERCapability struct {
	XMLName xml.Name `xml:"urn:ieee:std:2030.5:ns DERCapability"`
	Resource
	ModesSupported       *DERControlType `xml:"modesSupported,omitempty"`
	RTGMaxA              *int32          `xml:"rtgMaxA,omitempty"`
	RTGMaxChargeRateW    *ActivePower    `xml:"rtgMaxChargeRateW,omitempty"`
	RTGMaxDischargeRateW *ActivePower    `xml:"rtgMaxDischargeRateW,omitempty"`
	RTGMaxV              *VoltageRMS     `xml:"rtgMaxV,omitempty"`
	RTGMaxVA             *ApparentPower  `xml:"rtgMaxVA,omitempty"`
	RTGMaxVar            *ReactivePower  `xml:"rtgMaxVar,omitempty"`
	RTGMaxW              *ActivePower    `xml:"rtgMaxW,omitempty"`
	Type                 *uint8          `xml:"type,omitempty"`
}

// Copy returns an independent copy.
func (d DERCapability) Copy() DERCapability {
	c := d
	c.ModesSupported = clonePtr(d.ModesSupported)
	c.RTGMaxW = clonePtr(d.RTGMaxW)
	c.RTGMaxA = clonePtr(d.RTGMaxA)
	c.RTGMaxVar = clonePtr(d.RTGMaxVar)
	c.RTGMaxChargeRateW = clonePtr(d.RTGMaxChargeRateW)
	c.RTGMaxDischargeRateW = clonePtr(d.RTGMaxDischargeRateW)
	c.RTGMaxV = clonePtr(d.RTGMaxV)
	c.RTGMaxVA = clonePtr(d.RTGMaxVA)
	c.Type = clonePtr(d.Type)
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
	c.SubscribableResource = d.SubscribableResource.Copy()
	c.ModesEnabled = clonePtr(d.ModesEnabled)
	c.SetMaxW = clonePtr(d.SetMaxW)
	c.SetMaxVar = clonePtr(d.SetMaxVar)
	c.SetMaxChargeRateW = clonePtr(d.SetMaxChargeRateW)
	c.SetMaxDischargeRateW = clonePtr(d.SetMaxDischargeRateW)
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

// ConnectStatusType2 is the IEEE 2030.5-2023 connect status (sep.xsd
// ConnectStatusType2). Value is a HexBinary8 bitmap: bit 0 connected
// (implies galvanic isolation when clear), bit 1 energized; other bits are
// reserved.
type ConnectStatusType2 struct {
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
// here): alarmStatus, connectStatus, genConnectStatus, inverterStatus,
// operationalModeStatus, readingTime, stateOfChargeStatus,
// storageModeStatus, storConnectStatus.
//
// ConnectStatus exists only in 2030.5-2023, which deprecates
// GenConnectStatus; a 2018 document carries GenConnectStatus or
// StorConnectStatus. Each field is emitted only when set, so the edition on
// the wire follows what the caller populates.
type DERStatus struct {
	XMLName xml.Name `xml:"urn:ieee:std:2030.5:ns DERStatus"`
	SubscribableResource
	AlarmStatus           *HexBinary32               `xml:"alarmStatus,omitempty"`
	ConnectStatus         *ConnectStatusType2        `xml:"connectStatus,omitempty"`
	GenConnectStatus      *ConnectStatusType         `xml:"genConnectStatus,omitempty"`
	InverterStatus        *InverterStatusType        `xml:"inverterStatus,omitempty"`
	OperationalModeStatus *OperationalModeStatusType `xml:"operationalModeStatus,omitempty"`
	ReadingTime           int64                      `xml:"readingTime,omitempty"`
	StateOfChargeStatus   *StateOfChargeStatusType   `xml:"stateOfChargeStatus,omitempty"`
	StorageModeStatus     *StorageModeStatusType     `xml:"storageModeStatus,omitempty"`
	StorConnectStatus     *ConnectStatusType         `xml:"storConnectStatus,omitempty"`
}

// Copy returns an independent copy.
func (d DERStatus) Copy() DERStatus {
	c := d
	c.SubscribableResource = d.SubscribableResource.Copy()
	c.ConnectStatus = clonePtr(d.ConnectStatus)
	c.StorConnectStatus = clonePtr(d.StorConnectStatus)
	c.GenConnectStatus = clonePtr(d.GenConnectStatus)
	c.InverterStatus = clonePtr(d.InverterStatus)
	c.OperationalModeStatus = clonePtr(d.OperationalModeStatus)
	c.AlarmStatus = clonePtr(d.AlarmStatus)
	c.StateOfChargeStatus = clonePtr(d.StateOfChargeStatus)
	c.StorageModeStatus = clonePtr(d.StorageModeStatus)
	return c
}

// DERAvailability reports device availability. Field order follows
// sep.xsd's DERAvailability sequence, confirmed against the alphabetical
// attribute listing in IEEE 2030.5-2023 Annex B.23. readingTime is
// minOccurs=1 and carries no omitempty, unlike the other elements here.
//
// The 2023 edition adds two more elements here, statVarAbsorbAvail and
// statWAbsorbAvail, but this module's schema gate (schema/PROVENANCE.md)
// pins the 2018 edition, Model Build 20180301, which does not declare them;
// CI's schema-gated test fails with unknown-element for both. Adding that
// pair is therefore a separate change gated on an edition decision, not on
// this one.
type DERAvailability struct {
	XMLName xml.Name `xml:"urn:ieee:std:2030.5:ns DERAvailability"`
	SubscribableResource
	AvailabilityDuration *uint32        `xml:"availabilityDuration,omitempty"`
	MaxChargeDuration    *uint32        `xml:"maxChargeDuration,omitempty"`
	ReadingTime          int64          `xml:"readingTime"`
	ReserveChargePercent *PerCent       `xml:"reserveChargePercent,omitempty"`
	ReservePercent       *PerCent       `xml:"reservePercent,omitempty"`
	StatVarAvail         *ReactivePower `xml:"statVarAvail,omitempty"`
	StatWAvail           *ActivePower   `xml:"statWAvail,omitempty"`
}

// Copy returns an independent copy.
func (d DERAvailability) Copy() DERAvailability {
	c := d
	c.SubscribableResource = d.SubscribableResource.Copy()
	c.AvailabilityDuration = clonePtr(d.AvailabilityDuration)
	c.MaxChargeDuration = clonePtr(d.MaxChargeDuration)
	c.ReserveChargePercent = clonePtr(d.ReserveChargePercent)
	c.ReservePercent = clonePtr(d.ReservePercent)
	c.StatVarAvail = clonePtr(d.StatVarAvail)
	c.StatWAvail = clonePtr(d.StatWAvail)
	return c
}

// CurveData is a single x,y point on a DER curve.
type CurveData struct {
	XValue int32 `xml:"xvalue"`
	YValue int32 `xml:"yvalue"`
}

// DERCurve defines volt-var, freq-watt, etc. curves.
//
// Fields follow the sep.xsd sequence, which encoding/xml emits in declaration
// order. Required elements carry no omitempty, so an unset one serializes as
// an empty element or 0 rather than vanishing; CurveData still emits nothing
// when the curve has no points. yRefType is a DERUnitRefType (UInt8).
type DERCurve struct {
	XMLName xml.Name `xml:"urn:ieee:std:2030.5:ns DERCurve"`
	Resource
	MRID         string      `xml:"mRID"`
	Description  string      `xml:"description,omitempty"`
	CreationTime int64       `xml:"creationTime"`
	CurveData    []CurveData `xml:"CurveData"`
	CurveType    uint8       `xml:"curveType"`
	RampDecTms   *uint16     `xml:"rampDecTms,omitempty"`
	RampIncTms   *uint16     `xml:"rampIncTms,omitempty"`
	RampPT1Tms   *uint16     `xml:"rampPT1Tms,omitempty"`
	XMultiplier  int8        `xml:"xMultiplier"`
	YMultiplier  int8        `xml:"yMultiplier"`
	YRefType     uint8       `xml:"yRefType"`
}

// Copy returns an independent copy.
func (d DERCurve) Copy() DERCurve {
	c := d
	c.CurveData = cloneSlice(d.CurveData)
	c.RampDecTms = clonePtr(d.RampDecTms)
	c.RampIncTms = clonePtr(d.RampIncTms)
	c.RampPT1Tms = clonePtr(d.RampPT1Tms)
	return c
}

// DERCurveList is a list of DERCurve resources.
type DERCurveList struct {
	XMLName xml.Name `xml:"urn:ieee:std:2030.5:ns DERCurveList"`
	ListResource
	DERCurve []DERCurve `xml:"DERCurve,omitempty"`
}

// DER curve type constants (DERCurveType, UInt8). Values 15-255 are
// reserved: DERCurve.CurveType decodes and re-encodes them unchanged,
// since the type carries no custom decode validation.
const (
	CurveTypeOpModFreqWatt               uint8 = 0
	CurveTypeOpModHFRTMayTrip            uint8 = 1
	CurveTypeOpModHFRTMustTrip           uint8 = 2
	CurveTypeOpModHVRTMayTrip            uint8 = 3
	CurveTypeOpModHVRTMomentaryCessation uint8 = 4
	CurveTypeOpModHVRTMustTrip           uint8 = 5
	CurveTypeOpModLFRTMayTrip            uint8 = 6
	CurveTypeOpModLFRTMustTrip           uint8 = 7
	CurveTypeOpModLVRTMayTrip            uint8 = 8
	CurveTypeOpModLVRTMomentaryCessation uint8 = 9
	CurveTypeOpModLVRTMustTrip           uint8 = 10
	CurveTypeOpModVoltVar                uint8 = 11
	CurveTypeOpModVoltWatt               uint8 = 12
	CurveTypeOpModWattPF                 uint8 = 13
	CurveTypeOpModWattVar                uint8 = 14
)
