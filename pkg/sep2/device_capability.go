package sep2

import "encoding/xml"

// DeviceCapability is the entry point resource for an IEEE 2030.5 server.
// It provides links to all supported function sets.
// Spec reference: section 8.3
//
// IMPORTANT: Field order matches XSD element ordering.
// FunctionSetAssignmentsBase elements come first, then DeviceCapability extensions.
// Schema parsers (e.g., EPRI client) validate element order strictly.
type DeviceCapability struct {
	XMLName  xml.Name `xml:"urn:ieee:std:2030.5:ns DeviceCapability"`
	Resource
	PollRate uint32 `xml:"pollRate,attr,omitempty"`

	// --- FunctionSetAssignmentsBase elements (in XSD order) ---
	CustomerAccountListLink      *ListLink `xml:"CustomerAccountListLink,omitempty"`
	DemandResponseProgramListLink *ListLink `xml:"DemandResponseProgramListLink,omitempty"`
	DERProgramListLink           *ListLink `xml:"DERProgramListLink,omitempty"`
	FileListLink                 *ListLink `xml:"FileListLink,omitempty"`
	MessagingProgramListLink     *ListLink `xml:"MessagingProgramListLink,omitempty"`
	PrepaymentListLink           *ListLink `xml:"PrepaymentListLink,omitempty"`
	ResponseSetListLink          *ListLink `xml:"ResponseSetListLink,omitempty"`
	TariffProfileListLink        *ListLink `xml:"TariffProfileListLink,omitempty"`
	TimeLink                     *Link     `xml:"TimeLink,omitempty"`
	UsagePointListLink           *ListLink `xml:"UsagePointListLink,omitempty"`

	// --- DeviceCapability extension elements (in XSD order) ---
	EndDeviceListLink        *ListLink `xml:"EndDeviceListLink,omitempty"`
	MirrorUsagePointListLink *ListLink `xml:"MirrorUsagePointListLink,omitempty"`
	SelfDeviceLink           *Link     `xml:"SelfDeviceLink,omitempty"`
}
