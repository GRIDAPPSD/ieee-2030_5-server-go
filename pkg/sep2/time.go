package sep2

import "encoding/xml"

// Time represents the server's current time and timezone information.
// Spec reference: section 9.2
type Time struct {
	XMLName      xml.Name `xml:"urn:ieee:std:2030.5:ns Time"`
	Resource
	CurrentTime  int64  `xml:"currentTime"`
	DstEndTime   int64  `xml:"dstEndTime"`
	DstOffset    int32  `xml:"dstOffset"`
	DstStartTime int64  `xml:"dstStartTime"`
	LocalTime    *int64 `xml:"localTime,omitempty"`
	Quality      uint8  `xml:"quality"`
	TzOffset     int32  `xml:"tzOffset"`
}

// TimeQuality constants per spec section 9.2.
const (
	TimeQualityGPS                      uint8 = 3
	TimeQualityNTP                      uint8 = 4
	TimeQualityNoTimeSource             uint8 = 5
	TimeQualityLevel6                   uint8 = 6
	TimeQualityIntentionallyUncoordinated uint8 = 7
)
