package sep2

import "encoding/xml"

// Configuration provides device configuration settings.
// Spec reference: section 9.7
// Element order matches 2023 XSD.
type Configuration struct {
	XMLName xml.Name `xml:"urn:ieee:std:2030.5:ns Configuration"`
	SubscribableResource
	PollRate uint32 `xml:"pollRate,attr,omitempty"`

	CurrentLocale      string              `xml:"currentLocale,omitempty"` // RFC 5646 locale
	PowerConfiguration *PowerConfiguration `xml:"PowerConfiguration,omitempty"`
	// PriceResponseCfgListLink omitted (Pricing function set)
	TimeConfiguration *TimeConfiguration `xml:"TimeConfiguration,omitempty"`
	UserDeviceName    string             `xml:"userDeviceName,omitempty"`
}

// PowerConfiguration holds battery/power settings.
type PowerConfiguration struct {
	BatteryInstallTime *int64  `xml:"batteryInstallTime,omitempty"`
	LowChargeThreshold *uint32 `xml:"lowChargeThreshold,omitempty"`
}

// TimeConfiguration holds timezone/DST settings.
type TimeConfiguration struct {
	DstEndRule   string `xml:"dstEndRule"`
	DstOffset    int32  `xml:"dstOffset"`
	DstStartRule string `xml:"dstStartRule"`
	TzOffset     int32  `xml:"tzOffset"`
}

// Copy returns an independent copy.
func (c Configuration) Copy() Configuration {
	cp := c
	if c.PowerConfiguration != nil {
		p := *c.PowerConfiguration
		cp.PowerConfiguration = &p
	}
	if c.TimeConfiguration != nil {
		t := *c.TimeConfiguration
		cp.TimeConfiguration = &t
	}
	return cp
}
