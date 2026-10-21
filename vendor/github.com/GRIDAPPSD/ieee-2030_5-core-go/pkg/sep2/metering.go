package sep2

import "encoding/xml"

// UsagePoint represents a physical metering point.
// Spec reference: section 10.4
type UsagePoint struct {
	XMLName xml.Name `xml:"urn:ieee:std:2030.5:ns UsagePoint"`
	SubscribableResource
	MRID                string         `xml:"mRID,omitempty"`
	Description         string         `xml:"description,omitempty"`
	RoleFlags           RoleFlagsValue `xml:"roleFlags,omitempty"`
	ServiceCategoryKind uint8          `xml:"serviceCategoryKind"`
	Status              uint8          `xml:"status"`

	MeterReadingListLink *ListLink `xml:"MeterReadingListLink,omitempty"`
}

// Copy returns an independent copy.
func (u UsagePoint) Copy() UsagePoint {
	c := u
	c.SubscribableResource = u.SubscribableResource.Copy()
	c.MeterReadingListLink = clonePtr(u.MeterReadingListLink)
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
	c.SubscribableResource = m.SubscribableResource.Copy()
	c.ReadingTypeLink = clonePtr(m.ReadingTypeLink)
	c.ReadingLink = clonePtr(m.ReadingLink)
	c.ReadingSetListLink = clonePtr(m.ReadingSetListLink)
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
//
// Field order matches the sep.xsd ReadingBase sequence (sep.xsd:6511):
// consumptionBlock, qualityFlags, timePeriod, touTier, value, followed by
// Reading's own localID (sep.xsd:1996-2016). This struct implements
// qualityFlags (position 2), timePeriod (position 3), value (position 5),
// and localID (own position, after value); their declaration order below
// preserves that relative sequence for a strict schema-validating client,
// even though the unimplemented optional fields are omitted.
//
// LocalID is minOccurs=0: it identifies a reading's position within a
// MirrorReadingSet (assigned in creation-time order) and has no meaning
// for a standalone Reading, so a pointer with omitempty is correct rather
// than a required-field hazard.
type Reading struct {
	XMLName xml.Name `xml:"urn:ieee:std:2030.5:ns Reading"`
	Resource
	QualityFlags *HexBinary16      `xml:"qualityFlags,omitempty"`
	TimePeriod   *DateTimeInterval `xml:"timePeriod,omitempty"`
	Value        *int64            `xml:"value,omitempty"`
	LocalID      *HexBinary16      `xml:"localID,omitempty"`
}

// Copy returns an independent copy.
func (r Reading) Copy() Reading {
	c := r
	c.Value = clonePtr(r.Value)
	c.TimePeriod = clonePtr(r.TimePeriod)
	c.QualityFlags = clonePtr(r.QualityFlags)
	c.LocalID = clonePtr(r.LocalID)
	return c
}

// ReadingList is a list of Reading resources.
type ReadingList struct {
	XMLName xml.Name `xml:"urn:ieee:std:2030.5:ns ReadingList"`
	ListResource
	Reading []Reading `xml:"Reading,omitempty"`
}

// ReadingType describes the unit and semantics of a meter reading.
//
// ReadingType is <xs:extension base="Resource"/> in sep.xsd (:2053-2150);
// Resource contributes only the href attribute, no elements. There is no
// mRID element in this type's sequence: a full scan of the complexType body
// confirms zero occurrences, and the EPRI client's generated schema table
// (se_schema.c, ReadingType (330)) agrees, listing href then
// accumulationBehaviour as the first content. A prior version of this
// struct emitted mRID as the first child element, which the EPRI client's
// strict schema-validating parser rejected outright because no such slot
// exists in its table.
//
// The implemented fields below are a subset of the 17-element canonical
// sequence (accumulationBehaviour, calorificValue, commodity,
// conversionFactor, dataQualifier, flowDirection, intervalLength, kind,
// maxNumberOfIntervals, numberOfConsumptionBlocks, numberOfTouTiers, phase,
// powerOfTenMultiplier, subIntervalLength, supplyLimit,
// tieredConsumptionBlocks, uom); their relative declaration order below
// already matches their relative canonical positions (1, 3, 5, 6, 7, 8, 12,
// 13, 17), so omitting the unimplemented optional fields does not disturb
// order for a strict sequence-validating client.
type ReadingType struct {
	XMLName xml.Name `xml:"urn:ieee:std:2030.5:ns ReadingType"`
	Resource
	AccumulationBehaviour *uint8  `xml:"accumulationBehaviour,omitempty"`
	Commodity             *uint8  `xml:"commodity,omitempty"`
	DataQualifier         *uint8  `xml:"dataQualifier,omitempty"`
	FlowDirection         *uint8  `xml:"flowDirection,omitempty"`
	IntervalLength        *uint32 `xml:"intervalLength,omitempty"`
	Kind                  *uint8  `xml:"kind,omitempty"`
	Phase                 *uint8  `xml:"phase,omitempty"`
	PowerOfTenMultiplier  *int8   `xml:"powerOfTenMultiplier,omitempty"`
	Uom                   *uint8  `xml:"uom,omitempty"`
}

// Copy returns an independent copy.
func (rt ReadingType) Copy() ReadingType {
	c := rt
	c.AccumulationBehaviour = clonePtr(rt.AccumulationBehaviour)
	c.Commodity = clonePtr(rt.Commodity)
	c.DataQualifier = clonePtr(rt.DataQualifier)
	c.FlowDirection = clonePtr(rt.FlowDirection)
	c.IntervalLength = clonePtr(rt.IntervalLength)
	c.Kind = clonePtr(rt.Kind)
	c.Phase = clonePtr(rt.Phase)
	c.PowerOfTenMultiplier = clonePtr(rt.PowerOfTenMultiplier)
	c.Uom = clonePtr(rt.Uom)
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
