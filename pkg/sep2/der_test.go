package sep2_test

import (
	"encoding/xml"
	"strings"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-go/pkg/sep2"
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
		MRID:                  "PROG001",
		Description:           "Solar DER Program",
		Primacy:               10,
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

// TestDERControlBaseModeFieldsRoundTrip exercises the IEEE-092 mode
// fields (LVRT/HVRT/LFRT/HFRT curve refs, opModVoltWatt, opModFreqWatt)
// for XML marshal → unmarshal fidelity. Each subtest seeds exactly one
// field so a regression in the per-field tag or omit-if-nil behavior
// trips loudly. Curve-ref values are arbitrary non-zero int32s.
func TestDERControlBaseModeFieldsRoundTrip(t *testing.T) {
	type modeField struct {
		name    string
		element string
		set     func(*sep2.DERControlBase)
		get     func(*sep2.DERControlBase) (int32, bool)
	}
	cases := []modeField{
		{
			name:    "OpModLVRTMustTrip",
			element: "<opModLVRTMustTrip>",
			set: func(b *sep2.DERControlBase) {
				v := int32(11)
				b.OpModLVRTMustTrip = &v
			},
			get: func(b *sep2.DERControlBase) (int32, bool) {
				if b.OpModLVRTMustTrip == nil {
					return 0, false
				}
				return *b.OpModLVRTMustTrip, true
			},
		},
		{
			name:    "OpModLVRTMomentaryCessation",
			element: "<opModLVRTMomentaryCessation>",
			set: func(b *sep2.DERControlBase) {
				v := int32(12)
				b.OpModLVRTMomentaryCessation = &v
			},
			get: func(b *sep2.DERControlBase) (int32, bool) {
				if b.OpModLVRTMomentaryCessation == nil {
					return 0, false
				}
				return *b.OpModLVRTMomentaryCessation, true
			},
		},
		{
			name:    "OpModHVRTMustTrip",
			element: "<opModHVRTMustTrip>",
			set: func(b *sep2.DERControlBase) {
				v := int32(13)
				b.OpModHVRTMustTrip = &v
			},
			get: func(b *sep2.DERControlBase) (int32, bool) {
				if b.OpModHVRTMustTrip == nil {
					return 0, false
				}
				return *b.OpModHVRTMustTrip, true
			},
		},
		{
			name:    "OpModHVRTMomentaryCessation",
			element: "<opModHVRTMomentaryCessation>",
			set: func(b *sep2.DERControlBase) {
				v := int32(14)
				b.OpModHVRTMomentaryCessation = &v
			},
			get: func(b *sep2.DERControlBase) (int32, bool) {
				if b.OpModHVRTMomentaryCessation == nil {
					return 0, false
				}
				return *b.OpModHVRTMomentaryCessation, true
			},
		},
		{
			name:    "OpModLFRTMustTrip",
			element: "<opModLFRTMustTrip>",
			set: func(b *sep2.DERControlBase) {
				v := int32(21)
				b.OpModLFRTMustTrip = &v
			},
			get: func(b *sep2.DERControlBase) (int32, bool) {
				if b.OpModLFRTMustTrip == nil {
					return 0, false
				}
				return *b.OpModLFRTMustTrip, true
			},
		},
		{
			name:    "OpModHFRTMustTrip",
			element: "<opModHFRTMustTrip>",
			set: func(b *sep2.DERControlBase) {
				v := int32(22)
				b.OpModHFRTMustTrip = &v
			},
			get: func(b *sep2.DERControlBase) (int32, bool) {
				if b.OpModHFRTMustTrip == nil {
					return 0, false
				}
				return *b.OpModHFRTMustTrip, true
			},
		},
		{
			name:    "OpModVoltWatt",
			element: "<opModVoltWatt>",
			set: func(b *sep2.DERControlBase) {
				v := int32(31)
				b.OpModVoltWatt = &v
			},
			get: func(b *sep2.DERControlBase) (int32, bool) {
				if b.OpModVoltWatt == nil {
					return 0, false
				}
				return *b.OpModVoltWatt, true
			},
		},
		{
			name:    "OpModFreqWatt",
			element: "<opModFreqWatt>",
			set: func(b *sep2.DERControlBase) {
				v := int32(41)
				b.OpModFreqWatt = &v
			},
			get: func(b *sep2.DERControlBase) (int32, bool) {
				if b.OpModFreqWatt == nil {
					return 0, false
				}
				return *b.OpModFreqWatt, true
			},
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctrl := sep2.DERControl{DERControlBase: &sep2.DERControlBase{}}
			tc.set(ctrl.DERControlBase)

			data, err := xml.Marshal(&ctrl)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			xmlStr := string(data)
			if !strings.Contains(xmlStr, tc.element) {
				t.Errorf("marshalled XML missing %s element\nXML: %s", tc.element, xmlStr)
			}

			var parsed sep2.DERControl
			if err := xml.Unmarshal(data, &parsed); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if parsed.DERControlBase == nil {
				t.Fatal("parsed DERControlBase is nil")
			}
			gotWant, _ := tc.get(ctrl.DERControlBase)
			got, ok := tc.get(parsed.DERControlBase)
			if !ok {
				t.Fatalf("%s lost on unmarshal", tc.name)
			}
			if got != gotWant {
				t.Errorf("%s = %d, want %d", tc.name, got, gotWant)
			}

			// Copy() round-trip: independent pointer, equal value.
			cb := ctrl.DERControlBase.Copy()
			copyGot, ok := tc.get(&cb)
			if !ok {
				t.Fatalf("%s lost on Copy()", tc.name)
			}
			if copyGot != gotWant {
				t.Errorf("Copy().%s = %d, want %d", tc.name, copyGot, gotWant)
			}
		})
	}
}

