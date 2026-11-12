package assembly_test

import (
	"context"
	"encoding/xml"
	"net/http"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
)

// associationFixture is one owned device holding a DER whose stored record
// names its current program and usage point, a second program in another FSA,
// and two curves, behind the gate with the device's own identity.
//
// Ids: device victimID, DER "1", programs "1" (FSA "1") and "2" (FSA "9").
type associationFixture struct {
	srvGet   func(path, asLFDI string) (int, []byte)
	progHref string
}

func newAssociationFixture(t *testing.T) associationFixture {
	t.Helper()
	ctx := context.Background()
	stores := seededGateStores(t)
	seedManagedSubResources(t, stores, victimID)
	progHref := "/edev/" + victimID + "/fsa/" + victimID + "/derp/" + victimID
	second := "/edev/" + victimID + "/fsa/9/derp/2"
	if err := stores.DERPrograms.Create(ctx, victimID, "2", sep2.DERProgram{
		SubscribableResource: sep2.SubscribableResource{Resource: sep2.Resource{Href: second}},
		Primacy:              0,
	}); err != nil {
		t.Fatalf("seed second program: %v", err)
	}
	if err := stores.DERCurves.Create(ctx, "c2", sep2.DERCurve{Resource: sep2.Resource{Href: "/dc/c2"}}); err != nil {
		t.Fatalf("seed second curve: %v", err)
	}
	srv := gateServer(t, stores, gateTestPolicy())
	return associationFixture{
		srvGet: func(path, asLFDI string) (int, []byte) {
			return gateRequest(t, srv, http.MethodGet, path, asLFDI, "")
		},
		progHref: progHref,
	}
}

func getOK[T any](t *testing.T, f associationFixture, path string, into *T) {
	t.Helper()
	status, raw := f.srvGet(path, victimLFDI)
	if status != http.StatusOK {
		t.Fatalf("GET %s: status %d, want 200; body=%s", path, status, raw)
	}
	if err := xml.Unmarshal(raw, into); err != nil {
		t.Fatalf("GET %s: decode: %v; body=%s", path, err, raw)
	}
}

