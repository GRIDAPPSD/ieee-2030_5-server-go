package sep2

import "encoding/xml"

// DeviceCapability is the entry point resource for an IEEE 2030.5 server.
// It provides links to all supported function sets.
// Spec reference: section 8.3
type DeviceCapability struct {
	XMLName  xml.Name `xml:"urn:ieee:std:2030.5:ns DeviceCapability"`
	Resource
	PollRate uint32 `xml:"pollRate,attr,omitempty"`

	// Support resource links
	EndDeviceListLink *ListLink `xml:"EndDeviceListLink,omitempty"`
	SelfDeviceLink    *Link     `xml:"SelfDeviceLink,omitempty"`
	TimeLink          *Link     `xml:"TimeLink,omitempty"`

	// Metering links
	MirrorUsagePointListLink *ListLink `xml:"MirrorUsagePointListLink,omitempty"`
	UsagePointListLink       *ListLink `xml:"UsagePointListLink,omitempty"`

	// DER links
	DERProgramListLink *ListLink `xml:"DERProgramListLink,omitempty"`

	// DRLC links
	DemandResponseProgramListLink *ListLink `xml:"DemandResponseProgramListLink,omitempty"`

	// Messaging links
	MessagingProgramListLink *ListLink `xml:"MessagingProgramListLink,omitempty"`

	// Pricing links
	TariffProfileListLink *ListLink `xml:"TariffProfileListLink,omitempty"`

	// Prepayment links
	PrepaymentListLink *ListLink `xml:"PrepaymentListLink,omitempty"`

	// Response link
	ResponseSetListLink *ListLink `xml:"ResponseSetListLink,omitempty"`
}