// TestDERControlBaseOmitEmpty proves that an all-nil DERControlBase
// marshals without any opMod*/setMod* child elements — guards against
// a future zero-value field leaking into the wire form.
func TestDERControlBaseOmitEmpty(t *testing.T) {
	ctrl := sep2.DERControl{DERControlBase: &sep2.DERControlBase{}}
	data, err := xml.Marshal(&ctrl)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	xmlStr := string(data)
	// Each new field name should not appear when the pointer is nil.
	for _, elem := range []string{
		"opModLVRTMustTrip", "opModLVRTMomentaryCessation",
		"opModHVRTMustTrip", "opModHVRTMomentaryCessation",
		"opModLFRTMustTrip", "opModHFRTMustTrip",
		"opModVoltWatt", "opModFreqWatt",
	} {
		if strings.Contains(xmlStr, elem) {
			t.Errorf("zero-value DERControlBase emitted %q\nXML: %s", elem, xmlStr)
		}
	}
}

// TestDefaultDERControlSetGradWRoundTrip exercises the IEEE-092
// IEEE 2030.5 §10.11 device-default ramp-rate fields setGradW and
// setSoftGradW for XML round-trip plus Copy() independence.
func TestDefaultDERControlSetGradWRoundTrip(t *testing.T) {
	grad := uint16(1000)    // 10%/s in hundredths of percent per second
	softGrad := uint16(500) // 5%/s
	original := sep2.DefaultDERControl{
		SubscribableResource: sep2.SubscribableResource{
			Resource: sep2.Resource{Href: "/derp/1/dderc"},
		},
		MRID:         "DDERC001",
		SetGradW:     &grad,
		SetSoftGradW: &softGrad,
	}

	data, err := xml.Marshal(&original)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	xmlStr := string(data)
	if !strings.Contains(xmlStr, "<setGradW>1000</setGradW>") {
		t.Errorf("missing <setGradW>1000</setGradW>\nXML: %s", xmlStr)
	}
	if !strings.Contains(xmlStr, "<setSoftGradW>500</setSoftGradW>") {
		t.Errorf("missing <setSoftGradW>500</setSoftGradW>\nXML: %s", xmlStr)
	}

	var parsed sep2.DefaultDERControl
	if err := xml.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if parsed.SetGradW == nil || *parsed.SetGradW != 1000 {
		t.Errorf("SetGradW = %v, want 1000", parsed.SetGradW)
	}
	if parsed.SetSoftGradW == nil || *parsed.SetSoftGradW != 500 {
		t.Errorf("SetSoftGradW = %v, want 500", parsed.SetSoftGradW)
	}

	// Copy() independence: mutating the copy must not touch original.
	cp := original.Copy()
	*cp.SetGradW = 0
	*cp.SetSoftGradW = 0
	if *original.SetGradW != 1000 {
		t.Errorf("original.SetGradW mutated to %d", *original.SetGradW)
	}
	if *original.SetSoftGradW != 500 {
		t.Errorf("original.SetSoftGradW mutated to %d", *original.SetSoftGradW)
	}
}

// TestDefaultDERControlOmitGradFields proves an all-nil
// DefaultDERControl marshals without setGradW or setSoftGradW.
func TestDefaultDERControlOmitGradFields(t *testing.T) {
	dderc := sep2.DefaultDERControl{
		SubscribableResource: sep2.SubscribableResource{
			Resource: sep2.Resource{Href: "/derp/1/dderc"},
		},
	}
	data, err := xml.Marshal(&dderc)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	xmlStr := string(data)
	if strings.Contains(xmlStr, "setGradW") {
		t.Errorf("zero-value DefaultDERControl emitted setGradW\nXML: %s", xmlStr)
	}
	if strings.Contains(xmlStr, "setSoftGradW") {
		t.Errorf("zero-value DefaultDERControl emitted setSoftGradW\nXML: %s", xmlStr)
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