func TestDERAssociations_ServedValuesMatchStoredContent(t *testing.T) {
	t.Parallel()
	f := newAssociationFixture(t)
	derPath := "/edev/" + victimID + "/der/" + victimID

	t.Run("the DER advertises the three associations at its own routes", func(t *testing.T) {
		var d sep2.DER
		getOK(t, f, derPath, &d)
		if d.AssociatedDERProgramListLink == nil || d.AssociatedDERProgramListLink.Href != derPath+"/derp" {
			t.Errorf("AssociatedDERProgramListLink = %+v, want %s/derp", d.AssociatedDERProgramListLink, derPath)
		}
		if d.CurrentDERProgramLink == nil || d.CurrentDERProgramLink.Href != derPath+"/cdp" {
			t.Errorf("CurrentDERProgramLink = %+v, want %s/cdp", d.CurrentDERProgramLink, derPath)
		}
		if d.AssociatedUsagePointLink == nil || d.AssociatedUsagePointLink.Href != derPath+"/upt" {
			t.Errorf("AssociatedUsagePointLink = %+v, want %s/upt", d.AssociatedUsagePointLink, derPath)
		}
	})

	t.Run("the associated program list holds every program of the device in primacy order", func(t *testing.T) {
		var l sep2.DERProgramList
		getOK(t, f, derPath+"/derp", &l)
		if l.Href != derPath+"/derp" || l.All != 2 || l.Results != 2 || len(l.DERProgram) != 2 {
			t.Fatalf("list href=%q all=%d results=%d members=%d, want %s/derp 2 2 2", l.Href, l.All, l.Results, len(l.DERProgram), derPath)
		}
		// Program 2 has primacy 0 and program 1 primacy 0 too; both stored hrefs must be served as stored.
		got := map[string]bool{l.DERProgram[0].Href: true, l.DERProgram[1].Href: true}
		if !got[f.progHref] || !got["/edev/"+victimID+"/fsa/9/derp/2"] {
			t.Errorf("member hrefs = %v, want %s and the FSA 9 program", got, f.progHref)
		}
	})

	t.Run("the current program is the one the stored record names", func(t *testing.T) {
		var p sep2.DERProgram
		getOK(t, f, derPath+"/cdp", &p)
		if p.Href != f.progHref {
			t.Errorf("current program href = %q, want %q", p.Href, f.progHref)
		}
	})

	t.Run("the associated usage point is the stored one", func(t *testing.T) {
		var u sep2.UsagePoint
		getOK(t, f, derPath+"/upt", &u)
		if u.Href != "/upt/"+victimID {
			t.Errorf("usage point href = %q, want /upt/%s", u.Href, victimID)
		}
	})

	t.Run("the program advertises its curve list and the list and curves resolve under it", func(t *testing.T) {
		var p sep2.DERProgram
		getOK(t, f, f.progHref, &p)
		if p.DERCurveListLink == nil || p.DERCurveListLink.Href != f.progHref+"/dc" || p.DERCurveListLink.All != 2 {
			t.Fatalf("DERCurveListLink = %+v, want href %s/dc all 2", p.DERCurveListLink, f.progHref)
		}
		var l sep2.DERCurveList
		getOK(t, f, p.DERCurveListLink.Href, &l)
		if l.All != 2 || l.Results != 2 || len(l.DERCurve) != 2 {
			t.Fatalf("curve list all=%d results=%d members=%d, want 2 2 2", l.All, l.Results, len(l.DERCurve))
		}
		for _, c := range l.DERCurve {
			var one sep2.DERCurve
			getOK(t, f, c.Href, &one)
			if one.Href != c.Href {
				t.Errorf("curve at %s serves href %q", c.Href, one.Href)
			}
		}
		if want := f.progHref + "/dc/" + victimID; l.DERCurve[0].Href != want && l.DERCurve[1].Href != want {
			t.Errorf("curve hrefs %q, %q: want one at %s", l.DERCurve[0].Href, l.DERCurve[1].Href, want)
		}
	})

	t.Run("an unknown curve or a program of another FSA is a 404", func(t *testing.T) {
		for _, path := range []string{
			f.progHref + "/dc/nope",
			"/edev/" + victimID + "/fsa/other/derp/" + victimID + "/dc",
			derPath + "x/derp",
		} {
			if status, _ := f.srvGet(path, victimLFDI); status != http.StatusNotFound {
				t.Errorf("GET %s: status %d, want 404", path, status)
			}
		}
	})
}

// A foreign device is refused every association route at the ownership gate,
// and a stored record that names another device's program is not followed.
func TestDERAssociations_ForeignDeviceAndForeignRecord(t *testing.T) {
	t.Parallel()
	f := newAssociationFixture(t)
	derPath := "/edev/" + victimID + "/der/" + victimID
	for _, path := range []string{
		derPath + "/derp", derPath + "/cdp", derPath + "/upt",
		f.progHref + "/dc", f.progHref + "/dc/c2",
	} {
		if status, _ := f.srvGet(path, callerLFDI); status != http.StatusForbidden {
			t.Errorf("foreign device GET %s: status %d, want 403", path, status)
		}
	}

	ctx := context.Background()
	stores := seededGateStores(t)
	seedManagedSubResources(t, stores, victimID)
	d, err := stores.DERs.Get(ctx, victimID, victimID)
	if err != nil {
		t.Fatalf("get DER: %v", err)
	}
	d.CurrentDERProgramLink = &sep2.Link{Href: "/edev/" + callerID + "/fsa/1/derp/1"}
	d.AssociatedUsagePointLink = &sep2.Link{Href: "/upt/missing"}
	if err := stores.DERs.Update(ctx, victimID, victimID, d); err != nil {
		t.Fatalf("update DER: %v", err)
	}
	srv := gateServer(t, stores, gateTestPolicy())
	for _, path := range []string{derPath + "/cdp", derPath + "/upt"} {
		if status, _ := gateRequest(t, srv, http.MethodGet, path, victimLFDI, ""); status != http.StatusNotFound {
			t.Errorf("GET %s with a record naming a foreign or missing target: status %d, want 404", path, status)
		}
	}
}
