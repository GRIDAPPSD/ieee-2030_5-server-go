package sep2

import "encoding/xml"

// DeviceInformation provides metadata about a device.
// Spec reference: section 9.3
type DeviceInformation struct {
	XMLName        xml.Name `xml:"urn:ieee:std:2030.5:ns DeviceInformation"`
	Resource
	LFDI           string `xml:"lFDI,omitempty"`
	MfDate         int64  `xml:"mfDate,omitempty"`
	MfHwVer        string `xml:"mfHwVer,omitempty"`
	MfID           string `xml:"mfID,omitempty"`
	MfModel        string `xml:"mfModel,omitempty"`
	MfSerNum       string `xml:"mfSerNum,omitempty"`
	PrimaryPower   int32  `xml:"primaryPower,omitempty"`
	SecondaryPower int32  `xml:"secondaryPower,omitempty"`
	SwActTime      int64  `xml:"swActTime,omitempty"`
	SwVer          string `xml:"swVer,omitempty"`
}

// Copy returns an independent copy.
func (d DeviceInformation) Copy() DeviceInformation {
	return d
}
