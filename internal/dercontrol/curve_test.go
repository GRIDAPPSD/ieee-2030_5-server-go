package dercontrol

import (
	"encoding/xml"
	"errors"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
)

func TestCurveLink(t *testing.T) {
	t.Parallel()
	for _, id := range []string{"0", "42", "c1"} {
		link, err := CurveLink(id)
		if err != nil {
			t.Fatalf("CurveLink(%q): %v", id, err)
		}
		if want := "/dc/" + id; link.Href != want || CurveHref(id) != want {
			t.Errorf("CurveLink(%q).Href = %q, CurveHref = %q, want %q", id, link.Href, CurveHref(id), want)
		}
	}
	if _, err := CurveLink(""); !errors.Is(err, ErrEmptyCurveID) {
		t.Errorf("CurveLink(\"\") error = %v, want ErrEmptyCurveID", err)
	}
}

// TestCurveLinkWire pins the element a device reads: an empty element whose
// href attribute is the curve's own href.
func TestCurveLinkWire(t *testing.T) {
	t.Parallel()
	link, err := CurveLink("3")
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
