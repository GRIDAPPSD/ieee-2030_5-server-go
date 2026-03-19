package sep2_test

import (
	"testing"

	"github.com/craig8/ieee-2030_5-go/pkg/sep2"
)

func TestEndDeviceCopyNilFields(t *testing.T) {
	// All pointer fields nil — should not panic
	dev := sep2.EndDevice{SFDI: "123"}
	copied := dev.Copy()
	if copied.SFDI != "123" {
		t.Error("SFDI not copied")
	}
}

func TestUsagePointCopy(t *testing.T) {
	upt := sep2.UsagePoint{
		MeterReadingListLink: &sep2.ListLink{Href: "/mr", All: 5},
	}
	copied := upt.Copy()
	copied.MeterReadingListLink.Href = "/changed"

	if upt.MeterReadingListLink.Href != "/mr" {
		t.Error("original mutated")
	}
}

func TestUsagePointCopyNilFields(t *testing.T) {
	upt := sep2.UsagePoint{MRID: "test"}
	copied := upt.Copy()
	if copied.MRID != "test" {
		t.Error("MRID not copied")
	}
}

func TestMeterReadingCopy(t *testing.T) {
	mr := sep2.MeterReading{
		ReadingTypeLink:    &sep2.Link{Href: "/rt"},
		ReadingLink:        &sep2.Link{Href: "/r"},
		ReadingSetListLink: &sep2.ListLink{Href: "/rs"},
	}
	copied := mr.Copy()
	copied.ReadingTypeLink.Href = "/changed"

	if mr.ReadingTypeLink.Href != "/rt" {
		t.Error("original mutated")
	}
}

func TestMeterReadingCopyNilFields(t *testing.T) {
	mr := sep2.MeterReading{MRID: "test"}
	copied := mr.Copy()
	if copied.MRID != "test" {
		t.Error("MRID not copied")
	}
}

func TestReadingTypeCopy(t *testing.T) {
	uom := sep2.UomWatts
	flow := sep2.FlowDirectionForward
	mult := int8(-3)
	rt := sep2.ReadingType{
		Uom:                  &uom,
		FlowDirection:        &flow,
		PowerOfTenMultiplier: &mult,
	}
	copied := rt.Copy()
	*copied.Uom = 0
	*copied.PowerOfTenMultiplier = 0

	if *rt.Uom != sep2.UomWatts {
		t.Error("original Uom mutated")
	}
	if *rt.PowerOfTenMultiplier != -3 {
		t.Error("original multiplier mutated")
	}
}

func TestReadingTypeCopyAllFields(t *testing.T) {
	v1 := uint8(1)
	v2 := uint8(2)
	v3 := uint8(3)
	v4 := uint8(4)
	v5 := uint8(5)
	rt := sep2.ReadingType{
		AccumulationBehaviour: &v1,
		Commodity:             &v2,
		DataQualifier:         &v3,
		Kind:                  &v4,
		Phase:                 &v5,
	}
	copied := rt.Copy()
	*copied.AccumulationBehaviour = 99

	if *rt.AccumulationBehaviour != 1 {
		t.Error("original AccumulationBehaviour mutated")
	}
}

func TestMirrorUsagePointCopy(t *testing.T) {
	rate := uint32(300)
	mup := sep2.MirrorUsagePoint{
		PostRate:                   &rate,
		MirrorMeterReadingListLink: &sep2.ListLink{Href: "/mr"},
	}
	copied := mup.Copy()
	*copied.PostRate = 999
	copied.MirrorMeterReadingListLink.Href = "/changed"

	if *mup.PostRate != 300 {
		t.Error("original PostRate mutated")
	}
	if mup.MirrorMeterReadingListLink.Href != "/mr" {
		t.Error("original link mutated")
	}
}

func TestDERCopy(t *testing.T) {
	der := sep2.DER{
		DERCapabilityLink: &sep2.Link{Href: "/dercap"},
		DERStatusLink:     &sep2.Link{Href: "/ders"},
		DERSettingsLink:   &sep2.Link{Href: "/derg"},
		DERAvailabilityLink: &sep2.Link{Href: "/dera"},
		AssociatedDERProgramListLink: &sep2.ListLink{Href: "/derp"},
	}
	copied := der.Copy()
	copied.DERCapabilityLink.Href = "/changed"

	if der.DERCapabilityLink.Href != "/dercap" {
		t.Error("original mutated")
	}
}

func TestDERCopyNilFields(t *testing.T) {
	der := sep2.DER{}
	copied := der.Copy()
	_ = copied // should not panic
}

func TestDERProgramCopy(t *testing.T) {
	prog := sep2.DERProgram{
		ActiveDERControlListLink: &sep2.ListLink{Href: "/derca"},
		DefaultDERControlLink:    &sep2.Link{Href: "/dderc"},
		DERControlListLink:       &sep2.ListLink{Href: "/derc"},
		DERCurveListLink:         &sep2.ListLink{Href: "/dc"},
	}
	copied := prog.Copy()
	copied.DefaultDERControlLink.Href = "/changed"

	if prog.DefaultDERControlLink.Href != "/dderc" {
		t.Error("original mutated")
	}
}

