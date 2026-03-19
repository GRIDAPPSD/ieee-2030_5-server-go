package sep2

import "encoding/xml"

// EndDevice represents a client device registered with the server.
// Spec reference: section 8.5
type EndDevice struct {
	XMLName xml.Name `xml:"urn:ieee:std:2030.5:ns EndDevice"`
	SubscribableResource
	ChangedTime int64  `xml:"changedTime"`
	Enabled     *bool  `xml:"enabled,omitempty"`
	LFDI        string `xml:"lFDI,omitempty"`
	SFDI        string `xml:"sFDI"`

	// Sub-resource links
	RegistrationLink               *Link     `xml:"RegistrationLink,omitempty"`
	FunctionSetAssignmentsListLink *ListLink `xml:"FunctionSetAssignmentsListLink,omitempty"`
	DERListLink                    *ListLink `xml:"DERListLink,omitempty"`
	LogEventListLink               *ListLink `xml:"LogEventListLink,omitempty"`
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
	return c
}

// EndDeviceList is a list of EndDevice resources.
type EndDeviceList struct {
	XMLName   xml.Name    `xml:"urn:ieee:std:2030.5:ns EndDeviceList"`
	ListResource
	EndDevice []EndDevice `xml:"EndDevice,omitempty"`
}

// SelfDevice represents the server's own device identity.
// Spec reference: section 8.4
type SelfDevice struct {
	XMLName xml.Name `xml:"urn:ieee:std:2030.5:ns SelfDevice"`
	SubscribableResource
	LFDI    string `xml:"lFDI,omitempty"`
	SFDI    string `xml:"sFDI"`

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
