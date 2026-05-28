package sep2

import "encoding/xml"

// DeviceStatus reports a device's operational status.
// Spec reference: section 9.3 (DeviceStatus sub-resource)
// Element order matches 2023 XSD.
type DeviceStatus struct {
	XMLName xml.Name `xml:"urn:ieee:std:2030.5:ns DeviceStatus"`
	Resource
	PollRate uint32 `xml:"pollRate,attr,omitempty"`

	// XSD element order (2023):
	ChangedTime int64         `xml:"changedTime"`
	OnCount     *uint16       `xml:"onCount,omitempty"`
	OpState     *uint8        `xml:"opState,omitempty"` // 0=not applicable, 1=not operating, 2=operating, 3=starting up, 4=shutting down, 5=at disconnect level
	OpTime      *uint32       `xml:"opTime,omitempty"`  // seconds of operation
	Temperature []Temperature `xml:"Temperature,omitempty"`
	TimeLink    *Link         `xml:"TimeLink,omitempty"`
}

// Temperature is a temperature measurement.
type Temperature struct {
	Multiplier int8  `xml:"multiplier"`
	Subject    uint8 `xml:"subject"` // 0=enclosure, 1=transformer, 2=heat sink
	Value      int32 `xml:"value"`
}

// Copy returns an independent copy.
func (d DeviceStatus) Copy() DeviceStatus {
	c := d
	if d.OnCount != nil {
		v := *d.OnCount
		c.OnCount = &v
	}
	if d.OpState != nil {
		v := *d.OpState
		c.OpState = &v
	}
	if d.OpTime != nil {
		v := *d.OpTime
		c.OpTime = &v
	}
	if d.TimeLink != nil {
		l := *d.TimeLink
		c.TimeLink = &l
	}
	if d.Temperature != nil {
		c.Temperature = make([]Temperature, len(d.Temperature))
		copy(c.Temperature, d.Temperature)
	}
	return c
}

// OpState constants per spec.
const (
	OpStateNotApplicable uint8 = 0
	OpStateNotOperating  uint8 = 1
	OpStateOperating     uint8 = 2
	OpStateStartingUp    uint8 = 3
	OpStateShuttingDown  uint8 = 4
	OpStateAtDisconnect  uint8 = 5
)
