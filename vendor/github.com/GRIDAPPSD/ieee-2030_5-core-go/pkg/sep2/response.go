package sep2

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"strings"
)

// Response represents a client's response to an event.
// Spec reference: section 8.8
// Element order matches 2023 XSD.
type Response struct {
	XMLName xml.Name `xml:"urn:ieee:std:2030.5:ns Response"`
	Resource

	// XSD element order:
	CreatedDateTime int64  `xml:"createdDateTime,omitempty"`
	EndDeviceLFDI   string `xml:"endDeviceLFDI,omitempty"`
	Status          *uint8 `xml:"status,omitempty"`  // ResponseStatus enum
	Subject         string `xml:"subject,omitempty"` // mRID of the event
}

// Copy returns an independent copy.
func (r Response) Copy() Response {
	c := r
	if r.Status != nil {
		v := *r.Status
		c.Status = &v
	}
	return c
}

// ResponseList is a list of Response resources.
type ResponseList struct {
	XMLName xml.Name `xml:"urn:ieee:std:2030.5:ns ResponseList"`
	ListResource
	Response []Response `xml:"Response,omitempty"`
}

// ResponseSet groups responses for management.
type ResponseSet struct {
	XMLName xml.Name `xml:"urn:ieee:std:2030.5:ns ResponseSet"`
	Resource
	MRID        string  `xml:"mRID,omitempty"`
	Description string  `xml:"description,omitempty"`
	Version     *uint16 `xml:"version,omitempty"`

	ResponseListLink *ListLink `xml:"ResponseListLink,omitempty"`
}

// Copy returns an independent copy.
func (r ResponseSet) Copy() ResponseSet {
	c := r
	if r.Version != nil {
		v := *r.Version
		c.Version = &v
	}
	if r.ResponseListLink != nil {
		l := *r.ResponseListLink
		c.ResponseListLink = &l
	}
	return c
}

// ResponseSetList is a list of ResponseSet resources.
type ResponseSetList struct {
	XMLName xml.Name `xml:"urn:ieee:std:2030.5:ns ResponseSetList"`
	ListResource
	ResponseSet []ResponseSet `xml:"ResponseSet,omitempty"`
}

// DERControlResponse is a response specific to DER controls (2023 addition).
type DERControlResponse struct {
	XMLName xml.Name `xml:"urn:ieee:std:2030.5:ns DERControlResponse"`
	Response

	// ModesResponded carries a DERControlType bitmap, so it serializes as
	// hexBinary like every other DERControlType-valued field. The 2023
	// edition introduced this element; the 2018 canonical schema this
	// package is otherwise audited against defines DERControlResponse as a
	// bare extension of Response (sep.xsd:440-447) and so carries no line
	// to cite for the element itself. The type citation still holds:
	// DERControlType extends HexBinary32 (sep.xsd:3952). Leaving it decimal
	// would reproduce the modesSupported defect on a second field.
	ModesResponded *DERControlType `xml:"modesResponded,omitempty"`
}

// Copy returns an independent copy.
func (d DERControlResponse) Copy() DERControlResponse {
	c := d
	c.Response = d.Response.Copy()
	if d.ModesResponded != nil {
		v := *d.ModesResponded
		c.ModesResponded = &v
	}
	return c
}

// FlowReservationResponseResponse is a response to a FlowReservationResponse
// (sep.xsd:448-454). Like the other subtypes below it is a bare extension of
// Response: the XSD gives it no elements of its own, and the type exists only
// so the root element names which function set the response belongs to.
type FlowReservationResponseResponse struct {
	XMLName xml.Name `xml:"urn:ieee:std:2030.5:ns FlowReservationResponseResponse"`
	Response
}

// Copy returns an independent copy.
func (f FlowReservationResponseResponse) Copy() FlowReservationResponseResponse {
	c := f
	c.Response = f.Response.Copy()
	return c
}

// PriceResponse is a response related to a price message (sep.xsd:494-501).
type PriceResponse struct {
	XMLName xml.Name `xml:"urn:ieee:std:2030.5:ns PriceResponse"`
	Response
}

// Copy returns an independent copy.
func (p PriceResponse) Copy() PriceResponse {
	c := p
	c.Response = p.Response.Copy()
	return c
}

// TextResponse is a response to a text message (sep.xsd:574-581).
type TextResponse struct {
	XMLName xml.Name `xml:"urn:ieee:std:2030.5:ns TextResponse"`
	Response
}

