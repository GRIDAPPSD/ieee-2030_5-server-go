package sep2

import "encoding/xml"

// MessagingProgram represents a messaging program.
// Spec reference: section 10.6
// Element order matches 2023 XSD.
type MessagingProgram struct {
	XMLName xml.Name `xml:"urn:ieee:std:2030.5:ns MessagingProgram"`
	SubscribableResource
	MRID        string  `xml:"mRID,omitempty"`
	Description string  `xml:"description,omitempty"`
	Version     *uint16 `xml:"version,omitempty"`

	// XSD element order:
	ActiveTextMessageListLink *ListLink `xml:"ActiveTextMessageListLink,omitempty"`
	Locale                    string    `xml:"locale,omitempty"` // RFC 5646
	Primacy                   uint8     `xml:"primacy"`
	TextMessageListLink       *ListLink `xml:"TextMessageListLink,omitempty"`
}

// Copy returns an independent copy.
func (m MessagingProgram) Copy() MessagingProgram {
	c := m
	c.SubscribableResource = m.SubscribableResource.Copy()
	c.Version = clonePtr(m.Version)
	c.ActiveTextMessageListLink = clonePtr(m.ActiveTextMessageListLink)
	c.TextMessageListLink = clonePtr(m.TextMessageListLink)
	return c
}

// MessagingProgramList is a list of MessagingProgram resources.
type MessagingProgramList struct {
	XMLName xml.Name `xml:"urn:ieee:std:2030.5:ns MessagingProgramList"`
	ListResource
	MessagingProgram []MessagingProgram `xml:"MessagingProgram,omitempty"`
}

// TextMessage is a text message event.
// Extends Event, not RandomizableEvent: sep.xsd derives it from Event
// directly, so it carries no randomizeStart/randomizeDuration (#107).
type TextMessage struct {
	XMLName xml.Name `xml:"urn:ieee:std:2030.5:ns TextMessage"`
	Event

	// XSD element order:
	Originator string `xml:"originator,omitempty"`
	Priority   uint8  `xml:"priority"`
	TextBody   string `xml:"textMessage"` // the actual message text
}

// Copy returns an independent copy.
func (t TextMessage) Copy() TextMessage {
	c := t
	c.Event = t.Event.Copy()
	return c
}

// TextMessageList is a list of TextMessage resources.
type TextMessageList struct {
	XMLName xml.Name `xml:"urn:ieee:std:2030.5:ns TextMessageList"`
	ListResource
	TextMessage []TextMessage `xml:"TextMessage,omitempty"`
}

// Priority constants per spec.
const (
	PriorityLow      uint8 = 0
	PriorityNormal   uint8 = 1
	PriorityHigh     uint8 = 2
	PriorityCritical uint8 = 3
)
