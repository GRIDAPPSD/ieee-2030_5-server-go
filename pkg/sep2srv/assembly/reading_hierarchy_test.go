package assembly_test

import (
	"bytes"
	"context"
	"net/http"
	"slices"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/assembly"
	coremetering "github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/handlers/metering"
)

// The reading hierarchy under a UsagePoint is served at the WADL addresses,
// listed in the Table 39 order, and scoped by every path value above it.
//
// Two parents are seeded wherever scope matters, for the reason argued in
// list_scope_test.go: only the pair separates "scoped" from "not scoped".

// scopeOf unwraps a scope key built from fixture values that are known good.
func scopeOf(key string, ok bool) string {
	if !ok {
		panic("fixture scope key refused: " + key)
	}
	return key
}

func seedHierarchy(t *testing.T, stores *assembly.Stores) {
	t.Helper()
	ctx := context.Background()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, upt := range []string{"u1", "u2"} {
		for _, mr := range []string{"m1", "m2"} {
			m := sep2.MeterReading{IdentifiedObject: sep2.IdentifiedObject{MRID: "MR-" + mr}}
			m.Href = "/upt/" + upt + "/mr/" + mr
			must(stores.MeterReadings.Create(ctx, upt, mr, m))
		}
	}
	// Under u1/m1: three sets whose start and mRID order differs from their id order.
	for _, s := range []struct {
		id, mrid string
		start    int64
	}{{"s1", "AA", 100}, {"s2", "BB", 300}, {"s3", "CC", 300}} {
		set := sep2.ReadingSet{
			IdentifiedObject: sep2.IdentifiedObject{MRID: s.mrid},
			TimePeriod:       sep2.DateTimeInterval{Start: s.start, Duration: 900},
		}
		set.Href = "/upt/u1/mr/m1/rs/" + s.id
		must(stores.ReadingSets.Create(ctx, scopeOf(coremetering.ReadingSetScopeKey("u1", "m1")), s.id, set))
	}
	other := sep2.ReadingSet{IdentifiedObject: sep2.IdentifiedObject{MRID: "ZZ"}}
	other.Href = "/upt/u2/mr/m1/rs/s1"
	must(stores.ReadingSets.Create(ctx, scopeOf(coremetering.ReadingSetScopeKey("u2", "m1")), "s1", other))

	// Under u1/m1/s2: readings whose localID order differs from their id order.
	for _, r := range []struct {
		id    string
		local sep2.HexBinary16
		val   int64
	}{{"r1", 3, 10}, {"r2", 1, 20}, {"r3", 2, 30}} {
		local := r.local
		val := r.val
		rd := sep2.Reading{LocalID: &local, Value: &val}
		rd.Href = "/upt/u1/mr/m1/rs/s2/r/" + r.id
		must(stores.Readings.Create(ctx, scopeOf(coremetering.ReadingScopeKey("u1", "m1", "s2")), r.id, rd))
	}
	val := int64(99)
	foreign := sep2.Reading{Value: &val}
	foreign.Href = "/upt/u2/mr/m1/rs/s1/r/r1"
	must(stores.Readings.Create(ctx, scopeOf(coremetering.ReadingScopeKey("u2", "m1", "s1")), "r1", foreign))

	// Readings listed directly under a MeterReading: localID order differs
	// from id order, and u2 holds one that must never appear under u1.
	for _, r := range []struct {
		id    string
		local sep2.HexBinary16
		val   int64
	}{{"d1", 3, 1}, {"d2", 1, 2}, {"d3", 2, 3}} {
		local := r.local
		val := r.val
		rd := sep2.Reading{LocalID: &local, Value: &val}
		rd.Href = "/upt/u1/mr/m1/r/" + r.id
		must(stores.Readings.Create(ctx, scopeOf(coremetering.ReadingSetScopeKey("u1", "m1")), r.id, rd))
	}
	direct := int64(77)
	must(stores.Readings.Create(ctx, scopeOf(coremetering.ReadingSetScopeKey("u2", "m1")), "d1", sep2.Reading{Value: &direct}))

	uom := sep2.UomWattHours
	rt := sep2.ReadingType{Uom: &uom}
	rt.Href = "/upt/u1/mr/m1/rt"
	must(stores.ReadingTypes.Create(ctx, "u1", "m1", rt))
}

