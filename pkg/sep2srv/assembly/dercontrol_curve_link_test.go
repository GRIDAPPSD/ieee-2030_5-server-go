package assembly_test

import (
	"context"
	"io"
	"net/http"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/dercontrol/dercurve"
)

// A client following a served DERControl's curve links must reach the stored
// curve: GET reads its values back and HEAD answers 200 with no body.
func TestDERControlCurveLinksResolveToTheStoredCurves(t *testing.T) {
	t.Parallel()

	stores := testStores()
	ctx := context.Background()
	vvHref, err := dercurve.Href("vv1")
	if err != nil {
		t.Fatalf("Href: %v", err)
	}
	fwHref, err := dercurve.Href("fw1")
	if err != nil {
		t.Fatalf("Href: %v", err)
	}
	curves := map[string]sep2.DERCurve{
		"vv1": {CurveType: 11, YRefType: 3, XMultiplier: -1, YMultiplier: 2,
			CurveData: []sep2.CurveData{{XValue: 9200, YValue: 4400}, {XValue: 10800, YValue: -4400}}},
		"fw1": {CurveType: 4, YRefType: 1, XMultiplier: -2, YMultiplier: 1,
			CurveData: []sep2.CurveData{{XValue: 6000, YValue: 100}}},
	}
	for id, c := range curves {
		c.Href, _ = dercurve.Href(id)
		if err := stores.DERCurves.Create(ctx, id, c); err != nil {
			t.Fatalf("seed curve %s: %v", id, err)
		}
	}

	want := seedDERControl(t, stores, testLFDI, "curve-0", 5417, 1785429793)
	want.DERControlBase.OpModVoltVar = &sep2.DERCurveLink{Href: vvHref}
	want.DERControlBase.OpModFreqWatt = &sep2.DERCurveLink{Href: fwHref}
	if err := stores.DERControls.Update(ctx, derControlScopeKey(testLFDI, "1", "1"), "curve-0", want); err != nil {
		t.Fatalf("store control with curve links: %v", err)
	}
	srv := derControlRouter(t, stores)

	resp, err := srv.Client().Get(srv.URL + want.Href)
	if err != nil {
		t.Fatalf("GET %s: %v", want.Href, err)
	}
	var ctrl sep2.DERControl
	decodeXML(t, resp, &ctrl)
	if ctrl.DERControlBase == nil || ctrl.DERControlBase.OpModVoltVar == nil || ctrl.DERControlBase.OpModFreqWatt == nil {
		t.Fatalf("served control carries no curve links: %+v", ctrl.DERControlBase)
	}

	links := map[string]string{"vv1": ctrl.DERControlBase.OpModVoltVar.Href, "fw1": ctrl.DERControlBase.OpModFreqWatt.Href}
	for id, href := range links {
		t.Run(id, func(t *testing.T) {
			resp, err := srv.Client().Get(srv.URL + href)
			if err != nil {
				t.Fatalf("GET %s: %v", href, err)
			}
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("GET %s: status %d, want 200", href, resp.StatusCode)
			}
			var got sep2.DERCurve
			decodeXML(t, resp, &got)
			w := curves[id]
			if got.Href != href || got.CurveType != w.CurveType || got.YRefType != w.YRefType ||
				got.XMultiplier != w.XMultiplier || got.YMultiplier != w.YMultiplier {
				t.Errorf("curve at %s = href %q type %d yRef %d xMul %d yMul %d, want %q %d %d %d %d",
					href, got.Href, got.CurveType, got.YRefType, got.XMultiplier, got.YMultiplier,
					href, w.CurveType, w.YRefType, w.XMultiplier, w.YMultiplier)
			}
			if len(got.CurveData) != len(w.CurveData) {
				t.Fatalf("curve at %s has %d points, want %d", href, len(got.CurveData), len(w.CurveData))
			}
			for i, p := range w.CurveData {
				if got.CurveData[i] != p {
					t.Errorf("curve at %s point %d = %+v, want %+v", href, i, got.CurveData[i], p)
				}
			}

			head, err := srv.Client().Head(srv.URL + href)
			if err != nil {
				t.Fatalf("HEAD %s: %v", href, err)
			}
			body, _ := io.ReadAll(head.Body)
			_ = head.Body.Close()
			if head.StatusCode != http.StatusOK || len(body) != 0 {
				t.Errorf("HEAD %s: status %d body %d bytes, want 200 and none", href, head.StatusCode, len(body))
			}
		})
	}

	missing, err := srv.Client().Get(srv.URL + "/dc/absent")
	if err != nil {
		t.Fatalf("GET /dc/absent: %v", err)
	}
	_ = missing.Body.Close()
	if missing.StatusCode != http.StatusNotFound {
		t.Errorf("GET /dc/absent: status %d, want 404", missing.StatusCode)
	}
}
