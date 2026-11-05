package sep2

import "encoding/xml"

// EndDevice represents a client device registered with the server.
// Spec reference: section 8.5
//
// Field order matches the combined AbstractDevice + EndDevice xsd:sequence
// (AbstractDevice's own sequence elements come first per xsd:extension
// composition, then EndDevice's own sequence elements). Every Link element
// of the sequence is modeled; the non-link element deviceCategory is not yet
// modeled and is omitted.
// Schema parsers (e.g., EPRI oeg_client) validate element order strictly.
type EndDevice struct {
	XMLName xml.Name `xml:"urn:ieee:std:2030.5:ns EndDevice"`
	SubscribableResource

	// --- AbstractDevice sequence (in XSD order, subset present here) ---
	ConfigurationLink            *Link     `xml:"ConfigurationLink,omitempty"`
	DERListLink                  *ListLink `xml:"DERListLink,omitempty"`
	DeviceInformationLink        *Link     `xml:"DeviceInformationLink,omitempty"`
	DeviceStatusLink             *Link     `xml:"DeviceStatusLink,omitempty"`
	FileStatusLink               *Link     `xml:"FileStatusLink,omitempty"`
	IPInterfaceListLink          *ListLink `xml:"IPInterfaceListLink,omitempty"`
	LFDI                         string    `xml:"lFDI,omitempty"`
	LoadShedAvailabilityListLink *ListLink `xml:"LoadShedAvailabilityListLink,omitempty"`
	LogEventListLink             *ListLink `xml:"LogEventListLink,omitempty"`
	PowerStatusLink              *Link     `xml:"PowerStatusLink,omitempty"`
	SFDI                         string    `xml:"sFDI"`

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
	c.Enabled = clonePtr(e.Enabled)
	c.SubscribableResource = e.SubscribableResource.Copy()
	c.ConfigurationLink = clonePtr(e.ConfigurationLink)
	c.DERListLink = clonePtr(e.DERListLink)
	c.DeviceInformationLink = clonePtr(e.DeviceInformationLink)
	c.DeviceStatusLink = clonePtr(e.DeviceStatusLink)
	c.FileStatusLink = clonePtr(e.FileStatusLink)
	c.IPInterfaceListLink = clonePtr(e.IPInterfaceListLink)
	c.LoadShedAvailabilityListLink = clonePtr(e.LoadShedAvailabilityListLink)
	c.LogEventListLink = clonePtr(e.LogEventListLink)
	c.PowerStatusLink = clonePtr(e.PowerStatusLink)
	c.FlowReservationRequestListLink = clonePtr(e.FlowReservationRequestListLink)
	c.FlowReservationResponseListLink = clonePtr(e.FlowReservationResponseListLink)
	c.FunctionSetAssignmentsListLink = clonePtr(e.FunctionSetAssignmentsListLink)
	c.RegistrationLink = clonePtr(e.RegistrationLink)
	c.SubscriptionListLink = clonePtr(e.SubscriptionListLink)
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
	c.SubscribableResource = s.SubscribableResource.Copy()
	c.DeviceInformationLink = clonePtr(s.DeviceInformationLink)
	return c
}
