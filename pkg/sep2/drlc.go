package sep2

import "encoding/xml"

// DemandResponseProgram is a demand response program.
// Spec reference: section 10.3 (stub — handlers deferred)
type DemandResponseProgram struct {
	XMLName                         xml.Name `xml:"urn:ieee:std:2030.5:ns DemandResponseProgram"`
	SubscribableResource
	MRID                            string   `xml:"mRID,omitempty"`
	Description                     string   `xml:"description,omitempty"`
	Primacy                         uint8    `xml:"primacy"`
	ActiveEndDeviceControlListLink  *ListLink `xml:"ActiveEndDeviceControlListLink,omitempty"`
	EndDeviceControlListLink        *ListLink `xml:"EndDeviceControlListLink,omitempty"`
}

// Copy returns an independent copy.
func (d DemandResponseProgram) Copy() DemandResponseProgram {
	c := d
	if d.ActiveEndDeviceControlListLink != nil { l := *d.ActiveEndDeviceControlListLink; c.ActiveEndDeviceControlListLink = &l }
	if d.EndDeviceControlListLink != nil { l := *d.EndDeviceControlListLink; c.EndDeviceControlListLink = &l }
	return c
}

// DemandResponseProgramList is a list of DemandResponseProgram resources.
type DemandResponseProgramList struct {
	XMLName               xml.Name                `xml:"urn:ieee:std:2030.5:ns DemandResponseProgramList"`
	ListResource
	DemandResponseProgram []DemandResponseProgram `xml:"DemandResponseProgram,omitempty"`
}

// EndDeviceControl is a demand response control event.
type EndDeviceControl struct {
	XMLName        xml.Name `xml:"urn:ieee:std:2030.5:ns EndDeviceControl"`
	RandomizableEvent
	DeviceCategory *uint32  `xml:"deviceCategory,omitempty"`
}

// Copy returns an independent copy.
func (e EndDeviceControl) Copy() EndDeviceControl {
	c := e
	if e.DeviceCategory != nil { v := *e.DeviceCategory; c.DeviceCategory = &v }
	if e.EventStatus != nil { es := e.EventStatus.Copy(); c.EventStatus = &es }
	if e.Interval != nil { i := *e.Interval; c.Interval = &i }
	return c
}

// EndDeviceControlList is a list of EndDeviceControl resources.
type EndDeviceControlList struct {
	XMLName          xml.Name           `xml:"urn:ieee:std:2030.5:ns EndDeviceControlList"`
	ListResource
	EndDeviceControl []EndDeviceControl `xml:"EndDeviceControl,omitempty"`
}
