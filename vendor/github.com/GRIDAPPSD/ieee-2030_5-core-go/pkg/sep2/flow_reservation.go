package sep2

import "encoding/xml"

// RequestStatus is the FlowReservationRequest status element (sep.xsd
// complexType RequestStatus): a timestamp of the status change and the
// status value itself, both mandatory. dateTime must be the instant the
// status changed, never a future or past time (sep.xsd annotation on the
// element).
type RequestStatus struct {
	DateTime      int64 `xml:"dateTime"`
	RequestStatus uint8 `xml:"requestStatus"` // RequestStatusRequested or RequestStatusCancelled
}

// FlowReservationRequest is a client request to reserve energy flow.
// Spec reference: section 10.9
// Element order matches 2023 XSD.
type FlowReservationRequest struct {
	XMLName xml.Name `xml:"urn:ieee:std:2030.5:ns FlowReservationRequest"`
	Resource
	MRID        string  `xml:"mRID,omitempty"`
	Description string  `xml:"description,omitempty"`
	Version     *uint16 `xml:"version,omitempty"`

	// XSD element order:
	CreationTime      int64             `xml:"creationTime"`
	DurationRequested *uint16           `xml:"durationRequested,omitempty"` // seconds
	EnergyRequested   *SignedRealEnergy `xml:"energyRequested,omitempty"`   // 2023: SignedRealEnergy
	IntervalRequested *DateTimeInterval `xml:"intervalRequested,omitempty"`
	PowerRequested    *ActivePower      `xml:"powerRequested,omitempty"`
	// RequestStatus is minOccurs="1" on the parent, so it is a value, not a
	// pointer: encoding/xml always emits a struct-valued field regardless of
	// omitempty, which is what a required element needs.
	RequestStatus RequestStatus `xml:"RequestStatus"`
}

// Copy returns an independent copy.
func (f FlowReservationRequest) Copy() FlowReservationRequest {
	c := f
	if f.Version != nil {
		v := *f.Version
		c.Version = &v
	}
	if f.DurationRequested != nil {
		v := *f.DurationRequested
		c.DurationRequested = &v
	}
	if f.EnergyRequested != nil {
		v := *f.EnergyRequested
		c.EnergyRequested = &v
	}
	if f.IntervalRequested != nil {
		v := *f.IntervalRequested
		c.IntervalRequested = &v
	}
	if f.PowerRequested != nil {
		v := *f.PowerRequested
		c.PowerRequested = &v
	}
	// RequestStatus is a value, not a pointer, so `c := f` above already
	// copied it independently; no per-field clone is needed here.
	return c
}

// FlowReservationRequestList is a list of FlowReservationRequest resources.
type FlowReservationRequestList struct {
	XMLName xml.Name `xml:"urn:ieee:std:2030.5:ns FlowReservationRequestList"`
	ListResource
	FlowReservationRequest []FlowReservationRequest `xml:"FlowReservationRequest,omitempty"`
}

// FlowReservationResponse is the server's response to a FlowReservationRequest.
// Extends Event, not RandomizableEvent: sep.xsd derives it from Event
// directly, so it carries no randomizeStart/randomizeDuration (#107).
type FlowReservationResponse struct {
	XMLName xml.Name `xml:"urn:ieee:std:2030.5:ns FlowReservationResponse"`
	Event

	// XSD element order:
	EnergyAvailable *SignedRealEnergy `xml:"energyAvailable,omitempty"` // 2023: changed from RealEnergy
	PowerAvailable  *ActivePower      `xml:"powerAvailable,omitempty"`
	Subject         string            `xml:"subject,omitempty"` // mRID of the request
}

// Copy returns an independent copy.
func (f FlowReservationResponse) Copy() FlowReservationResponse {
	c := f
	if f.ResponseRequired != nil {
		v := *f.ResponseRequired
		c.ResponseRequired = &v
	}
	if f.EventStatus != nil {
		es := f.EventStatus.Copy()
		c.EventStatus = &es
	}
	if f.Interval != nil {
		i := *f.Interval
		c.Interval = &i
	}
	if f.EnergyAvailable != nil {
		v := *f.EnergyAvailable
		c.EnergyAvailable = &v
	}
	if f.PowerAvailable != nil {
		v := *f.PowerAvailable
		c.PowerAvailable = &v
	}
	return c
}

// FlowReservationResponseList is a list of FlowReservationResponse resources.
type FlowReservationResponseList struct {
	XMLName xml.Name `xml:"urn:ieee:std:2030.5:ns FlowReservationResponseList"`
	ListResource
	FlowReservationResponse []FlowReservationResponse `xml:"FlowReservationResponse,omitempty"`
}

// RequestStatus constants.
const (
	RequestStatusRequested uint8 = 0
	RequestStatusCancelled uint8 = 1
)
