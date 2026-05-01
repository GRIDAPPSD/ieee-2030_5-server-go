package sep2_test

import (
	"encoding/xml"
	"strings"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-go/pkg/sep2"
)

func TestDeviceCapabilityMarshalXML(t *testing.T) {
	dcap := sep2.DeviceCapability{
		Resource: sep2.Resource{Href: "/dcap"},
		PollRate: 900,
		TimeLink: &sep2.Link{Href: "/tm"},
		EndDeviceListLink: &sep2.ListLink{
			Href: "/edev",
			All:  5,
		},
		SelfDeviceLink: &sep2.Link{Href: "/sdev"},
	}

	data, err := xml.Marshal(&dcap)
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}

	xmlStr := string(data)

	if !strings.Contains(xmlStr, "urn:ieee:std:2030.5:ns") {
		t.Error("missing IEEE 2030.5 namespace")
	}
	if !strings.Contains(xmlStr, "DeviceCapability") {
		t.Error("missing DeviceCapability element")
	}
	if !strings.Contains(xmlStr, `href="/dcap"`) {
		t.Error("missing href attribute")
	}
	if !strings.Contains(xmlStr, `pollRate="900"`) {
		t.Error("missing pollRate attribute")
	}
	if !strings.Contains(xmlStr, `<TimeLink href="/tm"`) {
		t.Error("missing TimeLink element")
	}
	if !strings.Contains(xmlStr, `<EndDeviceListLink href="/edev"`) {
		t.Error("missing EndDeviceListLink element")
	}
	if !strings.Contains(xmlStr, `<SelfDeviceLink href="/sdev"`) {
		t.Error("missing SelfDeviceLink element")
	}
}

func TestDeviceCapabilityOmitsEmptyLinks(t *testing.T) {
	dcap := sep2.DeviceCapability{
		Resource: sep2.Resource{Href: "/dcap"},
		PollRate: 900,
		TimeLink: &sep2.Link{Href: "/tm"},
	}

	data, err := xml.Marshal(&dcap)
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}

	xmlStr := string(data)

	if strings.Contains(xmlStr, "EndDeviceListLink") {
		t.Error("should omit nil EndDeviceListLink")
	}
	if strings.Contains(xmlStr, "DERProgramListLink") {
		t.Error("should omit nil DERProgramListLink")
	}
}

func TestDeviceCapabilityRoundTrip(t *testing.T) {
	original := sep2.DeviceCapability{
		Resource: sep2.Resource{Href: "/dcap"},
		PollRate: 900,
		TimeLink: &sep2.Link{Href: "/tm"},
		EndDeviceListLink: &sep2.ListLink{
			Href: "/edev",
			All:  3,
		},
	}

	data, err := xml.Marshal(&original)
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}

	var parsed sep2.DeviceCapability
	if err := xml.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	if parsed.Href != "/dcap" {
		t.Errorf("Href = %q, want %q", parsed.Href, "/dcap")
	}
	if parsed.PollRate != 900 {
		t.Errorf("PollRate = %d, want %d", parsed.PollRate, 900)
	}
	if parsed.TimeLink == nil || parsed.TimeLink.Href != "/tm" {
		t.Error("TimeLink not preserved in round-trip")
	}
	if parsed.EndDeviceListLink == nil || parsed.EndDeviceListLink.Href != "/edev" {
		t.Error("EndDeviceListLink not preserved in round-trip")
	}
}

func TestTimeMarshalXML(t *testing.T) {
	tm := sep2.Time{
		Resource:     sep2.Resource{Href: "/tm"},
		CurrentTime:  1604963587,
		DstEndTime:   1583661600,
		DstOffset:    3600,
		DstStartTime: 1583661600,
		Quality:      sep2.TimeQualityIntentionallyUncoordinated,
		TzOffset:     -28800,
	}

	data, err := xml.Marshal(&tm)
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}

	xmlStr := string(data)

	if !strings.Contains(xmlStr, "urn:ieee:std:2030.5:ns") {
		t.Error("missing IEEE 2030.5 namespace")
	}
	if !strings.Contains(xmlStr, "<currentTime>1604963587</currentTime>") {
		t.Error("missing or wrong currentTime element")
	}
	if !strings.Contains(xmlStr, "<quality>7</quality>") {
		t.Error("missing or wrong quality element")
	}
	if !strings.Contains(xmlStr, "<tzOffset>-28800</tzOffset>") {
		t.Error("missing or wrong tzOffset element")
	}
}

func TestTimeRoundTrip(t *testing.T) {
	original := sep2.Time{
		Resource:     sep2.Resource{Href: "/tm"},
		CurrentTime:  1604963587,
		DstEndTime:   1583661600,
		DstOffset:    3600,
		DstStartTime: 1583661600,
		Quality:      sep2.TimeQualityNTP,
		TzOffset:     -28800,
	}

	data, err := xml.Marshal(&original)
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}

	var parsed sep2.Time
	if err := xml.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	if parsed.CurrentTime != 1604963587 {
		t.Errorf("CurrentTime = %d, want %d", parsed.CurrentTime, 1604963587)
	}
	if parsed.Quality != sep2.TimeQualityNTP {
		t.Errorf("Quality = %d, want %d", parsed.Quality, sep2.TimeQualityNTP)
	}
	if parsed.TzOffset != -28800 {
		t.Errorf("TzOffset = %d, want %d", parsed.TzOffset, -28800)
	}
}

func TestTimeMatchesSpecExample(t *testing.T) {
	// From the Common Metering Profile spec, Figure 2
	tm := sep2.Time{
		Resource:     sep2.Resource{Href: "/tm"},
		CurrentTime:  1604963587,
		DstEndTime:   1583661600,
		DstOffset:    3600,
		DstStartTime: 1583661600,
		Quality:      7,
		TzOffset:     -28800,
	}

	data, err := xml.Marshal(&tm)
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}

	xmlStr := string(data)

	// Verify the XML matches the spec example structure
	if !strings.Contains(xmlStr, `<Time xmlns="urn:ieee:std:2030.5:ns"`) {
		t.Error("root element should be <Time> with correct namespace")
	}
}
