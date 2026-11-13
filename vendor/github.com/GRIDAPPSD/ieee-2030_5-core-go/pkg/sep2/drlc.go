package sep2

import "encoding/xml"

// DemandResponseProgram is a demand response program.
// Spec reference: section 10.3 (stub: handlers deferred)
//
// Field order is the sep.xsd sequence: IdentifiedObject, then the two links
// and thresholds, then primacy.
type DemandResponseProgram struct {
	XMLName xml.Name `xml:"urn:ieee:std:2030.5:ns DemandResponseProgram"`
	Resource
	IdentifiedObject
	ActiveEndDeviceControlListLink           *ListLink    `xml:"ActiveEndDeviceControlListLink,omitempty"`
	AvailabilityUpdatePercentChangeThreshold *PerCent     `xml:"availabilityUpdatePercentChangeThreshold,omitempty"`
	AvailabilityUpdatePowerChangeThreshold   *ActivePower `xml:"availabilityUpdatePowerChangeThreshold,omitempty"`
	EndDeviceControlListLink                 *ListLink    `xml:"EndDeviceControlListLink,omitempty"`
	Primacy                                  uint8        `xml:"primacy"`
}

// Copy returns an independent copy.
func (d DemandResponseProgram) Copy() DemandResponseProgram {
	c := d
	c.IdentifiedObject = d.IdentifiedObject.Copy()
	c.ActiveEndDeviceControlListLink = clonePtr(d.ActiveEndDeviceControlListLink)
	c.AvailabilityUpdatePercentChangeThreshold = clonePtr(d.AvailabilityUpdatePercentChangeThreshold)
	c.AvailabilityUpdatePowerChangeThreshold = clonePtr(d.AvailabilityUpdatePowerChangeThreshold)
	c.EndDeviceControlListLink = clonePtr(d.EndDeviceControlListLink)
	return c
}

// DemandResponseProgramList is a list of DemandResponseProgram resources.
type DemandResponseProgramList struct {
	XMLName xml.Name `xml:"urn:ieee:std:2030.5:ns DemandResponseProgramList"`
	ListResource
	DemandResponseProgram []DemandResponseProgram `xml:"DemandResponseProgram,omitempty"`
}

// EndDeviceControl is a demand response control event. Fields follow the
// sep.xsd sequence. The two booleans are required and always encoded; a body
// that omits one decodes to false, but a present one must be a valid
// xs:boolean.
//
// DeviceCategory is required by the schema but stays a pointer: a value type
// would encode an invented 00000000 for a control that never set it, so the
// consumer checks presence instead.
type EndDeviceControl struct {
	XMLName xml.Name `xml:"urn:ieee:std:2030.5:ns EndDeviceControl"`
	RandomizableEvent
	ApplianceLoadReduction *ApplianceLoadReduction `xml:"ApplianceLoadReduction,omitempty"`
	DeviceCategory         *DeviceCategoryType     `xml:"deviceCategory,omitempty"`
	DRProgramMandatory     Boolean                 `xml:"drProgramMandatory"`
	DutyCycle              *DutyCycle              `xml:"DutyCycle,omitempty"`
	LoadShiftForward       Boolean                 `xml:"loadShiftForward"`
	Offset                 *Offset                 `xml:"Offset,omitempty"`
	OverrideDuration       *OverrideDuration       `xml:"overrideDuration,omitempty"`
	SetPoint               *SetPoint               `xml:"SetPoint,omitempty"`
	TargetReduction        *TargetReduction        `xml:"TargetReduction,omitempty"`
}

// drlcValues is the DRLC value fields as DrResponseDRLC, whose Copy is the
// one clone sequence for them. TargetReduction maps to AppliedTargetReduction,
// the same type under the name DrResponse uses.
func (e EndDeviceControl) drlcValues() DrResponseDRLC {
	return DrResponseDRLC{
		ApplianceLoadReduction: e.ApplianceLoadReduction,
		AppliedTargetReduction: e.TargetReduction,
		DutyCycle:              e.DutyCycle,
		Offset:                 e.Offset,
		OverrideDuration:       e.OverrideDuration,
		SetPoint:               e.SetPoint,
	}
}

// Copy returns an independent copy.
func (e EndDeviceControl) Copy() EndDeviceControl {
	c := e
	c.Event = e.Event.Copy()
	c.DeviceCategory = clonePtr(e.DeviceCategory)
	c.RandomizeDuration = clonePtr(e.RandomizeDuration)
	c.RandomizeStart = clonePtr(e.RandomizeStart)
	v := e.drlcValues().Copy()
	c.ApplianceLoadReduction = v.ApplianceLoadReduction
	c.TargetReduction = v.AppliedTargetReduction
	c.DutyCycle = v.DutyCycle
	c.Offset = v.Offset
	c.OverrideDuration = v.OverrideDuration
	c.SetPoint = v.SetPoint
	return c
}

// EndDeviceControlList is a list of EndDeviceControl resources.
type EndDeviceControlList struct {
	XMLName xml.Name `xml:"urn:ieee:std:2030.5:ns EndDeviceControlList"`
	ListResource
	EndDeviceControl []EndDeviceControl `xml:"EndDeviceControl,omitempty"`
}
