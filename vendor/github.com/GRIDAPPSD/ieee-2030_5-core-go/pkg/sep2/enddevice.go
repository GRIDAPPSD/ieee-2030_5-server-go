package sep2

import "encoding/xml"

// EndDevice represents a client device registered with the server.
// Spec reference: section 8.5
//
// Field order matches the combined AbstractDevice + EndDevice xsd:sequence
// (AbstractDevice's own sequence elements come first per xsd:extension
// composition, then EndDevice's own sequence elements). Only fields that
// exist on this Go type are represented; AbstractDevice/EndDevice elements
// not yet modeled here (ConfigurationLink, deviceCategory,
// DeviceInformationLink, DeviceStatusLink, FileStatusLink,
// IPInterfaceListLink, LoadShedAvailabilityListLink, PowerStatusLink,
// postRate) are omitted.
// Schema parsers (e.g., EPRI oeg_client) validate element order strictly.
type EndDevice struct {
	XMLName xml.Name `xml:"urn:ieee:std:2030.5:ns EndDevice"`
	SubscribableResource

	// --- AbstractDevice sequence (in XSD order, subset present here) ---
	DERListLink      *ListLink `xml:"DERListLink,omitempty"`
	LFDI             string    `xml:"lFDI,omitempty"`
	LogEventListLink *ListLink `xml:"LogEventListLink,omitempty"`
	SFDI             string    `xml:"sFDI"`

	// --- EndDevice's own sequence (in XSD order) ---
	ChangedTime int64 `xml:"changedTime"`
	Enabled     *bool `xml:"enabled,omitempty"`
	// FlowReservationRequestListLink and FlowReservationResponseListLink
	// advertise the per-EndDevice flow reservation lists the server already
	// mounts (GRIDAPPSD/ieee-2030_5-core-go#177); without them a client that
	// discovers resources by following links can never reach either list.
	FlowReservationRequestListLink  *ListLink `xml:"FlowReservationRequestListLink,omitempty"`
	FlowReservationResponseListLink *ListLink `xml:"FlowReservationResponseListLink,omitempty"`
	FunctionSetAssignmentsListLink  *ListLink `xml:"FunctionSetAssignmentsListLink,omitempty"`
	RegistrationLink                *Link     `xml:"RegistrationLink,omitempty"`
	// SubscriptionListLink is the per-EndDevice subscription list
	// (GRIDAPPSD/ieee-2030_5-server-go#180), per IEEE 2030.5 section
	// 10.5.5 / CSIP V1.2 CORE-018 step 1. Servers that
	// support subscription/notification advertise this link so an inverter
	// can POST a Subscription resource to it; servers that don't simply
	// omit it (and any POST to a non-advertised path returns 405, which the
	// inverter handles as polling-only fallback). omitempty preserves
	// backward XML compatibility, existing EndDevice payloads without this
	// link round-trip unchanged.
	SubscriptionListLink *ListLink `xml:"SubscriptionListLink,omitempty"`
}

// Copy returns an independent copy of the EndDevice.
func (e EndDevice) Copy() EndDevice {
	c := e
	c.XMLName = e.XMLName
	if e.Enabled != nil {
		v := *e.Enabled
		c.Enabled = &v
	}
	if e.Subscribable != nil {
		v := *e.Subscribable
		c.Subscribable = &v
	}
	if e.RegistrationLink != nil {
		l := *e.RegistrationLink
		c.RegistrationLink = &l
	}
	if e.FlowReservationRequestListLink != nil {
		l := *e.FlowReservationRequestListLink
		c.FlowReservationRequestListLink = &l
	}
	if e.FlowReservationResponseListLink != nil {
		l := *e.FlowReservationResponseListLink
		c.FlowReservationResponseListLink = &l
	}
	if e.FunctionSetAssignmentsListLink != nil {
		l := *e.FunctionSetAssignmentsListLink
		c.FunctionSetAssignmentsListLink = &l
	}
	if e.DERListLink != nil {
		l := *e.DERListLink
		c.DERListLink = &l
	}
	if e.LogEventListLink != nil {
		l := *e.LogEventListLink
		c.LogEventListLink = &l
	}
	if e.SubscriptionListLink != nil {
		l := *e.SubscriptionListLink
		c.SubscriptionListLink = &l
	}
	return c
}

// EndDeviceList is a list of EndDevice resources.
type EndDeviceList struct {
	XMLName xml.Name `xml:"urn:ieee:std:2030.5:ns EndDeviceList"`
	ListResource
	EndDevice []EndDevice `xml:"EndDevice,omitempty"`
}

// SelfDevice represents the server's own device identity.
// Spec reference: section 8.4
type SelfDevice struct {
	XMLName xml.Name `xml:"urn:ieee:std:2030.5:ns SelfDevice"`
	SubscribableResource
	LFDI string `xml:"lFDI,omitempty"`
	SFDI string `xml:"sFDI"`

	DeviceInformationLink *Link `xml:"DeviceInformationLink,omitempty"`
}

// Copy returns an independent copy of the SelfDevice.
func (s SelfDevice) Copy() SelfDevice {
	c := s
	if s.DeviceInformationLink != nil {
		l := *s.DeviceInformationLink
		c.DeviceInformationLink = &l
	}
	return c
}