func get(t *testing.T, url string) *http.Response {
	t.Helper()
	resp, err := http.Get(url) //nolint:gosec,noctx // httptest server, test only
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	return resp
}

func TestReadingSetList_TableThirtyNineOrderAndScope(t *testing.T) {
	t.Parallel()
	srv, stores := tmServer(t)
	seedHierarchy(t, stores)

	resp := get(t, srv.URL+"/upt/u1/mr/m1/rs")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var list sep2.ReadingSetList
	decodeXML(t, resp, &list)

	var got []string
	for _, s := range list.ReadingSet {
		got = append(got, s.MRID)
	}
	if want := []string{"CC", "BB", "AA"}; !slices.Equal(got, want) {
		t.Fatalf("members = %v, want %v (start descending, then mRID descending; none from u2)", got, want)
	}
	if list.All != 3 || list.Results != 3 {
		t.Errorf("all=%d results=%d, want 3 and 3", list.All, list.Results)
	}

	// Paging cuts the ordered collection, not the stored one.
	resp = get(t, srv.URL+"/upt/u1/mr/m1/rs?s=1&l=1")
	var page sep2.ReadingSetList
	decodeXML(t, resp, &page)
	if len(page.ReadingSet) != 1 || page.ReadingSet[0].MRID != "BB" || page.All != 3 || page.Results != 1 {
		t.Errorf("page s=1 l=1 = %+v, want the second member BB with all=3 results=1", page)
	}
}

func TestMeterReadingList_MRIDDescending(t *testing.T) {
	t.Parallel()
	srv, stores := tmServer(t)
	seedHierarchy(t, stores)

	var list sep2.MeterReadingList
	decodeXML(t, get(t, srv.URL+"/upt/u1/mr"), &list)
	if len(list.MeterReading) != 2 || list.MeterReading[0].Href != "/upt/u1/mr/m2" || list.MeterReading[1].Href != "/upt/u1/mr/m1" {
		t.Fatalf("MeterReadingList members = %+v, want m2 then m1 (mRID descending)", list.MeterReading)
	}
}

func TestReadingList_LocalIDAscendingUnderTheSet(t *testing.T) {
	t.Parallel()
	srv, stores := tmServer(t)
	seedHierarchy(t, stores)

	var list sep2.ReadingList
	decodeXML(t, get(t, srv.URL+"/upt/u1/mr/m1/rs/s2/r"), &list)
	var got []int64
	for _, r := range list.Reading {
		got = append(got, *r.Value)
	}
	if want := []int64{20, 30, 10}; !slices.Equal(got, want) {
		t.Fatalf("values = %v, want %v (localID 1, 2, 3; the u2 reading absent)", got, want)
	}

	var empty sep2.ReadingList
	decodeXML(t, get(t, srv.URL+"/upt/u1/mr/m1/rs/s1/r"), &empty)
	if len(empty.Reading) != 0 || empty.All != 0 {
		t.Errorf("a set with no readings served %d members, all=%d, want an empty list", len(empty.Reading), empty.All)
	}
}

