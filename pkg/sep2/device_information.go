package sep2

import "encoding/xml"

// DeviceInformation provides metadata about a device.
// Spec reference: section 9.3
// Element order matches 2023 XSD (superset of 2013/2018).
type DeviceInformation struct {
	XMLName xml.Name `xml:"urn:ieee:std:2030.5:ns DeviceInformation"`
	Resource
	PollRate uint32 `xml:"pollRate,attr,omitempty"` // 2023: added

	// XSD element order (2023):
	ConnectionPointID string `xml:"connectionPointID,omitempty"` // 2023: NEW
	// DRLCCapabilities omitted (complex sub-type, deferred)
	FunctionsImplemented *uint64 `xml:"functionsImplemented,omitempty"` // bitmap
	// GPSLocation omitted (complex sub-type, deferred)
	LFDI           string `xml:"lFDI,omitempty"`
	MfDate         int64  `xml:"mfDate,omitempty"`
	MfHwVer        string `xml:"mfHwVer,omitempty"`
	MfID           string `xml:"mfID,omitempty"`
	MfInfo         string `xml:"mfInfo,omitempty"`
	MfModel        string `xml:"mfModel,omitempty"`
	MfSerNum       string `xml:"mfSerNum,omitempty"`
	PrimaryPower   int32  `xml:"primaryPower,omitempty"`
	SecondaryPower int32  `xml:"secondaryPower,omitempty"`
	// SupportedLocaleListLink omitted (deferred)
	SwActTime int64  `xml:"swActTime,omitempty"`
	SwVer     string `xml:"swVer,omitempty"`
}

// Copy returns an independent copy.
func (d DeviceInformation) Copy() DeviceInformation {
	c := d
	if d.FunctionsImplemented != nil {
		v := *d.FunctionsImplemented
		c.FunctionsImplemented = &v
	}
	return c
}
