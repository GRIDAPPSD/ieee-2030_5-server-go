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

// ResponseStatus constants per IEEE 2030.5-2023 §10.10 Table 31
// ("Response types by function set"). The 2018 revision called this
// table number 27; the contents (wire values) are unchanged.
//
// Wire values are NORMATIVE — they appear in the `<status>` element of a
// `Response` POST and are interpreted by the server-side handler. The
// pre-IEEE-044a values in this enum were off-by-one against Table 31
// (EventReceived was 0 instead of 1, EventCancelled was 5 instead of 6),
// which forced the CSIP test `test/csip/core_022_responses_test.go` to
// emit raw uint8 literals 1/2/3/6 to bypass the wrong constants. The
// values below are the wire-correct Table 31 assignments.
const (
	// ResponseStatusEventReserved (0): Reserved per Table 31. Not a
	// valid wire value; included for completeness so callers do not
	// accidentally use the zero value as a meaningful status.
	ResponseStatusEventReserved uint8 = 0
	// ResponseStatusEventReceived (1): Event or DefaultDERControl
	// received by the client.
	ResponseStatusEventReceived uint8 = 1
	// ResponseStatusEventStarted (2): Event or DefaultDERControl
	// started — currently being applied by the client.
	ResponseStatusEventStarted uint8 = 2
	// ResponseStatusEventCompleted (3): Event completed (without
	// error, fully and successfully).
	ResponseStatusEventCompleted uint8 = 3
	// ResponseStatusOptOut (4): User has chosen to opt out of the
	// event (DRLC). Can occur before event begins.
	ResponseStatusOptOut uint8 = 4
	// ResponseStatusOptIn (5): User has chosen to opt in to the
	// event (DRLC). Can occur before event begins.
	ResponseStatusOptIn uint8 = 5
	// ResponseStatusEventCancelled (6): Spec name "Cancelled" — the
	// event has been cancelled or the executing DefaultDERControl
	// value has been removed.
	ResponseStatusEventCancelled uint8 = 6
	// ResponseStatusEventSuperseded (7): "Superseded, Same Function
	// Set Instance" — event was superseded by another event or
	// DefaultDERControl from the same function set instance.
	ResponseStatusEventSuperseded uint8 = 7
	// ResponseStatusEventPartialOptOut (8): "Partial Complete,
	// Opt-Out" — event partially completed with user opt-out.
	ResponseStatusEventPartialOptOut uint8 = 8
	// ResponseStatusEventPartialOptIn (9): "Partial Complete,
	// Opt-In" — event partially completed due to user opt-in.
	ResponseStatusEventPartialOptIn uint8 = 9
	// ResponseStatusEventCompletedNoApply (10): "Complete, No
	// Participation" — event completed with no user participation
	// (previous opt-out).
	ResponseStatusEventCompletedNoApply uint8 = 10
	// ResponseStatusEventAcknowledged (11): User has acknowledged
	// the event (Pricing / Messaging / DRLC).
	ResponseStatusEventAcknowledged uint8 = 11
	// ResponseStatusEventCannotBeDisplayed (12): Client is unable to
	// display the message (Messaging function set only).
	ResponseStatusEventCannotBeDisplayed uint8 = 12
	// ResponseStatusEventSupersededAlternateServer (13): "Superseded,
	// Alternate Server" — event superseded by an event from a
	// different function set instance on a different server.
	ResponseStatusEventSupersededAlternateServer uint8 = 13
	// ResponseStatusEventSupersededAlternateProgram (14):
	// "Superseded, Alternate Program" — event superseded by an event
	// from a different function set instance on the same server.
	ResponseStatusEventSupersededAlternateProgram uint8 = 14
	// ResponseStatusEventResumed (15): Event resumed — the client
	// has resumed an overlapped event after the overlapping one
	// completed.
	ResponseStatusEventResumed uint8 = 15
)
