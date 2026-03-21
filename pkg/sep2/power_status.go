package sep2

import "encoding/xml"

// PowerStatus reports device power source and battery status.
// Spec reference: section 9.4
// Element order matches 2023 XSD.
type PowerStatus struct {
	XMLName                  xml.Name `xml:"urn:ieee:std:2030.5:ns PowerStatus"`
	Resource
	PollRate                 uint32   `xml:"pollRate,attr,omitempty"`

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
	ChargingPowerNow      ActivePower      `xml:"chargingPowerNow"`
	EnergyRequestNow      RealEnergy       `xml:"energyRequestNow"`
	MaxForwardPower        ActivePower      `xml:"maxForwardPower"`
	MinimumChargingDuration uint32          `xml:"minimumChargingDuration"`
	TargetStateOfCharge    uint16           `xml:"targetStateOfCharge"` // percent
	TimeChargeIsNeeded     int64            `xml:"timeChargeIsNeeded"`
	TimeChargingStatusPEV  int64            `xml:"timeChargingStatusPEV"`
}

// RealEnergy represents energy in watt-hours.
type RealEnergy struct {
	Multiplier int8  `xml:"multiplier"`
	Value      int64 `xml:"value"` // watt-hours
}

// SignedRealEnergy represents signed energy (2023 addition for FlowReservation).
type SignedRealEnergy struct {
	Multiplier int8  `xml:"multiplier"`
	Value      int64 `xml:"value"` // watt-hours, positive = delivered, negative = received
}

// Copy returns an independent copy.
func (p PowerStatus) Copy() PowerStatus {
	c := p
	if p.BatteryStatus != nil { v := *p.BatteryStatus; c.BatteryStatus = &v }
	if p.EstimatedChargeRemaining != nil { v := *p.EstimatedChargeRemaining; c.EstimatedChargeRemaining = &v }
	if p.EstimatedTimeRemaining != nil { v := *p.EstimatedTimeRemaining; c.EstimatedTimeRemaining = &v }
	if p.PEVInfo != nil { pev := *p.PEVInfo; c.PEVInfo = &pev }
	if p.SessionTimeOnBattery != nil { v := *p.SessionTimeOnBattery; c.SessionTimeOnBattery = &v }
	if p.TotalTimeOnBattery != nil { v := *p.TotalTimeOnBattery; c.TotalTimeOnBattery = &v }
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
