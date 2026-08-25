package sep2

import "encoding/xml"

// FunctionSetAssignments maps function sets to a device.
// Spec reference: section 8.6
//
// Field order matches the FunctionSetAssignmentsBase + FunctionSetAssignments
// combined xsd:sequence: FunctionSetAssignmentsBase's own Link fields
// (CustomerAccountListLink, DemandResponseProgramListLink, DERProgramListLink,
// FileListLink, MessagingProgramListLink, PrepaymentListLink,
// ResponseSetListLink, TariffProfileListLink, TimeLink, UsagePointListLink,
// subset present here) come FIRST, THEN FunctionSetAssignments' own
// sequence (mRID, description; version omitted, not yet modeled).
type FunctionSetAssignments struct {
	XMLName xml.Name `xml:"urn:ieee:std:2030.5:ns FunctionSetAssignments"`
	Resource

	DemandResponseProgramListLink *ListLink `xml:"DemandResponseProgramListLink,omitempty"`
	DERProgramListLink            *ListLink `xml:"DERProgramListLink,omitempty"`
	UsagePointListLink            *ListLink `xml:"UsagePointListLink,omitempty"`

	MRID        string `xml:"mRID,omitempty"`
	Description string `xml:"description,omitempty"`
}

// Copy returns an independent copy.
func (f FunctionSetAssignments) Copy() FunctionSetAssignments {
	c := f
	if f.DERProgramListLink != nil {
		l := *f.DERProgramListLink
		c.DERProgramListLink = &l
	}
	if f.UsagePointListLink != nil {
		l := *f.UsagePointListLink
		c.UsagePointListLink = &l
	}
	if f.DemandResponseProgramListLink != nil {
		l := *f.DemandResponseProgramListLink
		c.DemandResponseProgramListLink = &l
	}
	return c
}

// FunctionSetAssignmentsList is a list of FunctionSetAssignments.
type FunctionSetAssignmentsList struct {
	XMLName xml.Name `xml:"urn:ieee:std:2030.5:ns FunctionSetAssignmentsList"`
	ListResource
	FunctionSetAssignments []FunctionSetAssignments `xml:"FunctionSetAssignments,omitempty"`
}
