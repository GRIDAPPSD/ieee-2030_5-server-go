package sep2

import "encoding/xml"

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
	RequestStatus     *uint8            `xml:"RequestStatus,omitempty"` // 0=requested, 1=cancelled
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
	if f.RequestStatus != nil {
		v := *f.RequestStatus
		c.RequestStatus = &v
	}
	return c
}

// FlowReservationRequestList is a list of FlowReservationRequest resources.
type FlowReservationRequestList struct {
	XMLName xml.Name `xml:"urn:ieee:std:2030.5:ns FlowReservationRequestList"`
	ListResource
	FlowReservationRequest []FlowReservationRequest `xml:"FlowReservationRequest,omitempty"`
}

// FlowReservationResponse is the server's response to a FlowReservationRequest.
// Extends Event (scheduled activation).
type FlowReservationResponse struct {
	XMLName xml.Name `xml:"urn:ieee:std:2030.5:ns FlowReservationResponse"`
	RandomizableEvent

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