func TestReadingHierarchyInstances_ServedAndScoped(t *testing.T) {
	t.Parallel()
	srv, stores := tmServer(t)
	seedHierarchy(t, stores)

	var mr sep2.MeterReading
	decodeXML(t, get(t, srv.URL+"/upt/u1/mr/m2"), &mr)
	if mr.Href != "/upt/u1/mr/m2" || mr.MRID != "MR-m2" {
		t.Errorf("MeterReading = href %q mRID %q, want /upt/u1/mr/m2 and MR-m2", mr.Href, mr.MRID)
	}

	var set sep2.ReadingSet
	decodeXML(t, get(t, srv.URL+"/upt/u1/mr/m1/rs/s2"), &set)
	if set.MRID != "BB" || set.TimePeriod.Start != 300 || set.TimePeriod.Duration != 900 {
		t.Errorf("ReadingSet = mRID %q period %+v, want BB and start 300 duration 900", set.MRID, set.TimePeriod)
	}

	var rd sep2.Reading
	decodeXML(t, get(t, srv.URL+"/upt/u1/mr/m1/rs/s2/r/r2"), &rd)
	if rd.Value == nil || *rd.Value != 20 || rd.LocalID == nil || *rd.LocalID != 1 {
		t.Errorf("Reading = %+v, want value 20 and localID 1", rd)
	}

	var rt sep2.ReadingType
	decodeXML(t, get(t, srv.URL+"/upt/u1/mr/m1/rt"), &rt)
	if rt.Href != "/upt/u1/mr/m1/rt" || rt.Uom == nil || *rt.Uom != sep2.UomWattHours {
		t.Errorf("ReadingType = %+v, want href /upt/u1/mr/m1/rt and uom %d", rt, sep2.UomWattHours)
	}

	// Each id is looked up under every parent above it: the same set and
	// reading ids under another parent, and a missing id, are 404.
	for _, path := range []string{
		"/upt/u3/mr/m1",
		"/upt/u1/mr/m3",
		"/upt/u2/mr/m1/rs/s2",
		"/upt/u1/mr/m2/rs/s2",
		"/upt/u1/mr/m1/rs/s2/r/r9",
		"/upt/u2/mr/m1/rs/s2/r/r2",
		"/upt/u1/mr/m1/rs/s1/r/r2",
		"/upt/u1/mr/m2/rt",
	} {
		resp := get(t, srv.URL+path)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("GET %s status = %d, want 404", path, resp.StatusCode)
		}
	}
}

func TestReadingHierarchy_HeadAndWriteMethods(t *testing.T) {
	t.Parallel()
	srv, stores := tmServer(t)
	seedHierarchy(t, stores)

	resp, err := http.Head(srv.URL + "/upt/u1/mr/m1/rs/s2/r/r2") //nolint:gosec,noctx // httptest server, test only
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("HEAD status = %d, want 200", resp.StatusCode)
	}

	// An unauthenticated write is refused with 403 by the manager-only check,
	// and a method the WADL marks Excluded answers 405. Neither changes a record.
	for _, tc := range []struct {
		method, path string
		want         int
	}{
		{http.MethodPut, "/upt/u1/mr/m1/rs/s2/r/r2", http.StatusForbidden},
		{http.MethodDelete, "/upt/u1/mr/m1/rs/s2", http.StatusForbidden},
		{http.MethodPut, "/upt/u1/mr/m1/rt", http.StatusForbidden},
		{http.MethodPost, "/upt/u1/mr/m1/rs", http.StatusMethodNotAllowed},
	} {
		req, err := http.NewRequest(tc.method, srv.URL+tc.path, nil) //nolint:noctx // test only
		if err != nil {
			t.Fatal(err)
		}
		resp, err := http.DefaultClient.Do(req) //nolint:gosec // httptest server
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != tc.want {
			t.Errorf("%s %s status = %d, want %d", tc.method, tc.path, resp.StatusCode, tc.want)
		}
	}
	if _, err := stores.Readings.Get(context.Background(), scopeOf(coremetering.ReadingScopeKey("u1", "m1", "s2")), "r2"); err != nil {
		t.Errorf("the Reading is gone after a refused DELETE of its set or PUT: %v", err)
	}
}

