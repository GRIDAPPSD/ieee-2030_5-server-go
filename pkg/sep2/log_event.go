package sep2

import "encoding/xml"

// LogEvent represents a device-reported event.
// Spec reference: section 9.6
// Element order matches 2023 XSD.
type LogEvent struct {
	XMLName xml.Name `xml:"urn:ieee:std:2030.5:ns LogEvent"`
	Resource

	CreatedDateTime int64  `xml:"createdDateTime"`
	Details         string `xml:"details,omitempty"` // 2023: NEW
	ExtendedData    *int64 `xml:"extendedData,omitempty"`
	FunctionSet     uint8  `xml:"functionSet"` // FunctionSetID enum
	LogEventCode    uint8  `xml:"logEventCode"`
	LogEventID      uint16 `xml:"logEventID"`
	LogEventPEN     uint32 `xml:"logEventPEN"`
	ProfileID       uint8  `xml:"profileID"`
}

// Copy returns an independent copy.
func (l LogEvent) Copy() LogEvent {
	c := l
	if l.ExtendedData != nil {
		v := *l.ExtendedData
		c.ExtendedData = &v
	}
	return c
}

// LogEventList is a list of LogEvent resources.
type LogEventList struct {
	XMLName xml.Name `xml:"urn:ieee:std:2030.5:ns LogEventList"`
	ListResource
	LogEvent []LogEvent `xml:"LogEvent,omitempty"`
}

// FunctionSet ID constants per spec.
const (
	FunctionSetDeviceCapability  uint8 = 0
	FunctionSetSelfDevice        uint8 = 1
	FunctionSetEndDevice         uint8 = 2
	FunctionSetFSA               uint8 = 3
	FunctionSetSubscription      uint8 = 4
	FunctionSetResponse          uint8 = 5
	FunctionSetTime              uint8 = 6
	FunctionSetDeviceInformation uint8 = 7
	FunctionSetPowerStatus       uint8 = 8
	FunctionSetNetworkStatus     uint8 = 9
	FunctionSetLogEvent          uint8 = 10
	FunctionSetConfiguration     uint8 = 11
	FunctionSetFile              uint8 = 12
	FunctionSetDRLC              uint8 = 13
	FunctionSetMetering          uint8 = 14
	FunctionSetPricing           uint8 = 15
	FunctionSetMessaging         uint8 = 16
	FunctionSetBilling           uint8 = 17
	FunctionSetPrepayment        uint8 = 18
	FunctionSetDER               uint8 = 19
	FunctionSetFlowReservation   uint8 = 20 // 2023: NEW
	FunctionSetMeteringMirror    uint8 = 21 // 2023: NEW
)
