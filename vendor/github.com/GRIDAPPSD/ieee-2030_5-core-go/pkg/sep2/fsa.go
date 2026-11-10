package sep2

import "encoding/xml"

// FunctionSetAssignmentsBase holds the ten Link elements that open the
// xsd:sequence of every type extending FunctionSetAssignmentsBase
// (DeviceCapability and FunctionSetAssignments). It is embedded so those
// elements marshal flat, ahead of the extending type's own elements.
// Spec reference: section 8.6
type FunctionSetAssignmentsBase struct {
	CustomerAccountListLink       *ListLink `xml:"CustomerAccountListLink,omitempty"`
	DemandResponseProgramListLink *ListLink `xml:"DemandResponseProgramListLink,omitempty"`
	DERProgramListLink            *ListLink `xml:"DERProgramListLink,omitempty"`
	FileListLink                  *ListLink `xml:"FileListLink,omitempty"`
	MessagingProgramListLink      *ListLink `xml:"MessagingProgramListLink,omitempty"`
	PrepaymentListLink            *ListLink `xml:"PrepaymentListLink,omitempty"`
	ResponseSetListLink           *ListLink `xml:"ResponseSetListLink,omitempty"`
	TariffProfileListLink         *ListLink `xml:"TariffProfileListLink,omitempty"`
	TimeLink                      *Link     `xml:"TimeLink,omitempty"`
	UsagePointListLink            *ListLink `xml:"UsagePointListLink,omitempty"`
}

// Copy returns an independent copy.
func (b FunctionSetAssignmentsBase) Copy() FunctionSetAssignmentsBase {
	c := b
	c.CustomerAccountListLink = clonePtr(b.CustomerAccountListLink)
	c.DemandResponseProgramListLink = clonePtr(b.DemandResponseProgramListLink)
	c.DERProgramListLink = clonePtr(b.DERProgramListLink)
	c.FileListLink = clonePtr(b.FileListLink)
	c.MessagingProgramListLink = clonePtr(b.MessagingProgramListLink)
	c.PrepaymentListLink = clonePtr(b.PrepaymentListLink)
	c.ResponseSetListLink = clonePtr(b.ResponseSetListLink)
	c.TariffProfileListLink = clonePtr(b.TariffProfileListLink)
	c.TimeLink = clonePtr(b.TimeLink)
	c.UsagePointListLink = clonePtr(b.UsagePointListLink)
	return c
}

// FunctionSetAssignments maps function sets to a device.
// Spec reference: section 8.6
//
// The embedded FunctionSetAssignmentsBase's Link fields come FIRST, THEN
// FunctionSetAssignments' own sequence (the embedded IdentifiedObject's mRID,
// description and version).
type FunctionSetAssignments struct {
	XMLName xml.Name `xml:"urn:ieee:std:2030.5:ns FunctionSetAssignments"`
	Resource
	FunctionSetAssignmentsBase
	IdentifiedObject
}

// Copy returns an independent copy.
func (f FunctionSetAssignments) Copy() FunctionSetAssignments {
	c := f
	c.FunctionSetAssignmentsBase = f.FunctionSetAssignmentsBase.Copy()
	c.IdentifiedObject = f.IdentifiedObject.Copy()
	return c
}

// FunctionSetAssignmentsList is a list of FunctionSetAssignments.
type FunctionSetAssignmentsList struct {
	XMLName xml.Name `xml:"urn:ieee:std:2030.5:ns FunctionSetAssignmentsList"`
	ListResource
	FunctionSetAssignments []FunctionSetAssignments `xml:"FunctionSetAssignments,omitempty"`
}
