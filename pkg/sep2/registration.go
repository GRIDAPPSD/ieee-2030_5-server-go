package sep2

import "encoding/xml"

// Registration tracks a device's registration status.
// Spec reference: section 6.9
type Registration struct {
	XMLName              xml.Name `xml:"urn:ieee:std:2030.5:ns Registration"`
	Resource
	DateTimeRegistered   int64  `xml:"dateTimeRegistered"`
	PIN                  uint32 `xml:"pIN"`
	PollRate             uint32 `xml:"pollRate,attr,omitempty"`
}

// Copy returns an independent copy of the Registration.
func (r Registration) Copy() Registration {
	return r
}
