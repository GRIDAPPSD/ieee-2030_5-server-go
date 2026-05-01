package sep2_test

import (
	"encoding/xml"
	"strings"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-go/pkg/sep2"
)

func TestSubscriptionRoundTrip(t *testing.T) {
	sub := sep2.Subscription{
		SubscribableResource: sep2.SubscribableResource{
			Resource: sep2.Resource{Href: "/edev/1/sub/1"},
		},
		SubscribedResource: "/edev/1",
		NotificationURI:    "https://client:8443/notify",
		Encoding:           sep2.EncodingXML,
		Limit:              10,
	}

	data, err := xml.Marshal(&sub)
	if err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(string(data), "urn:ieee:std:2030.5:ns") {
		t.Error("missing namespace")
	}

	var parsed sep2.Subscription
	if err := xml.Unmarshal(data, &parsed); err != nil {
		t.Fatal(err)
	}
	if parsed.NotificationURI != "https://client:8443/notify" {
		t.Errorf("NotificationURI = %q", parsed.NotificationURI)
	}
}

func TestSubscriptionCopy(t *testing.T) {
	cond := &sep2.Condition{AttributeIdentifier: 1, LowerThreshold: 100}
	original := sep2.Subscription{
		NotificationURI: "https://a",
		Condition:       cond,
	}
	copied := original.Copy()
	copied.NotificationURI = "https://b"
	copied.Condition.LowerThreshold = 999

	if original.NotificationURI != "https://a" {
		t.Error("original URI mutated")
	}
	if original.Condition.LowerThreshold != 100 {
		t.Error("original Condition mutated")
	}
}

func TestNotificationMarshal(t *testing.T) {
	n := sep2.Notification{
		Resource:           sep2.Resource{Href: "/ntfy/1"},
		SubscribedResource: "/edev/1",
		SubscriptionURI:    "/edev/1/sub/1",
		Status:             sep2.NotificationStatusChanged,
	}

	data, err := xml.Marshal(&n)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "<status>2</status>") {
		t.Error("missing status element")
	}
}
