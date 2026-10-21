package handler_test

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/dercontrol"
	coreder "github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/handlers/der"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

// #969: the curve rows. A curve value names a stored DERCurve by id.

// curveRowTypes is each curve row with the DERCurveType core gives its
// element.
var curveRowTypes = []struct {
	name      string
	curveType uint8
}{
	{"opModFreqWatt", sep2.CurveTypeOpModFreqWatt},
	{"opModHFRTMayTrip", sep2.CurveTypeOpModHFRTMayTrip},
	{"opModHFRTMustTrip", sep2.CurveTypeOpModHFRTMustTrip},
	{"opModHVRTMayTrip", sep2.CurveTypeOpModHVRTMayTrip},
	{"opModHVRTMomentaryCessation", sep2.CurveTypeOpModHVRTMomentaryCessation},
	{"opModHVRTMustTrip", sep2.CurveTypeOpModHVRTMustTrip},
	{"opModLFRTMayTrip", sep2.CurveTypeOpModLFRTMayTrip},
	{"opModLFRTMustTrip", sep2.CurveTypeOpModLFRTMustTrip},
	{"opModLVRTMayTrip", sep2.CurveTypeOpModLVRTMayTrip},
	{"opModLVRTMomentaryCessation", sep2.CurveTypeOpModLVRTMomentaryCessation},
	{"opModLVRTMustTrip", sep2.CurveTypeOpModLVRTMustTrip},
	{"opModVoltVar", sep2.CurveTypeOpModVoltVar},
	{"opModVoltWatt", sep2.CurveTypeOpModVoltWatt},
	{"opModWattPF", sep2.CurveTypeOpModWattPF},
	{"opModWattVar", sep2.CurveTypeOpModWattVar},
}

func curveID(curveType uint8) string { return "c" + strconv.Itoa(int(curveType)) }

// storedCurves holds one curve of every type, stored as curveID(type).
func storedCurves(t *testing.T) *memory.Store[sep2.DERCurve] {
	t.Helper()
	s := memory.NewStore[sep2.DERCurve]()
	for _, c := range curveRowTypes {
		id := curveID(c.curveType)
		curve := sep2.DERCurve{MRID: id, CurveType: c.curveType}
		curve.Href = "/dc/" + id
		if err := s.Create(context.Background(), id, curve); err != nil {
			t.Fatal(err)
		}
	}
	return s
}

// A control carrying each curve row is created with the curve sent, listed
// back with its id, and served to a device as a link to that curve.
func TestDERControlCreate_CurveRowsRoundTrip(t *testing.T) {
	for _, c := range curveRowTypes {
		t.Run(c.name, func(t *testing.T) {
			d := newDCHarness(t, ptrU32(dcPEN))
			d.h.Curves = storedCurves(t)
			id := curveID(c.curveType)
			w := d.do(t, http.MethodPost, "/api/der/controls", functionsBody(futureStart(600), `{"`+c.name+`":"`+id+`"}`))
			if w.Code != http.StatusCreated {
				t.Fatalf("status = %d body = %s, want 201", w.Code, w.Body.String())
			}
			created := decodeCreated(t, w)

			list := d.do(t, http.MethodGet, "/api/der/controls?device=0", "")
			var body struct {
				Controls []struct {
					Functions      map[string]json.RawMessage `json:"functions"`
					DERControlBase map[string]json.RawMessage `json:"derControlBase"`
				} `json:"controls"`
			}
			if err := json.Unmarshal(list.Body.Bytes(), &body); err != nil || len(body.Controls) != 1 {
				t.Fatalf("list = %s (%v), want one control", list.Body.String(), err)
			}
			got := body.Controls[0]
			if len(got.Functions) != 1 || string(got.Functions[c.name]) != `"`+id+`"` {
				t.Errorf("listed functions = %s, want only %s = %q", list.Body.String(), c.name, id)
			}
			if string(got.DERControlBase[c.name]) != `{"href":"/dc/`+id+`"}` {
				t.Errorf("listed derControlBase = %s, want %s linking /dc/%s", list.Body.String(), c.name, id)
			}

			parent, cid, _, err := d.controls.ByMRID(t.Context(), created.MRID)
			if err != nil {
				t.Fatalf("ByMRID: %v", err)
			}
			served, err := coreder.NewDerivedStatusControlStore(d.controls, d.lifecycles).Get(t.Context(), parent, cid)
			if err != nil {
				t.Fatalf("serve: %v", err)
			}
			doc, err := xml.Marshal(served)
			if err != nil {
				t.Fatalf("the served control does not encode: %v", err)
			}
			if want := `<DERControlBase><` + c.name + ` href="/dc/` + id + `"></` + c.name + `></DERControlBase>`; !strings.Contains(string(doc), want) {
				t.Errorf("served control %s\nlacks %s", doc, want)
			}
		})
	}
}

