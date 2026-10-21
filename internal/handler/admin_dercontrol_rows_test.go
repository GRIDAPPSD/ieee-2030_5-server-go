package handler_test

import (
	"encoding/json"
	"encoding/xml"
	"net/http"
	"strings"
	"testing"

	coreder "github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/handlers/der"
)

// #969: a control carrying each added catalog row is created with the value
// sent, listed back with it, and served to a device as that element, read
// through the status decorator the wire routes read.
func TestDERControlCreate_AddedRowsRoundTrip(t *testing.T) {
	tests := []struct{ name, value, wire string }{
		{"opModFixedPFAbsorbW", `{"displacement":950,"excitation":false}`,
			`<opModFixedPFAbsorbW><displacement>950</displacement><excitation>false</excitation><multiplier>-3</multiplier></opModFixedPFAbsorbW>`},
		{"opModFixedVar", `{"refType":2,"value":-2500}`, `<opModFixedVar><refType>2</refType><value>-2500</value></opModFixedVar>`},
		{"opModFixedW", `-7500`, `<opModFixedW>-7500</opModFixedW>`},
		{"opModFreqDroop", `{"dBOF":36,"dBUF":37,"kOF":5,"kUF":6,"openLoopTms":500}`,
			`<opModFreqDroop><dBOF>36</dBOF><dBUF>37</dBUF><kOF>5</kOF><kUF>6</kUF><openLoopTms>500</openLoopTms></opModFreqDroop>`},
		{"opModTargetVar", `{"value":-1200,"multiplier":2}`, `<opModTargetVar><multiplier>2</multiplier><value>-1200</value></opModTargetVar>`},
		{"rampTms", `6000`, `<rampTms>6000</rampTms>`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			d := newDCHarness(t, ptrU32(dcPEN))
			w := d.do(t, http.MethodPost, "/api/der/controls", functionsBody(futureStart(600), `{"`+tc.name+`":`+tc.value+`}`))
			if w.Code != http.StatusCreated {
				t.Fatalf("status = %d body = %s, want 201", w.Code, w.Body.String())
			}
			created := decodeCreated(t, w)
			if created.Type != "combined" {
				t.Errorf("type = %q, want combined", created.Type)
			}

			list := d.do(t, http.MethodGet, "/api/der/controls?device=0", "")
			var body struct {
				Controls []struct {
					Functions map[string]json.RawMessage `json:"functions"`
				} `json:"controls"`
			}
			if err := json.Unmarshal(list.Body.Bytes(), &body); err != nil || len(body.Controls) != 1 {
				t.Fatalf("list = %s (%v), want one control", list.Body.String(), err)
			}
			if got := body.Controls[0].Functions; len(got) != 1 || string(got[tc.name]) != tc.value {
				t.Errorf("listed functions = %s, want only %s = %s", list.Body.String(), tc.name, tc.value)
			}

			parent, id, _, err := d.controls.ByMRID(t.Context(), created.MRID)
			if err != nil {
				t.Fatalf("ByMRID: %v", err)
			}
			served, err := coreder.NewDerivedStatusControlStore(d.controls, d.lifecycles).Get(t.Context(), parent, id)
			if err != nil {
				t.Fatalf("serve: %v", err)
			}
			doc, err := xml.Marshal(served)
			if err != nil {
				t.Fatalf("the served control does not encode: %v", err)
			}
			if want := "<DERControlBase>" + tc.wire + "</DERControlBase>"; !strings.Contains(string(doc), want) {
				t.Errorf("served control %s\nlacks %s", doc, want)
			}
		})
	}
}
