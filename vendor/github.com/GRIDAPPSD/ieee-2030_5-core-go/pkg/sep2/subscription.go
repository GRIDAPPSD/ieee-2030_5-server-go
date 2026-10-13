package sep2

import "encoding/xml"

// Subscription allows a client to receive notifications when a resource changes.
// Spec reference: section 8.7
type Subscription struct {
	XMLName xml.Name `xml:"urn:ieee:std:2030.5:ns Subscription"`
	SubscribableResource
	SubscribedResource string     `xml:"subscribedResource"`
	NotificationURI    string     `xml:"notificationURI"`
	Encoding           uint8      `xml:"encoding"`
	Limit              uint32     `xml:"limit,omitempty"`
	Condition          *Condition `xml:"Condition,omitempty"`
}

// Copy returns an independent copy.
func (s Subscription) Copy() Subscription {
	c := s
	if s.Subscribable != nil {
		v := *s.Subscribable
		c.Subscribable = &v
	}
	if s.Condition != nil {
		cond := *s.Condition
		c.Condition = &cond
	}
	return c
}

// Condition specifies thresholds for conditional notifications.
type Condition struct {
	AttributeIdentifier uint8 `xml:"attributeIdentifier"`
	LowerThreshold      int64 `xml:"lowerThreshold"`
	UpperThreshold      int64 `xml:"upperThreshold"`
}

// SubscriptionList is a list of Subscription resources.
type SubscriptionList struct {
	XMLName xml.Name `xml:"urn:ieee:std:2030.5:ns SubscriptionList"`
	ListResource
	Subscription []Subscription `xml:"Subscription,omitempty"`
}

// Notification is pushed to a subscriber when a resource changes.
type Notification struct {
	XMLName xml.Name `xml:"urn:ieee:std:2030.5:ns Notification"`
	Resource
	SubscribedResource string `xml:"subscribedResource"`
	SubscriptionURI    string `xml:"subscriptionURI"`
	Status             uint8  `xml:"status"`
	NewResourceURI     string `xml:"newResourceURI,omitempty"`
}

// Copy returns an independent copy.
func (n Notification) Copy() Notification {
	return n
}

// NotificationList is a list of Notification resources.
type NotificationList struct {
	XMLName xml.Name `xml:"urn:ieee:std:2030.5:ns NotificationList"`
	ListResource
	Notification []Notification `xml:"Notification,omitempty"`
}

// Notification status values per IEEE 2030.5. Values above 4 are reserved.
const (
	NotificationStatusDefault                   uint8 = 0
	NotificationStatusCanceledNoInfo            uint8 = 1
	NotificationStatusCanceledMoved             uint8 = 2
	NotificationStatusCanceledDefinitionChanged uint8 = 3
	NotificationStatusCanceledDeleted           uint8 = 4
)

// Pre-standard names kept so existing callers still compile. Each value is a
// subscription-cancelled code in the standard, not the meaning its name implies.
const (
	// Deprecated: value 1 means "subscription canceled, no additional
	// information"; use NotificationStatusCanceledNoInfo.
	NotificationStatusSubscripted uint8 = 1
	// Deprecated: value 2 means "subscription canceled, resource moved"; use
	// NotificationStatusCanceledMoved. It is not a change notification.
	NotificationStatusChanged uint8 = 2
	// Deprecated: value 3 means "subscription canceled, resource definition
	// changed"; use NotificationStatusCanceledDefinitionChanged.
	NotificationStatusRemoved uint8 = 3
)

// Encoding values per spec.
const (
	EncodingXML uint8 = 0
	EncodingEXI uint8 = 1
)