// withCurves gives both handlers of x the curve store curves.
func withCurves(x *fsaControlFixture, curves dercontrol.CurveReader) {
	x.dc.h.Curves = curves
	x.f.h.Curves = curves
}

// isProgramRoute reports whether rt takes its values as a program's default
// control, whose refusals carry a "defaultControl." prefix and no audit line.
func isProgramRoute(rt cvRoute) bool { return strings.Contains(rt.name, "program") }

// Every route that takes control values, default controls included, takes a
// stored curve of the right type, and refuses one that is not stored or is
// of another type.
func TestControlValues_CurveCheckedOnEveryRoute(t *testing.T) {
	const fn = "opModVoltVar"
	routes := cvRoutes()
	if len(routes) != 8 {
		t.Fatalf("%d control values routes, want 8", len(routes))
	}
	for _, rt := range routes {
		x := newControlValuesFixture(t)
		withCurves(x, storedCurves(t))
		w := x.f.do(rt.method, rt.path(x), rt.body(`"functions":{"`+fn+`":"`+curveID(sep2.CurveTypeOpModVoltVar)+`"}`))
		if w.Code != http.StatusCreated && w.Code != http.StatusOK {
			t.Errorf("%s: stored curve: status %d body %s, want a success", rt.name, w.Code, w.Body.String())
		}

		for _, tc := range []struct {
			name, id, code, message string
		}{
			{"unknown", "nope", "unknown_curve", "functions." + fn + ": no stored curve has this id"},
			{"wrong type", curveID(sep2.CurveTypeOpModVoltWatt), "wrong_curve_type", "functions." + fn + ": the curve's curveType must be 11"},
		} {
			x := newControlValuesFixture(t)
			withCurves(x, storedCurves(t))
			w := x.f.do(rt.method, rt.path(x), rt.body(`"functions":{"`+fn+`":"`+tc.id+`"}`))
			want := tc.message
			if isProgramRoute(rt) {
				want = "defaultControl." + want
			}
			var body struct {
				Error string `json:"error"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || w.Code != http.StatusBadRequest || body.Error != want {
				t.Errorf("%s/%s: status %d body %s, want 400 %q", rt.name, tc.name, w.Code, w.Body.String(), want)
			}
			if !isProgramRoute(rt) && !strings.Contains(x.dc.logs.String(), "code="+tc.code) {
				t.Errorf("%s/%s: audit log lacks code=%s:\n%s", rt.name, tc.name, tc.code, x.dc.logs.String())
			}
			if c, _ := x.dc.storedCounts(t); c != 0 {
				t.Errorf("%s/%s: %d controls stored after a refusal", rt.name, tc.name, c)
			}
		}
	}
}

type unreadableCurves struct{}

func (unreadableCurves) Get(context.Context, string) (sep2.DERCurve, error) {
	return sep2.DERCurve{}, errors.New("curve store unreadable")
}

// A curve store that cannot be read answers 500 on every route, never a 400
// that would tell the operator the curve is wrong.
func TestControlValues_UnreadableCurveStoreIs500(t *testing.T) {
	for _, rt := range cvRoutes() {
		x := newControlValuesFixture(t)
		withCurves(x, unreadableCurves{})
		w := x.f.do(rt.method, rt.path(x), rt.body(`"functions":{"opModVoltVar":"c11"}`))
		if w.Code != http.StatusInternalServerError {
			t.Errorf("%s: status %d body %s, want 500", rt.name, w.Code, w.Body.String())
		}
		if !isProgramRoute(rt) && !strings.Contains(x.dc.logs.String(), "cause=curve_read_failed") {
			t.Errorf("%s: audit log lacks cause=curve_read_failed:\n%s", rt.name, x.dc.logs.String())
		}
	}
}
