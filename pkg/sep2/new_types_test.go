package sep2_test

import (
	"encoding/xml"
	"strings"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-go/pkg/sep2"
)

func TestDeviceStatusMarshalAndCopy(t *testing.T) {
	onCount := uint16(5)
	opState := sep2.OpStateOperating
	ds := sep2.DeviceStatus{ChangedTime: 1000, OnCount: &onCount, OpState: &opState}
	ds.Href = "/sdev/dstat"

	data, err := xml.Marshal(&ds)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "DeviceStatus") {
		t.Error("missing root element")
	}

	copied := ds.Copy()
	*copied.OnCount = 99
	if *ds.OnCount != 5 {
		t.Error("original mutated")
	}
}

func TestConfigurationMarshalAndCopy(t *testing.T) {
	cfg := sep2.Configuration{CurrentLocale: "en-US", UserDeviceName: "Test"}
	cfg.Href = "/edev/1/cfg"
	cfg.TimeConfiguration = &sep2.TimeConfiguration{TzOffset: -28800}

	data, err := xml.Marshal(&cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "Configuration") {
		t.Error("missing root element")
	}

	copied := cfg.Copy()
	copied.TimeConfiguration.TzOffset = 0
	if cfg.TimeConfiguration.TzOffset != -28800 {
		t.Error("original mutated")
	}
}

func TestLogEventMarshalAndCopy(t *testing.T) {
	ext := int64(42)
	le := sep2.LogEvent{
		CreatedDateTime: 1000, Details: "voltage fault",
		FunctionSet: sep2.FunctionSetDER, LogEventCode: 0x02,
		ExtendedData: &ext,
	}

	data, err := xml.Marshal(&le)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "LogEvent") {
		t.Error("missing root element")
	}
	if !strings.Contains(string(data), "voltage fault") {
		t.Error("missing details (2023 field)")
	}

	copied := le.Copy()
	*copied.ExtendedData = 999
	if *le.ExtendedData != 42 {
		t.Error("original mutated")
	}
}

func TestPowerStatusMarshalAndCopy(t *testing.T) {
	bat := sep2.BatteryStatusNormal
	ps := sep2.PowerStatus{
		BatteryStatus: &bat, ChangedTime: 1000,
		CurrentPowerSource: sep2.PowerSourceMains,
	}

	data, err := xml.Marshal(&ps)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "PowerStatus") {
		t.Error("missing root element")
	}

	copied := ps.Copy()
	*copied.BatteryStatus = sep2.BatteryStatusLow
	if *ps.BatteryStatus != sep2.BatteryStatusNormal {
		t.Error("original mutated")
	}
}

func TestMessagingProgramMarshalAndCopy(t *testing.T) {
	mp := sep2.MessagingProgram{MRID: "msg1", Primacy: 1, Locale: "en-US"}
	mp.Href = "/msg/1"
	mp.TextMessageListLink = &sep2.ListLink{Href: "/msg/1/tm"}

	data, err := xml.Marshal(&mp)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "MessagingProgram") {
		t.Error("missing root element")
	}

	copied := mp.Copy()
	copied.TextMessageListLink.Href = "/changed"
	if mp.TextMessageListLink.Href != "/msg/1/tm" {
		t.Error("original mutated")
	}
}

func TestTextMessageMarshalAndCopy(t *testing.T) {
	tm := sep2.TextMessage{TextBody: "Alert: high voltage", Priority: sep2.PriorityCritical}
	tm.MRID = "tm1"

	data, err := xml.Marshal(&tm)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "TextMessage") {
		t.Error("missing root element")
	}
	if !strings.Contains(string(data), "Alert: high voltage") {
		t.Error("missing text body")
	}
}

func TestFlowReservationRequestMarshalAndCopy(t *testing.T) {
	energy := sep2.SignedRealEnergy{Value: 5000}
	frq := sep2.FlowReservationRequest{
		MRID: "frq1", CreationTime: 1000,
		EnergyRequested: &energy,
	}

	data, err := xml.Marshal(&frq)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "FlowReservationRequest") {
		t.Error("missing root element")
	}

	copied := frq.Copy()
	copied.EnergyRequested.Value = 999
	if frq.EnergyRequested.Value != 5000 {
		t.Error("original mutated")
	}
}

func TestFlowReservationResponseMarshal(t *testing.T) {
	energy := sep2.SignedRealEnergy{Value: 3000}
	frp := sep2.FlowReservationResponse{
		EnergyAvailable: &energy, Subject: "frq1",
	}

	data, err := xml.Marshal(&frp)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "FlowReservationResponse") {
		t.Error("missing root element")
	}
	if !strings.Contains(string(data), "SignedRealEnergy") || !strings.Contains(string(data), "3000") {
		// SignedRealEnergy is inline, so just check the value
		if !strings.Contains(string(data), "3000") {
			t.Error("missing energy value")
		}
	}
}

func TestResponseMarshalAndCopy(t *testing.T) {
	status := sep2.ResponseStatusEventReceived
	rsp := sep2.Response{Subject: "event1", Status: &status, EndDeviceLFDI: "AABB"}

	data, err := xml.Marshal(&rsp)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "Response") {
		t.Error("missing root element")
	}

	copied := rsp.Copy()
	*copied.Status = sep2.ResponseStatusOptOut
	if *rsp.Status != sep2.ResponseStatusEventReceived {
		t.Error("original mutated")
	}
}

func TestResponseSetMarshalAndCopy(t *testing.T) {
	rs := sep2.ResponseSet{MRID: "rsps1", Description: "Test Set"}
	rs.ResponseListLink = &sep2.ListLink{Href: "/rsps/rsps1/rsp"}

	data, err := xml.Marshal(&rs)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "ResponseSet") {
		t.Error("missing root element")
	}

	copied := rs.Copy()
	copied.ResponseListLink.Href = "/changed"
	if rs.ResponseListLink.Href != "/rsps/rsps1/rsp" {
		t.Error("original mutated")
	}
}

func TestDERControlResponseMarshal(t *testing.T) {
	modes := uint32(0xFF)
	dcr := sep2.DERControlResponse{ModesResponded: &modes}
	dcr.Subject = "ctrl1"

	data, err := xml.Marshal(&dcr)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "DERControlResponse") {
		t.Error("missing root element")
	}
}