func TestDERCapabilityCopy(t *testing.T) {
	modes := uint32(0xFF)
	maxW := sep2.ActivePower{Value: 5000}
	maxA := int32(20)
	dtype := uint8(1)
	cap := sep2.DERCapability{
		ModesSupported: &modes,
		RTGMaxW:        &maxW,
		RTGMaxA:        &maxA,
		Type:           &dtype,
	}
	copied := cap.Copy()
	*copied.ModesSupported = 0

	if *cap.ModesSupported != 0xFF {
		t.Error("original mutated")
	}
}

func TestDERSettingsCopy(t *testing.T) {
	modes := uint32(0x0F)
	settings := sep2.DERSettings{ModesEnabled: &modes}
	copied := settings.Copy()
	*copied.ModesEnabled = 0

	if *settings.ModesEnabled != 0x0F {
		t.Error("original mutated")
	}
}

func TestDERAvailabilityCopy(t *testing.T) {
	dur := uint32(3600)
	avail := sep2.DERAvailability{
		AvailabilityDuration: &dur,
		StatWAvail:           &sep2.ActivePower{Value: 5000},
	}
	copied := avail.Copy()
	*copied.AvailabilityDuration = 0

	if *avail.AvailabilityDuration != 3600 {
		t.Error("original mutated")
	}
}

func TestDERStatusCopy(t *testing.T) {
	alarm := uint32(0x01)
	soc := uint16(85)
	status := sep2.DERStatus{
		AlarmStatus:         &alarm,
		StateOfChargeStatus: &soc,
		GenConnectStatus:    &sep2.ConnectStatusType{Value: 1},
		InverterStatus:      &sep2.InverterStatusType{Value: 2},
		OperationalModeStatus: &sep2.OperationalModeStatusType{Value: 3},
	}
	copied := status.Copy()
	*copied.AlarmStatus = 0

	if *status.AlarmStatus != 0x01 {
		t.Error("original mutated")
	}
}

func TestSelfDeviceCopy(t *testing.T) {
	sdev := sep2.SelfDevice{
		DeviceInformationLink: &sep2.Link{Href: "/sdi"},
	}
	copied := sdev.Copy()
	copied.DeviceInformationLink.Href = "/changed"

	if sdev.DeviceInformationLink.Href != "/sdi" {
		t.Error("original mutated")
	}
}

func TestSelfDeviceCopyNil(t *testing.T) {
	sdev := sep2.SelfDevice{SFDI: "123"}
	copied := sdev.Copy()
	if copied.SFDI != "123" {
		t.Error("SFDI not copied")
	}
}

func TestFSACopy(t *testing.T) {
	fsa := sep2.FunctionSetAssignments{
		DERProgramListLink: &sep2.ListLink{Href: "/derp"},
		UsagePointListLink: &sep2.ListLink{Href: "/upt"},
		DemandResponseProgramListLink: &sep2.ListLink{Href: "/drp"},
	}
	copied := fsa.Copy()
	copied.DERProgramListLink.Href = "/changed"

	if fsa.DERProgramListLink.Href != "/derp" {
		t.Error("original mutated")
	}
}

func TestFSACopyNil(t *testing.T) {
	fsa := sep2.FunctionSetAssignments{MRID: "test"}
	copied := fsa.Copy()
	if copied.MRID != "test" {
		t.Error("MRID not copied")
	}
}

func TestEventStatusCopy(t *testing.T) {
	supersededTime := int64(1234)
	es := sep2.EventStatus{
		CurrentStatus:            1,
		PotentiallySupersededTime: &supersededTime,
	}
	copied := es.Copy()
	*copied.PotentiallySupersededTime = 9999

	if *es.PotentiallySupersededTime != 1234 {
		t.Error("original mutated")
	}
}

func TestDERControlCopy(t *testing.T) {
	connected := true
	dur := int32(300)
	ctrl := sep2.DERControl{
		DERControlBase: &sep2.DERControlBase{OpModConnect: &connected},
	}
	ctrl.EventStatus = &sep2.EventStatus{CurrentStatus: 1}
	ctrl.Interval = &sep2.DateTimeInterval{Start: 100, Duration: 600}
	ctrl.RandomizeDuration = &dur

	copied := ctrl.Copy()
	*copied.DERControlBase.OpModConnect = false
	*copied.RandomizeDuration = 0

	if *ctrl.DERControlBase.OpModConnect != true {
		t.Error("original OpModConnect mutated")
	}
	if *ctrl.RandomizeDuration != 300 {
		t.Error("original RandomizeDuration mutated")
	}
}

func TestDRLCCopy(t *testing.T) {
	drp := sep2.DemandResponseProgram{
		ActiveEndDeviceControlListLink: &sep2.ListLink{Href: "/active"},
		EndDeviceControlListLink:       &sep2.ListLink{Href: "/edc"},
	}
	copied := drp.Copy()
	copied.ActiveEndDeviceControlListLink.Href = "/changed"

	if drp.ActiveEndDeviceControlListLink.Href != "/active" {
		t.Error("original mutated")
	}
}

func TestEndDeviceControlCopy(t *testing.T) {
	cat := uint32(0xFF)
	edc := sep2.EndDeviceControl{DeviceCategory: &cat}
	edc.EventStatus = &sep2.EventStatus{CurrentStatus: 2}
	edc.Interval = &sep2.DateTimeInterval{Start: 500}

	copied := edc.Copy()
	*copied.DeviceCategory = 0

	if *edc.DeviceCategory != 0xFF {
		t.Error("original mutated")
	}
}
