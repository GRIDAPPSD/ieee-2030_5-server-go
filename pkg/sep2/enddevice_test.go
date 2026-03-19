package sep2_test

import (
	"encoding/xml"
	"strings"
	"testing"

	"github.com/craig8/ieee-2030_5-go/pkg/sep2"
)

func TestEndDeviceMarshalXML(t *testing.T) {
	enabled := true
	dev := sep2.EndDevice{
		SubscribableResource: sep2.SubscribableResource{
			Resource: sep2.Resource{Href: "/edev/1"},
		},
		ChangedTime:      1604963587,
		Enabled:          &enabled,
		SFDI:             "167261211391",
		LFDI:             "3E4F45AB31EDFE5B67E343E5E4562E31984E23E5",
		RegistrationLink: &sep2.Link{Href: "/edev/1/rg"},
	}

	data, err := xml.Marshal(&dev)
	if err != nil {
		t.Fatal(err)
	}
	xmlStr := string(data)

	if !strings.Contains(xmlStr, "urn:ieee:std:2030.5:ns") {
		t.Error("missing namespace")
	}
	if !strings.Contains(xmlStr, "<sFDI>167261211391</sFDI>") {
		t.Error("missing sFDI element")
	}
	if !strings.Contains(xmlStr, "<lFDI>3E4F45AB31EDFE5B67E343E5E4562E31984E23E5</lFDI>") {
		t.Error("missing lFDI element")
	}
	if !strings.Contains(xmlStr, "<enabled>true</enabled>") {
		t.Error("missing enabled element")
	}
}

func TestEndDeviceRoundTrip(t *testing.T) {
	enabled := true
	original := sep2.EndDevice{
		SubscribableResource: sep2.SubscribableResource{
			Resource: sep2.Resource{Href: "/edev/1"},
		},
		ChangedTime: 1604963587,
		Enabled:     &enabled,
		SFDI:        "167261211391",
		LFDI:        "3E4F45AB31EDFE5B67E343E5E4562E31984E23E5",
	}

	data, err := xml.Marshal(&original)
	if err != nil {
		t.Fatal(err)
	}

	var parsed sep2.EndDevice
	if err := xml.Unmarshal(data, &parsed); err != nil {
		t.Fatal(err)
	}

	if parsed.SFDI != original.SFDI {
		t.Errorf("SFDI = %q, want %q", parsed.SFDI, original.SFDI)
	}
	if parsed.Enabled == nil || *parsed.Enabled != true {
		t.Error("Enabled not preserved")
	}
}

func TestEndDeviceListMarshalXML(t *testing.T) {
	list := sep2.EndDeviceList{
		ListResource: sep2.ListResource{
			SubscribableResource: sep2.SubscribableResource{
				Resource: sep2.Resource{Href: "/edev"},
			},
			All:     2,
			Results: 2,
		},
		EndDevice: []sep2.EndDevice{
			{SubscribableResource: sep2.SubscribableResource{Resource: sep2.Resource{Href: "/edev/1"}}, SFDI: "111"},
			{SubscribableResource: sep2.SubscribableResource{Resource: sep2.Resource{Href: "/edev/2"}}, SFDI: "222"},
		},
	}

	data, err := xml.Marshal(&list)
	if err != nil {
		t.Fatal(err)
	}
	xmlStr := string(data)

	if !strings.Contains(xmlStr, "EndDeviceList") {
		t.Error("missing EndDeviceList element")
	}
	if !strings.Contains(xmlStr, `all="2"`) {
		t.Error("missing all attribute")
	}
	if !strings.Contains(xmlStr, `results="2"`) {
		t.Error("missing results attribute")
	}
}

func TestRegistrationMarshalXML(t *testing.T) {
	reg := sep2.Registration{
		Resource:           sep2.Resource{Href: "/edev/1/rg"},
		DateTimeRegistered: 1604963587,
		PIN:                12345,
	}

	data, err := xml.Marshal(&reg)
	if err != nil {
		t.Fatal(err)
	}
	xmlStr := string(data)

	if !strings.Contains(xmlStr, "Registration") {
		t.Error("missing Registration element")
	}
	if !strings.Contains(xmlStr, "<pIN>12345</pIN>") {
		t.Error("missing pIN element")
	}
}

func TestEndDeviceCopy(t *testing.T) {
	enabled := true
	original := sep2.EndDevice{
		Enabled: &enabled,
		SFDI:    "123",
		RegistrationLink: &sep2.Link{Href: "/rg"},
	}

	copied := original.Copy()

	// Mutate copy
	*copied.Enabled = false
	copied.SFDI = "999"
	copied.RegistrationLink.Href = "/changed"

	// Original should be unchanged
	if *original.Enabled != true {
		t.Error("original Enabled was mutated")
	}
	if original.SFDI != "123" {
		t.Error("original SFDI was mutated")
	}
	if original.RegistrationLink.Href != "/rg" {
		t.Error("original RegistrationLink was mutated")
	}
}
