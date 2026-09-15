package dercontrol

import (
	"testing"
)

// Acceptance criterion 5: the four types map to the closed DERControlBase
// shapes IEEE 2030.5-2018 defines for them, each validated at every range
// boundary, and no request field ever sets randomizeStart,
// randomizeDuration, EventStatus, replyTo, responseRequired, mRID or
// creationTime.

func TestIssue_Mapping_Connect(t *testing.T) {
	h := newHarness(Config{PEN: testPEN(1)})
	h.seedProgram(t, "dev1", "p1", controlListHref("dev1", "0", "p1"))

	res, err := issueWith(t, h, func(r *CreateRequest) { r.Type = Connect })
	if err != nil {
		t.Fatalf("Issue() error = %v", err)
	}
	b := res.Control.DERControlBase
	if b.OpModConnect == nil || !*b.OpModConnect {
		t.Fatalf("OpModConnect = %v, want true", b.OpModConnect)
	}
	if b.OpModEnergize == nil || !*b.OpModEnergize {
		t.Fatalf("OpModEnergize = %v, want true", b.OpModEnergize)
	}
	assertNoServerOwnedFieldsLeaked(t, res)
}

func TestIssue_Mapping_Disconnect(t *testing.T) {
	h := newHarness(Config{PEN: testPEN(1)})
	h.seedProgram(t, "dev1", "p1", controlListHref("dev1", "0", "p1"))

	res, err := issueWith(t, h, func(r *CreateRequest) { r.Type = Disconnect })
	if err != nil {
		t.Fatalf("Issue() error = %v", err)
	}
	b := res.Control.DERControlBase
	if b.OpModConnect == nil || *b.OpModConnect {
		t.Fatalf("OpModConnect = %v, want false", b.OpModConnect)
	}
	if b.OpModEnergize == nil || *b.OpModEnergize {
		t.Fatalf("OpModEnergize = %v, want false", b.OpModEnergize)
	}
	assertNoServerOwnedFieldsLeaked(t, res)
}

func TestIssue_Mapping_ConnectRefusesAValue(t *testing.T) {
	h := newHarness(Config{PEN: testPEN(1)})
	h.seedProgram(t, "dev1", "p1", controlListHref("dev1", "0", "p1"))

	_, err := issueWith(t, h, func(r *CreateRequest) {
		r.Type = Connect
		r.MaxLimW = uint16ptr(100)
	})
	assertRefusal(t, err, RefusalUnexpectedValue)
	assertNoNewControl(t, h)
}

func TestIssue_Mapping_MaxLimW_Boundaries(t *testing.T) {
	cases := []struct {
		name    string
		value   uint16
		wantErr RefusalCode // "" means accepted
	}{
		{"min 0 accepted", 0, ""},
		{"max 10000 accepted", 10000, ""},
		{"above max 10001 refused", 10001, RefusalValueOutOfRange},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(Config{PEN: testPEN(1)})
			h.seedProgram(t, "dev1", "p1", controlListHref("dev1", "0", "p1"))
			v := tc.value
			res, err := issueWith(t, h, func(r *CreateRequest) {
				r.Type = MaxLimW
				r.MaxLimW = &v
			})
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("Issue() error = %v, want accepted", err)
				}
				if res.Control.DERControlBase.OpModMaxLimW == nil || uint16(*res.Control.DERControlBase.OpModMaxLimW) != tc.value {
					t.Fatalf("OpModMaxLimW = %v, want %d", res.Control.DERControlBase.OpModMaxLimW, tc.value)
				}
				assertNoServerOwnedFieldsLeaked(t, res)
				return
			}
			assertRefusal(t, err, tc.wantErr)
			assertNoNewControl(t, h)
		})
	}
}

