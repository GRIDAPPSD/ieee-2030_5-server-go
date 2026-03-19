package sep2_test

import (
	"encoding/xml"
	"strings"
	"testing"

	"github.com/craig8/ieee-2030_5-go/pkg/sep2"
)

func TestDERControlBaseCopy(t *testing.T) {
	connected := true
	maxW := sep2.ActivePower{Multiplier: 0, Value: 5000}
	original := sep2.DERControlBase{
		OpModConnect: &connected,
		OpModMaxLimW: &maxW,
	}

	copied := original.Copy()
	*copied.OpModConnect = false
	copied.OpModMaxLimW.Value = 999

	if *original.OpModConnect != true {
		t.Error("original OpModConnect mutated")
	}
	if original.OpModMaxLimW.Value != 5000 {
		t.Error("original OpModMaxLimW mutated")
	}
}

func TestDERControlMarshalXML(t *testing.T) {
	maxW := sep2.ActivePower{Value: 5000}
	ctrl := sep2.DERControl{
		DERControlBase: &sep2.DERControlBase{
			OpModMaxLimW: &maxW,
		},
	}
	ctrl.MRID = "CTRL001"
	ctrl.Href = "/derp/1/derc/1"

	data, err := xml.Marshal(&ctrl)
	if err != nil {
		t.Fatal(err)
	}
	xmlStr := string(data)

	if !strings.Contains(xmlStr, "DERControl") {
		t.Error("missing DERControl element")
	}
	if !strings.Contains(xmlStr, "urn:ieee:std:2030.5:ns") {
		t.Error("missing namespace")
	}
	if !strings.Contains(xmlStr, "<opModMaxLimW>") {
		t.Error("missing opModMaxLimW")
	}
}

func TestDefaultDERControlRoundTrip(t *testing.T) {
	connected := true
	dderc := sep2.DefaultDERControl{
		SubscribableResource: sep2.SubscribableResource{
			Resource: sep2.Resource{Href: "/derp/1/dderc"},
		},
		DERControlBase: &sep2.DERControlBase{
			OpModConnect: &connected,
		},
	}

	data, err := xml.Marshal(&dderc)
	if err != nil {
		t.Fatal(err)
	}

	var parsed sep2.DefaultDERControl
	if err := xml.Unmarshal(data, &parsed); err != nil {
		t.Fatal(err)
	}

	if parsed.DERControlBase == nil || parsed.DERControlBase.OpModConnect == nil {
		t.Fatal("DERControlBase.OpModConnect should be preserved")
	}
	if *parsed.DERControlBase.OpModConnect != true {
		t.Error("OpModConnect should be true")
	}
}

func TestDERProgramMarshal(t *testing.T) {
	prog := sep2.DERProgram{
		SubscribableResource: sep2.SubscribableResource{
			Resource: sep2.Resource{Href: "/derp/1"},
		},
		MRID:        "PROG001",
		Description: "Solar DER Program",
		Primacy:     10,
		DefaultDERControlLink: &sep2.Link{Href: "/derp/1/dderc"},
		DERControlListLink:    &sep2.ListLink{Href: "/derp/1/derc"},
	}

	data, err := xml.Marshal(&prog)
	if err != nil {
		t.Fatal(err)
	}
	xmlStr := string(data)

	if !strings.Contains(xmlStr, "DERProgram") {
		t.Error("missing DERProgram element")
	}
	if !strings.Contains(xmlStr, "<primacy>10</primacy>") {
		t.Error("missing primacy")
	}
}

func TestDERCurveCopy(t *testing.T) {
	original := sep2.DERCurve{
		CurveType: sep2.CurveTypeOpModVoltVar,
		CurveData: []sep2.CurveData{
			{XValue: 9200, YValue: 100},
			{XValue: 9800, YValue: 0},
			{XValue: 10200, YValue: 0},
			{XValue: 10800, YValue: -100},
		},
	}

	copied := original.Copy()
	copied.CurveData[0].YValue = 999

	if original.CurveData[0].YValue != 100 {
		t.Error("original CurveData mutated")
	}
}

func TestDERStatusMarshal(t *testing.T) {
	status := sep2.DERStatus{
		SubscribableResource: sep2.SubscribableResource{
			Resource: sep2.Resource{Href: "/edev/1/der/1/ders"},
		},
		GenConnectStatus: &sep2.ConnectStatusType{DateTime: 1604963587, Value: 1},
		ReadingTime:      1604963587,
	}

	data, err := xml.Marshal(&status)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "DERStatus") {
		t.Error("missing DERStatus element")
	}
}

func TestDRLCMarshal(t *testing.T) {
	drp := sep2.DemandResponseProgram{
		SubscribableResource: sep2.SubscribableResource{
			Resource: sep2.Resource{Href: "/drp/1"},
		},
		MRID:    "DRP001",
		Primacy: 5,
	}

	data, err := xml.Marshal(&drp)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "DemandResponseProgram") {
		t.Error("missing DemandResponseProgram element")
	}
}
