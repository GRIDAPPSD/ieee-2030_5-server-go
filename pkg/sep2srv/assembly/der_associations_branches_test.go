package assembly_test

import (
	"context"
	"encoding/xml"
	"net/http"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/assembly"
)

// branchServer serves stores where device victimID holds a DER whose stored
// record names its own program 1 and usage point 1, after edit has changed them.
func branchServer(t *testing.T, edit func(ctx context.Context, stores *assembly.Stores, der *sep2.DER)) func(path string) int {
	t.Helper()
	ctx := context.Background()
	stores := seededGateStores(t)
	seedManagedSubResources(t, stores, victimID)
	d, err := stores.DERs.Get(ctx, victimID, victimID)
	if err != nil {
		t.Fatalf("get DER: %v", err)
	}
	edit(ctx, stores, &d)
	if err := stores.DERs.Update(ctx, victimID, victimID, d); err != nil {
		t.Fatalf("update DER: %v", err)
	}
	srv := gateServer(t, stores, gateTestPolicy())
	return func(path string) int {
		status, _ := gateRequest(t, srv, http.MethodGet, path, victimLFDI, "")
		return status
	}
}

const branchDERPath = "/edev/" + victimID + "/der/" + victimID

func seedProgram(ctx context.Context, t *testing.T, stores *assembly.Stores, edev, id, href string) {
	t.Helper()
	p := sep2.DERProgram{}
	p.Href = href
	if err := stores.DERPrograms.Create(ctx, edev, id, p); err != nil {
		t.Fatalf("seed program %s: %v", href, err)
	}
}

// A current-program record resolves only to a program the device itself holds
// in the FSA the record names; every other shape is a 404, never a 200 for
// another device's program and never a 500.
func TestCurrentProgram_RecordResolution(t *testing.T) {
	t.Parallel()
	own := "/edev/" + victimID + "/fsa/" + victimID + "/derp/" + victimID
	cases := []struct {
		name string
		edit func(ctx context.Context, stores *assembly.Stores, der *sep2.DER)
		want int
	}{
		{"the device's own program in its FSA", func(context.Context, *assembly.Stores, *sep2.DER) {}, http.StatusOK},
		{"a program stored under another device", func(ctx context.Context, stores *assembly.Stores, der *sep2.DER) {
			other := "/edev/" + callerID + "/fsa/2/derp/2"
			seedProgram(ctx, t, stores, callerID, "2", other)
			der.CurrentDERProgramLink = &sep2.Link{Href: other}
		}, http.StatusNotFound},
		{"the device's own program under the wrong FSA", func(_ context.Context, _ *assembly.Stores, der *sep2.DER) {
			der.CurrentDERProgramLink = &sep2.Link{Href: "/edev/" + victimID + "/fsa/9/derp/" + victimID}
		}, http.StatusNotFound},
		{"a program that was deleted", func(_ context.Context, _ *assembly.Stores, der *sep2.DER) {
			der.CurrentDERProgramLink = &sep2.Link{Href: "/edev/" + victimID + "/fsa/" + victimID + "/derp/77"}
		}, http.StatusNotFound},
		{"a link that names no program", func(_ context.Context, _ *assembly.Stores, der *sep2.DER) {
			der.CurrentDERProgramLink = &sep2.Link{Href: "/dc/1"}
		}, http.StatusNotFound},
		{"no stored link", func(_ context.Context, _ *assembly.Stores, der *sep2.DER) {
			der.CurrentDERProgramLink = nil
		}, http.StatusNotFound},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			get := branchServer(t, tc.edit)
			if got := get(branchDERPath + "/cdp"); got != tc.want {
				t.Errorf("GET %s/cdp = %d, want %d (own program %s)", branchDERPath, got, tc.want, own)
			}
		})
	}
}

// An associated usage point resolves only from a link of the form /upt/{id}
// that names a stored usage point.
func TestAssociatedUsagePoint_RecordResolution(t *testing.T) {
	t.Parallel()
	seedUPT := func(id string) func(ctx context.Context, stores *assembly.Stores, der *sep2.DER) {
		return func(ctx context.Context, stores *assembly.Stores, der *sep2.DER) {
			u := sep2.UsagePoint{}
			u.Href = "/upt/" + id
			if err := stores.UsagePoints.Create(ctx, id, u); err != nil {
				t.Fatalf("seed usage point %q: %v", id, err)
			}
			der.AssociatedUsagePointLink = &sep2.Link{Href: "/upt/" + id}
		}
	}
	cases := []struct {
		name string
		edit func(ctx context.Context, stores *assembly.Stores, der *sep2.DER)
		want int
	}{
		{"a stored usage point", func(context.Context, *assembly.Stores, *sep2.DER) {}, http.StatusOK},
		{"a usage point that was deleted", func(_ context.Context, _ *assembly.Stores, der *sep2.DER) {
			der.AssociatedUsagePointLink = &sep2.Link{Href: "/upt/gone"}
		}, http.StatusNotFound},
		{"no stored link", func(_ context.Context, _ *assembly.Stores, der *sep2.DER) {
			der.AssociatedUsagePointLink = nil
		}, http.StatusNotFound},
		{"a link outside /upt/", func(ctx context.Context, stores *assembly.Stores, der *sep2.DER) {
			seedUPT("other")(ctx, stores, der)
			der.AssociatedUsagePointLink = &sep2.Link{Href: "/mup/other"}
		}, http.StatusNotFound},
		{"a nested id even when a record holds that key", func(ctx context.Context, stores *assembly.Stores, der *sep2.DER) {
			seedUPT("a/b")(ctx, stores, der)
		}, http.StatusNotFound},
		{"an empty id even when a record holds that key", func(ctx context.Context, stores *assembly.Stores, der *sep2.DER) {
			seedUPT("")(ctx, stores, der)
		}, http.StatusNotFound},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			get := branchServer(t, tc.edit)
			if got := get(branchDERPath + "/upt"); got != tc.want {
				t.Errorf("GET %s/upt = %d, want %d", branchDERPath, got, tc.want)
			}
		})
	}
}

