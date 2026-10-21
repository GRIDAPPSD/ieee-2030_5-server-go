package dercurve

import (
	"encoding/xml"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
)

func TestLink(t *testing.T) {
	t.Parallel()
	for _, id := range []string{"0", "42", "c1"} {
		link, err := Link(id)
		if err != nil {
			t.Fatalf("Link(%q): %v", id, err)
		}
		href, err := Href(id)
		if want := "/dc/" + id; err != nil || link.Href != want || href != want {
			t.Errorf("Link(%q).Href = %q, Href = %q (%v), want %q", id, link.Href, href, err, want)
		}
	}
	for _, id := range []string{"", " ", "\t\n"} {
		if _, err := Link(id); !errors.Is(err, ErrBlankID) {
			t.Errorf("Link(%q) error = %v, want ErrBlankID", id, err)
		}
		if _, err := Href(id); !errors.Is(err, ErrBlankID) {
			t.Errorf("Href(%q) error = %v, want ErrBlankID", id, err)
		}
	}
}

// TestLinkWire pins the element a device reads: an empty element whose
// href attribute is the curve's own href.
func TestLinkWire(t *testing.T) {
	t.Parallel()
	link, err := Link("3")
	if err != nil {
		t.Fatal(err)
	}
	out, err := xml.Marshal(sep2.DERControlBase{OpModVoltVar: link})
	if err != nil {
		t.Fatal(err)
	}
	const want = `<DERControlBase><opModVoltVar href="/dc/3"></opModVoltVar></DERControlBase>`
	if string(out) != want {
		t.Errorf("wire = %s, want %s", out, want)
	}
}

// TestLinkFields pins the 15 curve-link elements IEEE 2030.5-2018 declares
// on DERControlBase, in declaration order.
func TestLinkFields(t *testing.T) {
	t.Parallel()
	want := []string{
		"OpModFreqWatt", "OpModHFRTMayTrip", "OpModHFRTMustTrip", "OpModHVRTMayTrip",
		"OpModHVRTMomentaryCessation", "OpModHVRTMustTrip", "OpModLFRTMayTrip", "OpModLFRTMustTrip",
		"OpModLVRTMayTrip", "OpModLVRTMomentaryCessation", "OpModLVRTMustTrip",
		"OpModVoltVar", "OpModVoltWatt", "OpModWattPF", "OpModWattVar",
	}
	got := LinkFields()
	if !slices.Equal(got, want) {
		t.Errorf("LinkFields() = %v, want %v", got, want)
	}
	got[0] = "changed"
	if LinkFields()[0] != want[0] {
		t.Error("LinkFields shares its slice with callers")
	}
}

func TestSetLinks(t *testing.T) {
	t.Parallel()
	id := "7"
	spec := struct {
		OpModVoltVar  *string `yaml:"op_mod_volt_var,omitempty"`
		OpModWattPF   *string `yaml:"op_mod_watt_pf"`
		OpModFixedW   *sep2.SignedPerCent
		OpModVoltWatt *string
	}{OpModVoltVar: &id, OpModWattPF: &id}
	var base sep2.DERControlBase
	if err := SetLinks(&spec, &base); err != nil {
		t.Fatalf("SetLinks: %v", err)
	}
	if base.OpModVoltVar == nil || base.OpModVoltVar.Href != "/dc/7" || base.OpModWattPF == nil || base.OpModWattPF.Href != "/dc/7" {
		t.Errorf("links = %+v, %+v, want /dc/7 for both", base.OpModVoltVar, base.OpModWattPF)
	}
	if base.OpModVoltWatt != nil || base.OpModFixedW != nil {
		t.Errorf("unset modes were set: %+v", base)
	}

	blank := " "
	if err := SetLinks(struct {
		OpModVoltVar *string `yaml:"op_mod_volt_var"`
	}{&blank}, &sep2.DERControlBase{}); !errors.Is(err, ErrBlankID) || !strings.HasPrefix(err.Error(), "op_mod_volt_var: ") {
		t.Errorf("blank id error = %v, want op_mod_volt_var: ... ErrBlankID", err)
	}

	// A *string field named like no curve mode fails even when nil, so a
	// misspelt field is caught on every load, not only when it is set.
	if err := SetLinks(struct {
		OpModVoltVarr *string `yaml:"op_mod_volt_varr"`
	}{}, &sep2.DERControlBase{}); err == nil || !strings.Contains(err.Error(), "op_mod_volt_varr (OpModVoltVarr) is not a DERControlBase curve mode") {
		t.Errorf("misnamed field error = %v", err)
	}
	if err := SetLinks(struct {
		RampTms *string
	}{}, &sep2.DERControlBase{}); err == nil {
		t.Error("a *string field named like a non-link DERControlBase field was accepted")
	}
}

func TestIDInvertsHref(t *testing.T) {
	href, err := Href("vv1")
	if err != nil {
		t.Fatal(err)
	}
	if id, ok := ID(href); !ok || id != "vv1" {
		t.Errorf("ID(%q) = %q, %v, want vv1, true", href, id, ok)
	}
	for _, bad := range []string{"", "/dc/", "/dc/ ", "/elsewhere/vv1", "dc/vv1"} {
		if id, ok := ID(bad); ok {
			t.Errorf("ID(%q) = %q, true, want refused", bad, id)
		}
	}
}