func TestMeterReadingReadingList_OrderedAndScoped(t *testing.T) {
	t.Parallel()
	srv, stores := tmServer(t)
	seedHierarchy(t, stores)

	var list sep2.ReadingList
	decodeXML(t, get(t, srv.URL+"/upt/u1/mr/m1/r"), &list)
	var got []int64
	for _, r := range list.Reading {
		got = append(got, *r.Value)
	}
	// localID 1, 2, 3 is ids d2, d3, d1; the u2 reading (77) and the readings
	// stored under the sets (10, 20, 30) must not appear.
	if want := []int64{2, 3, 1}; !slices.Equal(got, want) {
		t.Fatalf("values = %v, want %v", got, want)
	}
	if list.All != 3 || list.Results != 3 {
		t.Errorf("all=%d results=%d, want 3 and 3", list.All, list.Results)
	}
}

// The mux decodes %2F into a path value, so a value holding "/" must never be
// joined into a scope key: u1%2Fm1 + s2 would otherwise name the readings
// stored under the set u1/m1/s2.
func TestReadingHierarchy_ForgedScopeKeysRefused(t *testing.T) {
	t.Parallel()
	srv, stores := tmServer(t)
	seedHierarchy(t, stores)
	// MeterReadings stored under the forged ids, so the parent check passes and
	// only the refusal of "/" in a path value stands between the request and
	// the records.
	for _, id := range [][2]string{{"u1/m1", "s2"}, {"u1", "m1/s2"}, {"u1/m1/s2", "x"}} {
		if err := stores.MeterReadings.Create(context.Background(), id[0], id[1], sep2.MeterReading{}); err != nil {
			t.Fatal(err)
		}
	}

	for _, path := range []string{
		"/upt/u1%2Fm1/mr/s2/r",
		"/upt/u1/mr/m1%2Fs2/r",
		"/upt/u1%2Fm1/mr/s2/rs",
		"/upt/u1/mr/m1%2Fs2/rs",
		"/upt/u1%2Fm1/mr/s2/rs/r1",
		"/upt/u1%2Fm1/mr/s2/rs/x/r",
		"/upt/u1/mr/m1%2Fs2/rs/x/r/r1",
		"/upt/u1/mr/m1/rs/s2%2Fx/r",
		"/upt/u1%2Fm1%2Fs2/mr/x/rs/y/r/r1",
		"/upt/u1%2Fm1/mr/s2/rt",
	} {
		resp := get(t, srv.URL+path)
		body := new(bytes.Buffer)
		_, _ = body.ReadFrom(resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("GET %s status = %d, want 404", path, resp.StatusCode)
		}
		if bytes.Contains(body.Bytes(), []byte("<Reading")) {
			t.Errorf("GET %s returned reading records: %s", path, body.String())
		}
	}
}

// A MeterReading's ReadingType is reachable only under its UsagePoint.
func TestFlatReadingTypeRoutes_DoNotServeUsagePointTypes(t *testing.T) {
	t.Parallel()
	srv, stores := tmServer(t)
	seedHierarchy(t, stores)

	var list sep2.ReadingTypeList
	decodeXML(t, get(t, srv.URL+"/rt"), &list)
	if len(list.ReadingType) != 0 || list.All != 0 {
		t.Errorf("GET /rt listed %d records, all=%d, want none", len(list.ReadingType), list.All)
	}
	for _, path := range []string{"/rt/u1", "/rt/m1", "/rt/u1%2Fm1", "/rt/u1/m1"} {
		resp := get(t, srv.URL+path)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("GET %s status = %d, want 404", path, resp.StatusCode)
		}
	}
}

// A list under a parent that does not exist is a 404, not an empty 200.
func TestReadingHierarchy_ListUnderMissingParentIs404(t *testing.T) {
	t.Parallel()
	srv, stores := tmServer(t)
	seedHierarchy(t, stores)

	for _, path := range []string{
		"/upt/u1/mr/m9/rs",
		"/upt/u9/mr/m1/rs",
		"/upt/u1/mr/m9/r",
		"/upt/u9/mr/m1/r",
		"/upt/u1/mr/m1/rs/s9/r",
		"/upt/u1/mr/m9/rs/s1/r",
	} {
		resp := get(t, srv.URL+path)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("GET %s status = %d, want 404", path, resp.StatusCode)
		}
	}
}