// Copy returns an independent copy.
func (t TextResponse) Copy() TextResponse {
	c := t
	c.Response = t.Response.Copy()
	return c
}

// ResponseRootElements are the root element local names DecodeResponse
// accepts, in the order the error message lists them.
//
// The set is taken from the WADL, which puts six resources at one sample
// path, /rsps/{id1}/rsp/{id2}: Response, PriceResponse, TextResponse,
// DERControlResponse, FlowReservationResponseResponse and DrResponse. Five of
// them appear here; DrResponse is refused deliberately, see DecodeResponse.
var ResponseRootElements = []string{
	"Response",
	"DERControlResponse",
	"FlowReservationResponseResponse",
	"PriceResponse",
	"TextResponse",
}

// DecodeResponse decodes a POSTed Response document, accepting any of the
// Response subtype root elements the WADL declares for the ResponseList POST,
// and returns the base Response the server stores.
//
// # Why this is not one xml.Unmarshal
//
// Response.XMLName is pinned, so unmarshalling a <DERControlResponse> body
// into a Response fails and the caller answers 400. That is what the EPRI
// reference client received for doing exactly the right thing. The repair is
// NOT to loosen the pin: the pin is what stops an unrelated resource from
// being accepted as a Response and recorded against an event nobody
// acknowledged. Instead this dispatches on the root element and decodes the
// matching type, each of which pins its own XMLName just as tightly, so the
// accepted set is exactly the declared subtypes and nothing else.
//
// The namespace is enforced by those pins rather than checked here: a body
// whose root local name matches but whose namespace does not still fails, and
// fails with encoding/xml naming both, which is more use to an operator than
// a message this function could write.
//
// # What the returned Response carries, and what it does not
//
// The returned value's XMLName is cleared. Every response is stored in one
// ResponseList whose members sep.xsd declares as <Response> elements, and a
// populated XMLName would make encoding/xml emit a redundant namespace
// declaration on some members and not others, so two responses with identical
// data would serialize differently depending only on which root element the
// client POSTed.
//
// Elements a subtype adds beyond Response are NOT preserved, because the
// server's response store holds the base type. In the 2018 canonical schema
// this loses nothing for the four subtypes accepted here: all four are bare
// extensions. It does drop DERControlResponse.modesResponded, which the 2023
// edition adds. That is a known, bounded loss and it is why DrResponse (which
// the XSD gives six children of its own, sep.xsd:477-491) is refused rather
// than quietly flattened.
func DecodeResponse(data []byte) (Response, error) {
	root, err := rootElementName(data)
	if err != nil {
		return Response{}, err
	}

	var out Response
	switch root.Local {
	case "Response":
		var v Response
		if err := xml.Unmarshal(data, &v); err != nil {
			return Response{}, fmt.Errorf("sep2: decoding Response: %w", err)
		}
		out = v
	case "DERControlResponse":
		var v DERControlResponse
		if err := xml.Unmarshal(data, &v); err != nil {
			return Response{}, fmt.Errorf("sep2: decoding DERControlResponse: %w", err)
		}
		out = v.Response
	case "FlowReservationResponseResponse":
		var v FlowReservationResponseResponse
		if err := xml.Unmarshal(data, &v); err != nil {
			return Response{}, fmt.Errorf("sep2: decoding FlowReservationResponseResponse: %w", err)
		}
		out = v.Response
	case "PriceResponse":
		var v PriceResponse
		if err := xml.Unmarshal(data, &v); err != nil {
			return Response{}, fmt.Errorf("sep2: decoding PriceResponse: %w", err)
		}
		out = v.Response
	case "TextResponse":
		var v TextResponse
		if err := xml.Unmarshal(data, &v); err != nil {
			return Response{}, fmt.Errorf("sep2: decoding TextResponse: %w", err)
		}
		out = v.Response
	case "DrResponse":
		// Refused rather than flattened. DrResponse extends Response with six
		// DRLC-specific children and this package models none of them, so
		// decoding one into the base would return 201 over data the client
		// sent and the server silently discarded. Nothing is owed a
		// DrResponse today: no EndDeviceControl route is served for one to
		// answer. Model the DRLC children before accepting it.
		return Response{}, fmt.Errorf(
			"sep2: DrResponse is a declared Response subtype but its DRLC-specific elements are not modelled, so accepting it would discard them")
	default:
		return Response{}, fmt.Errorf(
			"sep2: root element %q is not a Response subtype this server accepts (accepted: %s)",
			root.Local, strings.Join(ResponseRootElements, ", "))
	}

	out.XMLName = xml.Name{}
	return out, nil
}

