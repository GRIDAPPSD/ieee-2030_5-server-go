package sep2

import (
	"encoding/xml"
	"fmt"
)

// PowerStatus reports device power source and battery status.
// Spec reference: section 9.4
// Element order matches 2023 XSD.
type PowerStatus struct {
	XMLName xml.Name `xml:"urn:ieee:std:2030.5:ns PowerStatus"`
	Resource
	PollRate uint32 `xml:"pollRate,attr,omitempty"`

	BatteryStatus            *uint8   `xml:"batteryStatus,omitempty"`
	ChangedTime              int64    `xml:"changedTime"`
	CurrentPowerSource       uint8    `xml:"currentPowerSource"`
	EstimatedChargeRemaining *uint16  `xml:"estimatedChargeRemaining,omitempty"` // percent
	EstimatedTimeRemaining   *uint32  `xml:"estimatedTimeRemaining,omitempty"`   // seconds
	PEVInfo                  *PEVInfo `xml:"PEVInfo,omitempty"`
	SessionTimeOnBattery     *uint32  `xml:"sessionTimeOnBattery,omitempty"`
	TotalTimeOnBattery       *uint32  `xml:"totalTimeOnBattery,omitempty"`
}

// PEVInfo provides plug-in electric vehicle information.
type PEVInfo struct {
	ChargingPowerNow        ActivePower `xml:"chargingPowerNow"`
	EnergyRequestNow        RealEnergy  `xml:"energyRequestNow"`
	MaxForwardPower         ActivePower `xml:"maxForwardPower"`
	MinimumChargingDuration uint32      `xml:"minimumChargingDuration"`
	TargetStateOfCharge     uint16      `xml:"targetStateOfCharge"` // percent
	TimeChargeIsNeeded      int64       `xml:"timeChargeIsNeeded"`
	TimeChargingStatusPEV   int64       `xml:"timeChargingStatusPEV"`
}

// UInt48/Int48 bounds per sep.xsd. Go has no native 48-bit integer type,
// so RealEnergy and SignedRealEnergy use int64 as the widest Go integer
// that can hold the range, and MarshalXML guards it at the wire boundary
// so an out-of-range value errors instead of being silently serialized.
//
// The exact ranges: unsigned UInt48 is [0, 2^48-1]; signed Int48 (two's
// complement) is the asymmetric [-2^47, 2^47-1]. maxInt48 and minInt48
// below implement that mathematically correct signed-48-bit bound, which
// is one below sep.xsd's Int48 restriction's literal xs:maxInclusive
// facet (140737488355328, i.e. 2^47); that facet value does not fit in a
// signed 48-bit integer and appears to have an off-by-one error.
const (
	maxUint48 = 1<<48 - 1
	maxInt48  = 1<<47 - 1
	minInt48  = -(1 << 47)
)

// RealEnergy represents energy in watt-hours. Value is XSD UInt48
// (unsigned, 0..2^48-1 per sep.xsd); Go has no native 48-bit type, so the
// underlying field is int64 and MarshalXML enforces the range.
type RealEnergy struct {
	Multiplier int8  `xml:"multiplier"`
	Value      int64 `xml:"value"` // watt-hours; must be in [0, 2^48-1]
}

// MarshalXML validates Value is within XSD UInt48 range before encoding,
// rejecting an out-of-range value rather than serializing an illegal wire
// value.
func (r RealEnergy) MarshalXML(e *xml.Encoder, start xml.StartElement) error {
	if r.Value < 0 || r.Value > maxUint48 {
		return fmt.Errorf("sep2: RealEnergy.Value %d out of UInt48 range [0, %d]", r.Value, maxUint48)
	}
	type shadow RealEnergy
	return e.EncodeElement(shadow(r), start)
}

// SignedRealEnergy represents signed energy (2023 addition for
// FlowReservation). Value is XSD Int48 (signed, [-2^47, 2^47-1] per
// sep.xsd); Go has no native 48-bit type, so the underlying field is
// int64 and MarshalXML enforces the range.
type SignedRealEnergy struct {
	Multiplier int8  `xml:"multiplier"`
	Value      int64 `xml:"value"` // watt-hours, positive = delivered, negative = received; must be in [-2^47, 2^47-1]
}

// MarshalXML validates Value is within XSD Int48 range before encoding,
// rejecting an out-of-range value rather than serializing an illegal wire
// value. Sign is preserved: no normalization or absolute-value coercion.
func (s SignedRealEnergy) MarshalXML(e *xml.Encoder, start xml.StartElement) error {
	if s.Value < minInt48 || s.Value > maxInt48 {
		return fmt.Errorf("sep2: SignedRealEnergy.Value %d out of Int48 range [%d, %d]", s.Value, minInt48, maxInt48)
	}
	type shadow SignedRealEnergy
	return e.EncodeElement(shadow(s), start)
}

// Copy returns an independent copy.
func (p PowerStatus) Copy() PowerStatus {
	c := p
	if p.BatteryStatus != nil {
		v := *p.BatteryStatus
		c.BatteryStatus = &v
	}
	if p.EstimatedChargeRemaining != nil {
		v := *p.EstimatedChargeRemaining
		c.EstimatedChargeRemaining = &v
	}
	if p.EstimatedTimeRemaining != nil {
		v := *p.EstimatedTimeRemaining
		c.EstimatedTimeRemaining = &v
	}
	if p.PEVInfo != nil {
		pev := *p.PEVInfo
		c.PEVInfo = &pev
	}
	if p.SessionTimeOnBattery != nil {
		v := *p.SessionTimeOnBattery
		c.SessionTimeOnBattery = &v
	}
	if p.TotalTimeOnBattery != nil {
		v := *p.TotalTimeOnBattery
		c.TotalTimeOnBattery = &v
	}
	return c
}

// Battery status constants.
const (
	BatteryStatusUnknown       uint8 = 0
	BatteryStatusNormal        uint8 = 1
	BatteryStatusLow           uint8 = 2
	BatteryStatusDepleted      uint8 = 3
	BatteryStatusNotApplicable uint8 = 4
)

// Power source constants.
const (
	PowerSourceMains   uint8 = 0
	PowerSourceBattery uint8 = 1
	PowerSourceLocal   uint8 = 2
)
