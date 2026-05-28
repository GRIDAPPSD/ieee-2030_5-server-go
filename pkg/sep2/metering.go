package sep2

import "encoding/xml"

// UsagePoint represents a physical metering point.
// Spec reference: section 10.4
type UsagePoint struct {
	XMLName xml.Name `xml:"urn:ieee:std:2030.5:ns UsagePoint"`
	SubscribableResource
	MRID                string `xml:"mRID,omitempty"`
	Description         string `xml:"description,omitempty"`
	RoleFlags           uint16 `xml:"roleFlags,omitempty"`
	ServiceCategoryKind uint8  `xml:"serviceCategoryKind"`
	Status              uint8  `xml:"status"`

	MeterReadingListLink *ListLink `xml:"MeterReadingListLink,omitempty"`
}

// Copy returns an independent copy.
func (u UsagePoint) Copy() UsagePoint {
	c := u
	if u.Subscribable != nil {
		v := *u.Subscribable
		c.Subscribable = &v
	}
	if u.MeterReadingListLink != nil {
		l := *u.MeterReadingListLink
		c.MeterReadingListLink = &l
	}
	return c
}

// UsagePointList is a list of UsagePoint resources.
type UsagePointList struct {
	XMLName xml.Name `xml:"urn:ieee:std:2030.5:ns UsagePointList"`
	ListResource
	UsagePoint []UsagePoint `xml:"UsagePoint,omitempty"`
}

// MeterReading describes a specific measurement type at a usage point.
type MeterReading struct {
	XMLName xml.Name `xml:"urn:ieee:std:2030.5:ns MeterReading"`
	SubscribableResource
	MRID        string `xml:"mRID,omitempty"`
	Description string `xml:"description,omitempty"`

	ReadingTypeLink    *Link     `xml:"ReadingTypeLink,omitempty"`
	ReadingLink        *Link     `xml:"ReadingLink,omitempty"`
	ReadingSetListLink *ListLink `xml:"ReadingSetListLink,omitempty"`
}

// Copy returns an independent copy.
func (m MeterReading) Copy() MeterReading {
	c := m
	if m.Subscribable != nil {
		v := *m.Subscribable
		c.Subscribable = &v
	}
	if m.ReadingTypeLink != nil {
		l := *m.ReadingTypeLink
		c.ReadingTypeLink = &l
	}
	if m.ReadingLink != nil {
		l := *m.ReadingLink
		c.ReadingLink = &l
	}
	if m.ReadingSetListLink != nil {
		l := *m.ReadingSetListLink
		c.ReadingSetListLink = &l
	}
	return c
}

// MeterReadingList is a list of MeterReading resources.
type MeterReadingList struct {
	XMLName xml.Name `xml:"urn:ieee:std:2030.5:ns MeterReadingList"`
	ListResource
	MeterReading []MeterReading `xml:"MeterReading,omitempty"`
}

// DateTimeInterval represents a time period with start and duration.
type DateTimeInterval struct {
	Duration uint32 `xml:"duration"`
	Start    int64  `xml:"start"`
}

// Reading is an individual meter data point.
type Reading struct {
	XMLName xml.Name `xml:"urn:ieee:std:2030.5:ns Reading"`
	Resource
	Value        *int64            `xml:"value,omitempty"`
	TimePeriod   *DateTimeInterval `xml:"timePeriod,omitempty"`
	QualityFlags *uint16           `xml:"qualityFlags,omitempty"`
}

// Copy returns an independent copy.
func (r Reading) Copy() Reading {
	c := r
	if r.Value != nil {
		v := *r.Value
		c.Value = &v
	}
	if r.TimePeriod != nil {
		tp := *r.TimePeriod
		c.TimePeriod = &tp
	}
	if r.QualityFlags != nil {
		q := *r.QualityFlags
		c.QualityFlags = &q
	}
	return c
}

// ReadingList is a list of Reading resources.
type ReadingList struct {
	XMLName xml.Name `xml:"urn:ieee:std:2030.5:ns ReadingList"`
	ListResource
	Reading []Reading `xml:"Reading,omitempty"`
}

// ReadingType describes the unit and semantics of a meter reading.
type ReadingType struct {
	XMLName xml.Name `xml:"urn:ieee:std:2030.5:ns ReadingType"`
	Resource
	MRID                  string `xml:"mRID,omitempty"`
	AccumulationBehaviour *uint8 `xml:"accumulationBehaviour,omitempty"`
	Commodity             *uint8 `xml:"commodity,omitempty"`
	DataQualifier         *uint8 `xml:"dataQualifier,omitempty"`
	FlowDirection         *uint8 `xml:"flowDirection,omitempty"`
	Kind                  *uint8 `xml:"kind,omitempty"`
	Phase                 *uint8 `xml:"phase,omitempty"`
	PowerOfTenMultiplier  *int8  `xml:"powerOfTenMultiplier,omitempty"`
	Uom                   *uint8 `xml:"uom,omitempty"`
}

// Copy returns an independent copy.
func (rt ReadingType) Copy() ReadingType {
	c := rt
	if rt.AccumulationBehaviour != nil {
		v := *rt.AccumulationBehaviour
		c.AccumulationBehaviour = &v
	}
	if rt.Commodity != nil {
		v := *rt.Commodity
		c.Commodity = &v
	}
	if rt.DataQualifier != nil {
		v := *rt.DataQualifier
		c.DataQualifier = &v
	}
	if rt.FlowDirection != nil {
		v := *rt.FlowDirection
		c.FlowDirection = &v
	}
	if rt.Kind != nil {
		v := *rt.Kind
		c.Kind = &v
	}
	if rt.Phase != nil {
		v := *rt.Phase
		c.Phase = &v
	}
	if rt.PowerOfTenMultiplier != nil {
		v := *rt.PowerOfTenMultiplier
		c.PowerOfTenMultiplier = &v
	}
	if rt.Uom != nil {
		v := *rt.Uom
		c.Uom = &v
	}
	return c
}

// ReadingTypeList is a list of ReadingType resources.
type ReadingTypeList struct {
	XMLName xml.Name `xml:"urn:ieee:std:2030.5:ns ReadingTypeList"`
	ListResource
	ReadingType []ReadingType `xml:"ReadingType,omitempty"`
}

// Common UOM (Unit of Measure) constants per spec.
const (
	UomWatts     uint8 = 38
	UomWattHours uint8 = 72
	UomVoltAmps  uint8 = 61
	UomVars      uint8 = 63
	UomVolts     uint8 = 29
	UomAmps      uint8 = 5
)

// FlowDirection constants.
const (
	FlowDirectionForward uint8 = 1  // delivered to customer
	FlowDirectionReverse uint8 = 19 // received from customer
)