// rootElementName returns the name of a document's first start element.
//
// It reads tokens rather than unmarshalling because the point is to learn
// which type to unmarshal INTO. A document with no start element at all (empty
// body, chardata only, malformed) is an error here rather than an empty name,
// so a caller cannot mistake "no root" for "root that matched nothing".
func rootElementName(data []byte) (xml.Name, error) {
	dec := xml.NewDecoder(bytes.NewReader(data))
	for {
		tok, err := dec.Token()
		if err != nil {
			if err == io.EOF {
				return xml.Name{}, fmt.Errorf("sep2: document has no root element")
			}
			return xml.Name{}, fmt.Errorf("sep2: reading root element: %w", err)
		}
		if start, ok := tok.(xml.StartElement); ok {
			return start.Name, nil
		}
	}
}

// ResponseStatus constants per IEEE 2030.5-2023 section 10.10 Table 31
// ("Response types by function set"). The 2018 revision called this
// table number 27; the contents (wire values) are unchanged.
//
// Wire values are NORMATIVE: they appear in the `<status>` element of a
// `Response` POST and are interpreted by the server-side handler. The
// values in this enum used to be off-by-one against Table 31 (EventReceived
// was 0 instead of 1, EventCancelled was 5 instead of 6, fixed by
// GRIDAPPSD/ieee-2030_5-server-go#109), which forced the
// CSIP test `test/csip/core_022_responses_test.go` to emit raw uint8
// literals 1/2/3/6 to bypass the wrong constants. The values below are the
// wire-correct Table 31 assignments.
const (
	// ResponseStatusEventReserved (0): Reserved per Table 31. Not a
	// valid wire value; included for completeness so callers do not
	// accidentally use the zero value as a meaningful status.
	ResponseStatusEventReserved uint8 = 0
	// ResponseStatusEventReceived (1): Event or DefaultDERControl
	// received by the client.
	ResponseStatusEventReceived uint8 = 1
	// ResponseStatusEventStarted (2): Event or DefaultDERControl
	// started : currently being applied by the client.
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
	// ResponseStatusEventCancelled (6): Spec name "Cancelled" : the
	// event has been cancelled or the executing DefaultDERControl
	// value has been removed.
	ResponseStatusEventCancelled uint8 = 6
	// ResponseStatusEventSuperseded (7): "Superseded, Same Function
	// Set Instance" : event was superseded by another event or
	// DefaultDERControl from the same function set instance.
	ResponseStatusEventSuperseded uint8 = 7
	// ResponseStatusEventPartialOptOut (8): "Partial Complete,
	// Opt-Out" : event partially completed with user opt-out.
	ResponseStatusEventPartialOptOut uint8 = 8
	// ResponseStatusEventPartialOptIn (9): "Partial Complete,
	// Opt-In" : event partially completed due to user opt-in.
	ResponseStatusEventPartialOptIn uint8 = 9
	// ResponseStatusEventCompletedNoApply (10): "Complete, No
	// Participation" : event completed with no user participation
	// (previous opt-out).
	ResponseStatusEventCompletedNoApply uint8 = 10
	// ResponseStatusEventAcknowledged (11): User has acknowledged
	// the event (Pricing / Messaging / DRLC).
	ResponseStatusEventAcknowledged uint8 = 11
	// ResponseStatusEventCannotBeDisplayed (12): Client is unable to
	// display the message (Messaging function set only).
	ResponseStatusEventCannotBeDisplayed uint8 = 12
	// ResponseStatusEventSupersededAlternateServer (13): "Superseded,
	// Alternate Server" : event superseded by an event from a
	// different function set instance on a different server.
	ResponseStatusEventSupersededAlternateServer uint8 = 13
	// ResponseStatusEventSupersededAlternateProgram (14):
	// "Superseded, Alternate Program" : event superseded by an event
	// from a different function set instance on the same server.
	ResponseStatusEventSupersededAlternateProgram uint8 = 14
	// ResponseStatusEventResumed (15): Event resumed : the client
	// has resumed an overlapped event after the overlapping one
	// completed.
	ResponseStatusEventResumed uint8 = 15
)