// With no UsagePoint store wired, the DER does not advertise its /upt route,
// which the server then does not mount.
func TestAssociatedUsagePoint_NotAdvertisedWhenUsagePointsAreOff(t *testing.T) {
	t.Parallel()
	stores := seededGateStores(t)
	seedManagedSubResources(t, stores, victimID)
	stores.UsagePoints = nil
	srv := gateServer(t, stores, gateTestPolicy())

	status, raw := gateRequest(t, srv, http.MethodGet, branchDERPath, victimLFDI, "")
	if status != http.StatusOK {
		t.Fatalf("GET %s = %d; body=%s", branchDERPath, status, raw)
	}
	var d sep2.DER
	if err := xml.Unmarshal(raw, &d); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if d.AssociatedUsagePointLink != nil && d.AssociatedUsagePointLink.Href == branchDERPath+"/upt" {
		t.Errorf("AssociatedUsagePointLink = %q, a route this server does not mount", d.AssociatedUsagePointLink.Href)
	}
	if status, _ := gateRequest(t, srv, http.MethodGet, branchDERPath+"/upt", victimLFDI, ""); status != http.StatusNotFound {
		t.Errorf("GET %s/upt = %d, want 404 (unmounted)", branchDERPath, status)
	}
}

// Each served association body is the stored record, identified by its mRID
// as well as its href.
func TestDERAssociations_ServedBodiesCarryTheStoredMRID(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	stores := seededGateStores(t)
	seedManagedSubResources(t, stores, victimID)
	progHref := "/edev/" + victimID + "/fsa/" + victimID + "/derp/" + victimID
	prog := sep2.DERProgram{IdentifiedObject: sep2.IdentifiedObject{MRID: "00000000000000000000000000000AA1"}}
	prog.Href = progHref
	if err := stores.DERPrograms.Update(ctx, victimID, victimID, prog); err != nil {
		t.Fatalf("update program: %v", err)
	}
	upt := sep2.UsagePoint{IdentifiedObject: sep2.IdentifiedObject{MRID: "00000000000000000000000000000BB2"}}
	upt.Href = "/upt/" + victimID
	if err := stores.UsagePoints.Update(ctx, victimID, upt); err != nil {
		t.Fatalf("update usage point: %v", err)
	}
	curve := sep2.DERCurve{IdentifiedObject: sep2.IdentifiedObject{MRID: "00000000000000000000000000000CC3"}}
	curve.Href = "/dc/" + victimID
	if err := stores.DERCurves.Update(ctx, victimID, curve); err != nil {
		t.Fatalf("update curve: %v", err)
	}
	srv := gateServer(t, stores, gateTestPolicy())
	f := associationFixture{srvGet: func(path, asLFDI string) (int, []byte) {
		return gateRequest(t, srv, http.MethodGet, path, asLFDI, "")
	}, progHref: progHref}

	var gotProg sep2.DERProgram
	getOK(t, f, branchDERPath+"/cdp", &gotProg)
	if gotProg.MRID != prog.MRID || gotProg.Href != progHref {
		t.Errorf("current program mRID %q href %q, want %q %q", gotProg.MRID, gotProg.Href, prog.MRID, progHref)
	}
	var gotUpt sep2.UsagePoint
	getOK(t, f, branchDERPath+"/upt", &gotUpt)
	if gotUpt.MRID != upt.MRID || gotUpt.Href != upt.Href {
		t.Errorf("usage point mRID %q href %q, want %q %q", gotUpt.MRID, gotUpt.Href, upt.MRID, upt.Href)
	}
	var gotCurve sep2.DERCurve
	getOK(t, f, progHref+"/dc/"+victimID, &gotCurve)
	if gotCurve.MRID != curve.MRID || gotCurve.Href != progHref+"/dc/"+victimID {
		t.Errorf("curve mRID %q href %q, want %q %q", gotCurve.MRID, gotCurve.Href, curve.MRID, progHref+"/dc/"+victimID)
	}
	var list sep2.DERCurveList
	getOK(t, f, progHref+"/dc", &list)
	found := false
	for _, c := range list.DERCurve {
		if c.MRID == curve.MRID {
			found = c.Href == progHref+"/dc/"+victimID
		}
	}
	if !found {
		t.Errorf("curve list %+v does not carry mRID %s under %s/dc/%s", list.DERCurve, curve.MRID, progHref, victimID)
	}
	var progs sep2.DERProgramList
	getOK(t, f, branchDERPath+"/derp", &progs)
	found = false
	for _, p := range progs.DERProgram {
		if p.MRID == prog.MRID && p.Href == progHref {
			found = true
		}
	}
	if !found {
		t.Errorf("associated program list %+v does not carry mRID %s at %s", progs.DERProgram, prog.MRID, progHref)
	}
}