func TestIssue_Mapping_MaxLimW_RefusesMissingValue(t *testing.T) {
	h := newHarness(Config{PEN: testPEN(1)})
	h.seedProgram(t, "dev1", "p1", controlListHref("dev1", "0", "p1"))

	_, err := issueWith(t, h, func(r *CreateRequest) { r.Type = MaxLimW })
	assertRefusal(t, err, RefusalMissingValue)
	assertNoNewControl(t, h)
}

func TestIssue_Mapping_FixedPFInjectW_Boundaries(t *testing.T) {
	cases := []struct {
		name    string
		disp    uint16
		wantErr RefusalCode
	}{
		{"min 1 accepted", 1, ""},
		{"max 1000 accepted", 1000, ""},
		{"below min 0 refused", 0, RefusalValueOutOfRange},
		{"above max 1001 refused", 1001, RefusalValueOutOfRange},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(Config{PEN: testPEN(1)})
			h.seedProgram(t, "dev1", "p1", controlListHref("dev1", "0", "p1"))
			pf := PowerFactorValue{Displacement: tc.disp, Excitation: true}
			res, err := issueWith(t, h, func(r *CreateRequest) {
				r.Type = FixedPFInjectW
				r.PowerFactor = &pf
			})
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("Issue() error = %v, want accepted", err)
				}
				got := res.Control.DERControlBase.OpModFixedPFInjectW
				if got == nil || got.Displacement != tc.disp || got.Excitation != true || got.Multiplier != -3 {
					t.Fatalf("OpModFixedPFInjectW = %+v, want {Displacement:%d Excitation:true Multiplier:-3}", got, tc.disp)
				}
				assertNoServerOwnedFieldsLeaked(t, res)
				return
			}
			assertRefusal(t, err, tc.wantErr)
			assertNoNewControl(t, h)
		})
	}
}

func TestIssue_Mapping_FixedPFInjectW_RefusesMissingValue(t *testing.T) {
	h := newHarness(Config{PEN: testPEN(1)})
	h.seedProgram(t, "dev1", "p1", controlListHref("dev1", "0", "p1"))

	_, err := issueWith(t, h, func(r *CreateRequest) { r.Type = FixedPFInjectW })
	assertRefusal(t, err, RefusalMissingValue)
	assertNoNewControl(t, h)
}

func TestIssue_Mapping_RefusesUnknownType(t *testing.T) {
	h := newHarness(Config{PEN: testPEN(1)})
	h.seedProgram(t, "dev1", "p1", controlListHref("dev1", "0", "p1"))

	_, err := issueWith(t, h, func(r *CreateRequest) { r.Type = ControlType("opModFixedW") })
	assertRefusal(t, err, RefusalUnknownType)
	assertNoNewControl(t, h)
}

// assertNoServerOwnedFieldsLeaked asserts CreateRequest cannot have set any
// of the fields the issuer alone owns (acceptance criterion 5's negative
// half). CreateRequest exposes no setter for any of them, so this also
// documents that the assertion holds by construction.
func assertNoServerOwnedFieldsLeaked(t *testing.T, res Result) {
	t.Helper()
	if res.Control.RandomizeStart != nil {
		t.Fatalf("RandomizeStart = %v, want nil", res.Control.RandomizeStart)
	}
	if res.Control.RandomizeDuration != nil {
		t.Fatalf("RandomizeDuration = %v, want nil", res.Control.RandomizeDuration)
	}
	if res.Control.EventStatus != nil {
		t.Fatalf("EventStatus = %v, want nil (derived at serve time, not stored)", res.Control.EventStatus)
	}
	if res.Control.ReplyTo != "" {
		t.Fatalf("ReplyTo = %q, want empty (stamped at serve time)", res.Control.ReplyTo)
	}
	if res.Control.ResponseRequired != nil {
		t.Fatalf("ResponseRequired = %v, want nil (stamped at serve time)", res.Control.ResponseRequired)
	}
	if res.Control.MRID == "" {
		t.Fatalf("MRID is empty, want issuer-generated")
	}
	if res.Control.CreationTime == 0 {
		t.Fatalf("CreationTime is zero, want issuer-generated")
	}
}
