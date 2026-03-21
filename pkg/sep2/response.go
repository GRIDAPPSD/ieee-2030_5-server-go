package sep2

import "encoding/xml"

// Response represents a client's response to an event.
// Spec reference: section 8.8
// Element order matches 2023 XSD.
type Response struct {
	XMLName         xml.Name `xml:"urn:ieee:std:2030.5:ns Response"`
	Resource

	// XSD element order:
	CreatedDateTime int64  `xml:"createdDateTime,omitempty"`
	EndDeviceLFDI   string `xml:"endDeviceLFDI,omitempty"`
	Status          *uint8 `xml:"status,omitempty"` // ResponseStatus enum
	Subject         string `xml:"subject,omitempty"` // mRID of the event
}

// Copy returns an independent copy.
func (r Response) Copy() Response {
	c := r
	if r.Status != nil { v := *r.Status; c.Status = &v }
	return c
}

// ResponseList is a list of Response resources.
type ResponseList struct {
	XMLName  xml.Name   `xml:"urn:ieee:std:2030.5:ns ResponseList"`
	ListResource
	Response []Response `xml:"Response,omitempty"`
}

// ResponseSet groups responses for management.
type ResponseSet struct {
	XMLName          xml.Name  `xml:"urn:ieee:std:2030.5:ns ResponseSet"`
	Resource
	MRID             string    `xml:"mRID,omitempty"`
	Description      string    `xml:"description,omitempty"`
	Version          *uint16   `xml:"version,omitempty"`

	ResponseListLink *ListLink `xml:"ResponseListLink,omitempty"`
}

// Copy returns an independent copy.
func (r ResponseSet) Copy() ResponseSet {
	c := r
	if r.Version != nil { v := *r.Version; c.Version = &v }
	if r.ResponseListLink != nil { l := *r.ResponseListLink; c.ResponseListLink = &l }
	return c
}

// ResponseSetList is a list of ResponseSet resources.
type ResponseSetList struct {
	XMLName     xml.Name      `xml:"urn:ieee:std:2030.5:ns ResponseSetList"`
	ListResource
	ResponseSet []ResponseSet `xml:"ResponseSet,omitempty"`
}

// DERControlResponse is a response specific to DER controls (2023 addition).
type DERControlResponse struct {
	XMLName        xml.Name `xml:"urn:ieee:std:2030.5:ns DERControlResponse"`
	Response

	ModesResponded *uint32 `xml:"modesResponded,omitempty"` // DERControlType bitmap
}

// Copy returns an independent copy.
func (d DERControlResponse) Copy() DERControlResponse {
	c := d
	c.Response = d.Response.Copy()
	if d.ModesResponded != nil { v := *d.ModesResponded; c.ModesResponded = &v }
	return c
}

// ResponseStatus constants per spec.
const (
	ResponseStatusEventReceived       uint8 = 0
	ResponseStatusEventStarted       uint8 = 1
	ResponseStatusEventCompleted     uint8 = 2
	ResponseStatusOptOut             uint8 = 3
	ResponseStatusOptIn              uint8 = 4
	ResponseStatusEventCancelled     uint8 = 5
	ResponseStatusEventSuperseded    uint8 = 6
	ResponseStatusEventPartialOptOut uint8 = 7
	ResponseStatusEventPartialOptIn  uint8 = 8
	ResponseStatusEventCompletedNoApply uint8 = 9
)
