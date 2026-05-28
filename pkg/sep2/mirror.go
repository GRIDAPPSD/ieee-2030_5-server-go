package sep2

import "encoding/xml"

// MirrorUsagePoint is a client-created resource for reporting metering data.
// Inverters POST to /mup to register, then POST readings to /mup/{id}/mr.
// Spec reference: section 10.11
type MirrorUsagePoint struct {
	XMLName xml.Name `xml:"urn:ieee:std:2030.5:ns MirrorUsagePoint"`
	Resource
	MRID                string  `xml:"mRID,omitempty"`
	Description         string  `xml:"description,omitempty"`
	DeviceLFDI          string  `xml:"deviceLFDI,omitempty"`
	PostRate            *uint32 `xml:"postRate,omitempty"`
	RoleFlags           uint16  `xml:"roleFlags,omitempty"`
	ServiceCategoryKind uint8   `xml:"serviceCategoryKind"`
	Status              uint8   `xml:"status"`

	MirrorMeterReadingListLink *ListLink `xml:"MirrorMeterReadingListLink,omitempty"`
}

// Copy returns an independent copy.
func (m MirrorUsagePoint) Copy() MirrorUsagePoint {
	c := m
	if m.PostRate != nil {
		v := *m.PostRate
		c.PostRate = &v
	}
	if m.MirrorMeterReadingListLink != nil {
		l := *m.MirrorMeterReadingListLink
		c.MirrorMeterReadingListLink = &l
	}
	return c
}

// MirrorUsagePointList is a list of MirrorUsagePoint resources.
type MirrorUsagePointList struct {
	XMLName xml.Name `xml:"urn:ieee:std:2030.5:ns MirrorUsagePointList"`
	ListResource
	MirrorUsagePoint []MirrorUsagePoint `xml:"MirrorUsagePoint,omitempty"`
}

// MirrorMeterReading contains metering data posted by a device.
type MirrorMeterReading struct {
	XMLName xml.Name `xml:"urn:ieee:std:2030.5:ns MirrorMeterReading"`
	Resource
	MRID           string       `xml:"mRID,omitempty"`
	Description    string       `xml:"description,omitempty"`
	LastUpdateTime int64        `xml:"lastUpdateTime,omitempty"`
	ReadingType    *ReadingType `xml:"ReadingType,omitempty"`
	Reading        *Reading     `xml:"Reading,omitempty"`
}

// Copy returns an independent copy.
func (m MirrorMeterReading) Copy() MirrorMeterReading {
	c := m
	if m.ReadingType != nil {
		rt := m.ReadingType.Copy()
		c.ReadingType = &rt
	}
	if m.Reading != nil {
		r := m.Reading.Copy()
		c.Reading = &r
	}
	return c
}

// MirrorMeterReadingList is a list of MirrorMeterReading resources.
type MirrorMeterReadingList struct {
	XMLName xml.Name `xml:"urn:ieee:std:2030.5:ns MirrorMeterReadingList"`
	ListResource
	MirrorMeterReading []MirrorMeterReading `xml:"MirrorMeterReading,omitempty"`
}
