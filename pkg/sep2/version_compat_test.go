package sep2_test

import (
	"encoding/xml"
	"strings"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-go/pkg/sep2"
)

// Test that 2023-only fields are present when set and omitted when empty.
// This ensures backward compatibility — 2013/2018 clients won't see unknown elements.

func TestDeviceInformation2023Fields(t *testing.T) {
	// With 2023 fields set
	funcs := uint64(0xFF)
	di := sep2.DeviceInformation{
		ConnectionPointID:    "NMI-12345", // 2023 addition
		FunctionsImplemented: &funcs,
		LFDI:                 "AABB",
		MfModel:              "TestModel",
	}

	data, _ := xml.Marshal(&di)
	xmlStr := string(data)

	if !strings.Contains(xmlStr, "connectionPointID") {
		t.Error("2023 connectionPointID should be present when set")
	}
	if !strings.Contains(xmlStr, "NMI-12345") {
		t.Error("connectionPointID value should be present")
	}

	// Without 2023 fields — should be omitted (2013/2018 compatible)
	di2 := sep2.DeviceInformation{LFDI: "AABB", MfModel: "TestModel"}
	data2, _ := xml.Marshal(&di2)
	xmlStr2 := string(data2)

	if strings.Contains(xmlStr2, "connectionPointID") {
		t.Error("connectionPointID should be omitted when empty (2013/2018 compat)")
	}
}

func TestLogEvent2023DetailsField(t *testing.T) {
	// With 2023 details field
	le := sep2.LogEvent{
		CreatedDateTime: 1000,
		Details:         "voltage exceeded threshold", // 2023 addition
		FunctionSet:     sep2.FunctionSetDER,
		LogEventCode:    0x02,
	}

	data, _ := xml.Marshal(&le)
	xmlStr := string(data)

	if !strings.Contains(xmlStr, "<details>voltage exceeded threshold</details>") {
		t.Error("2023 details field should be present when set")
	}

	// Without details — 2013/2018 compatible
	le2 := sep2.LogEvent{CreatedDateTime: 1000, FunctionSet: sep2.FunctionSetDER}
	data2, _ := xml.Marshal(&le2)
	xmlStr2 := string(data2)

	if strings.Contains(xmlStr2, "details") {
		t.Error("details should be omitted when empty (2013/2018 compat)")
	}
}

func TestFlowReservation2023SignedRealEnergy(t *testing.T) {
	// 2023 changed energyAvailable from RealEnergy to SignedRealEnergy
	// Verify our type uses SignedRealEnergy (can be negative for storage discharge)
	energy := sep2.SignedRealEnergy{Multiplier: 0, Value: -5000} // negative = receiving
	frp := sep2.FlowReservationResponse{EnergyAvailable: &energy}

	data, _ := xml.Marshal(&frp)
	xmlStr := string(data)

	if !strings.Contains(xmlStr, "-5000") {
		t.Error("SignedRealEnergy should support negative values (2023 requirement)")
	}
}

func TestDERControlResponse2023Type(t *testing.T) {
	// DERControlResponse is a 2023 addition (doesn't exist in 2013)
	modes := uint32(0x0F)
	dcr := sep2.DERControlResponse{ModesResponded: &modes}
	dcr.Subject = "ctrl-001"
	status := sep2.ResponseStatusEventCompleted
	dcr.Status = &status

	data, _ := xml.Marshal(&dcr)
	xmlStr := string(data)

	if !strings.Contains(xmlStr, "DERControlResponse") {
		t.Error("DERControlResponse element should marshal (2023 type)")
	}
	if !strings.Contains(xmlStr, "modesResponded") {
		t.Error("modesResponded field should be present (2023 addition)")
	}
}

func TestNamespaceConstants(t *testing.T) {
	if sep2.Namespace != "urn:ieee:std:2030.5:ns" {
		t.Errorf("2018 namespace = %q", sep2.Namespace)
	}
	if sep2.Namespace2013 != "http://ieee.org/2030.5" {
		t.Errorf("2013 namespace = %q", sep2.Namespace2013)
	}
	if sep2.Namespace2023 != sep2.Namespace {
		t.Error("2023 namespace should equal 2018 namespace")
	}
}

func TestElementOrderMatchesXSD(t *testing.T) {
	// DeviceCapability elements must be in XSD order:
	// FunctionSetAssignmentsBase elements first, then DeviceCapability extensions
	dcap := sep2.DeviceCapability{
		Resource: sep2.Resource{Href: "/dcap"},
		PollRate: 900,
		// Base elements
		DERProgramListLink: &sep2.ListLink{Href: "/dc"},
		TimeLink:           &sep2.Link{Href: "/tm"},
		// Extension elements
		EndDeviceListLink:        &sep2.ListLink{Href: "/edev"},
		MirrorUsagePointListLink: &sep2.ListLink{Href: "/mup"},
		SelfDeviceLink:           &sep2.Link{Href: "/sdev"},
	}

	data, _ := xml.Marshal(&dcap)
	xmlStr := string(data)

	// DERProgramListLink (base) must come before EndDeviceListLink (extension)
	derIdx := strings.Index(xmlStr, "DERProgramListLink")
	edevIdx := strings.Index(xmlStr, "EndDeviceListLink")
	if derIdx > edevIdx {
		t.Error("XSD order violation: DERProgramListLink (base) must come before EndDeviceListLink (extension)")
	}

	// TimeLink (base) must come before SelfDeviceLink (extension)
	timeIdx := strings.Index(xmlStr, "TimeLink")
	selfIdx := strings.Index(xmlStr, "SelfDeviceLink")
	if timeIdx > selfIdx {
		t.Error("XSD order violation: TimeLink (base) must come before SelfDeviceLink (extension)")
	}
}

func TestLogEventFunctionSetConstants2023(t *testing.T) {
	// 2023 added new function set IDs (20+)
	if sep2.FunctionSetFlowReservation != 20 {
		t.Errorf("FlowReservation = %d, want 20 (2023 addition)", sep2.FunctionSetFlowReservation)
	}
	if sep2.FunctionSetMeteringMirror != 21 {
		t.Errorf("MeteringMirror = %d, want 21 (2023 addition)", sep2.FunctionSetMeteringMirror)
	}
}
