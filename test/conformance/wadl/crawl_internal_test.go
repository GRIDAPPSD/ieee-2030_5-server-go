package wadl

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync"
	"testing"
)

// These tests need no WADL copy and no schema copy: the schema and the model
// are small synthetic documents.

const syntheticXSD = `<xs:schema xmlns:xs="http://www.w3.org/2001/XMLSchema">
  <xs:complexType name="DeviceCapability"><xs:sequence>
    <xs:element name="ThingListLink" type="ThingListLink"/>
    <xs:element name="SoloLink" type="SoloLink"/>
  </xs:sequence></xs:complexType>
  <xs:complexType name="Base"/>
  <xs:complexType name="Sub"><xs:complexContent><xs:extension base="sep:Base"><xs:sequence/></xs:extension></xs:complexContent></xs:complexType>
  <xs:complexType name="BaseListHolder"><xs:sequence><xs:element name="BaseListLink" type="BaseListLink"/></xs:sequence></xs:complexType>
  <xs:element name="GlobalOnlyListLink" type="GlobalOnlyListLink"/>
</xs:schema>`

func TestParseLinkSchemaNamesOnlyDeclaredLinks(t *testing.T) {
	s, err := ParseLinkSchema([]byte(syntheticXSD))
	if err != nil {
		t.Fatalf("ParseLinkSchema: %v", err)
	}
	tests := []struct {
		id   string
		want bool
	}{
		{"ThingList", true}, // its own Link field
		{"Thing", true},     // member of a named list
		{"Solo", true},
		{"Base", true},            // BaseListLink
		{"Sub", true},             // named through its base type
		{"GlobalOnlyList", false}, // a global element is not a Link field
		{"Unrelated", false},
		{"", false},
	}
	for _, tc := range tests {
		if got := s.Names(tc.id); got != tc.want {
			t.Errorf("Names(%q) = %v, want %v", tc.id, got, tc.want)
		}
	}
}

func TestParseLinkSchemaRejectsADocumentWithNoLinks(t *testing.T) {
	if _, err := ParseLinkSchema([]byte(`<xs:schema xmlns:xs="http://www.w3.org/2001/XMLSchema"/>`)); err == nil {
		t.Fatal("a schema with no Link fields parsed without error")
	}
}

func TestParseLinksReadsXsiType(t *testing.T) {
	body := []byte(`<ResponseList xmlns="urn:ieee:std:2030.5:ns" xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance" href="/r">` +
		`<Response href="/r/1" xsi:type="DERControlResponse"/><Response href="/r/2"/><ThingLink href="/t"/></ResponseList>`)
	got := map[string]string{}
	for _, l := range parseLinks(body, "/r") {
		got[l.href] = l.resourceID()
	}
	want := map[string]string{"/r/1": "DERControlResponse", "/r/2": "Response", "/t": "Thing"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("resource ids by href = %v, want %v", got, want)
	}
}

func TestResourceStates(t *testing.T) {
	m := &Model{Methods: []Method{
		{ResourceID: "Thing", Path: "/thing", Verb: "GET", Mode: ModeMandatory},
		{ResourceID: "Thing", Path: "/thing", Verb: "PUT", Mode: ModeOptional},
		{ResourceID: "ThingList", Path: "/thing/list", Verb: "GET", Mode: ModeMandatory},
		{ResourceID: "Unlinked", Path: "/unlinked", Verb: "GET", Mode: ModeMandatory},
		{ResourceID: "Dep", Path: "/dep", Verb: "GET", Mode: ModeDeprecated},
	}}
	schema, err := ParseLinkSchema([]byte(syntheticXSD))
	if err != nil {
		t.Fatalf("ParseLinkSchema: %v", err)
	}
	c := newCrawl()
	c.add("Thing", "/server/chosen/7")

	states, rows := resourceStates(m, c, schema)
	want := map[resKey]reachState{
		{"Thing", "/thing"}:          stateReached,
		{"ThingList", "/thing/list"}: stateNotReached,
		{"Unlinked", "/unlinked"}:    stateSamplePath,
	}
	if !reflect.DeepEqual(states, want) {
		t.Errorf("states = %v, want %v", states, want)
	}
	if len(rows) != 1 || rows[0].Verdict != VerdictNotReached || rows[0].ResourceID != "ThingList" ||
		rows[0].Mode != ModeMandatory || rows[0].WADLPath != "/thing/list" {
		t.Errorf("not_reached rows = %+v, want one Mandatory row for ThingList", rows)
	}

	strict, strictRows := resourceStates(m, c, nil)
	if strict[resKey{"Unlinked", "/unlinked"}] != stateNotReached || len(strictRows) != 2 {
		t.Errorf("without a schema: Unlinked state %v, %d not_reached rows; want not reached and 2 rows",
			strict[resKey{"Unlinked", "/unlinked"}], len(strictRows))
	}
}

// fakeServer serves a /dcap whose links lead to /things (a list with one member
// at a server-chosen href) and to /gone, which answers 404.
func fakeServer(t *testing.T, dcap string) *Target {
	t.Helper()
	mux := http.NewServeMux()
	body := func(s string) http.HandlerFunc {
		return func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(s)) }
	}
	ns := ` xmlns="urn:ieee:std:2030.5:ns"`
	mux.HandleFunc("GET /dcap", body(dcap))
	mux.HandleFunc("GET /things", body(`<ThingList`+ns+` href="/things"><Thing href="/srv/9"/></ThingList>`))
	mux.HandleFunc("GET /srv/9", body(`<Thing`+ns+` href="/srv/9"/>`))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return &Target{BaseURL: srv.URL, Client: srv.Client()}
}

func fakeModel() *Model {
	return &Model{Methods: []Method{
		{ResourceID: "Thing", Path: "/things/{id1}", Verb: "GET", Mode: ModeMandatory, DeclaredElements: []string{"Thing"}},
		{ResourceID: "ThingList", Path: "/things", Verb: "GET", Mode: ModeMandatory, DeclaredElements: []string{"ThingList"}},
		{ResourceID: "Orphan", Path: "/orphan", Verb: "GET", Mode: ModeMandatory},
	}}
}

func TestSweepScoresAResourceAtTheHrefAndReportsWhatNoLinkReaches(t *testing.T) {
	const dcap = `<DeviceCapability xmlns="urn:ieee:std:2030.5:ns" href="/dcap">` +
		`<ThingListLink href="/things"/><GoneLink href="/gone"/></DeviceCapability>`
	tgt := fakeServer(t, dcap)
	schema, err := ParseLinkSchema([]byte(syntheticXSD))
	if err != nil {
		t.Fatalf("ParseLinkSchema: %v", err)
	}
	tgt.Links = schema

	res := Sweep(tgt, fakeModel())

	var thing *Row
	for i, r := range res.Rows {
		if r.Phase == PhaseWADL && r.ResourceID == "Thing" {
			thing = &res.Rows[i]
		}
	}
	if thing == nil {
		t.Fatal("no WADL row for Thing")
	}
	if thing.Path != "/srv/9" || thing.IDSource != "crawled" || thing.Verdict != VerdictConformant {
		t.Errorf("Thing row = path %q source %q verdict %q, want /srv/9 crawled conformant",
			thing.Path, thing.IDSource, thing.Verdict)
	}
	if got := res.Count(PhaseWADL, VerdictNotReached); got != 0 {
		t.Errorf("not_reached rows = %d, want 0: Orphan has no schema link and is probed at its sample path", got)
	}
	if res.CrawlReached != 2 {
		t.Errorf("CrawlReached = %d, want 2 (ThingList and Thing)", res.CrawlReached)
	}

	tgt.Links = nil
	strict := Sweep(tgt, fakeModel())
	if got := strict.Count(PhaseWADL, VerdictNotReached); got != 1 {
		t.Errorf("without a schema: not_reached rows = %d, want 1 (Orphan)", got)
	}
	if got := strict.NotReachedMethods(fakeModel(), ModeMandatory); got != 1 {
		t.Errorf("NotReachedMethods = %d, want 1", got)
	}
}

func TestSweepCrawlReachingNothingReportsZero(t *testing.T) {
	// /dcap answers but carries no link: nothing is followed.
	tgt := fakeServer(t, `<DeviceCapability xmlns="urn:ieee:std:2030.5:ns" href="/dcap"/>`)
	schema, err := ParseLinkSchema([]byte(syntheticXSD))
	if err != nil {
		t.Fatalf("ParseLinkSchema: %v", err)
	}
	tgt.Links = schema

	res := Sweep(tgt, fakeModel())

	if res.CrawlReached != 0 {
		t.Errorf("CrawlReached = %d, want 0 for a /dcap with no links", res.CrawlReached)
	}
	if got := res.Count(PhaseWADL, VerdictNotReached); got != 2 {
		t.Errorf("not_reached rows = %d, want 2 (Thing and ThingList)", got)
	}
}

// linkServer serves the given bodies by path and counts every GET per path.
func linkServer(t *testing.T, bodies map[string]string) (*Target, func(string) int) {
	t.Helper()
	var mu sync.Mutex
	hits := map[string]int{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, ok := bodies[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		if r.Method == http.MethodGet {
			mu.Lock()
			hits[r.URL.Path]++
			mu.Unlock()
		}
		_, _ = w.Write([]byte(b))
	}))
	t.Cleanup(srv.Close)
	return &Target{BaseURL: srv.URL, Client: srv.Client()}, func(p string) int {
		mu.Lock()
		defer mu.Unlock()
		return hits[p]
	}
}

// A diamond (/c advertised by both /a and /b) and a cycle (/c links back to /a
// and /dcap) must each fetch every resource once in the discovery phase. The
// diamond fails if the pop-time guard in phaseDiscovery goes; the cycle fails
// if both guards go.
func TestDiscoveryFetchesEachResourceOnceAcrossADiamondAndACycle(t *testing.T) {
	ns := ` xmlns="urn:ieee:std:2030.5:ns"`
	tgt, _ := linkServer(t, map[string]string{
		"/dcap": `<DeviceCapability` + ns + ` href="/dcap"><ALink href="/a"/><BLink href="/b"/></DeviceCapability>`,
		"/a":    `<A` + ns + ` href="/a"><CLink href="/c"/></A>`,
		"/b":    `<B` + ns + ` href="/b"><CLink href="/c"/></B>`,
		"/c":    `<C` + ns + ` href="/c"><ALink href="/a"/><DCapLink href="/dcap"/></C>`,
	})

	res := Sweep(tgt, &Model{})

	got := map[string]int{}
	for _, r := range res.Rows {
		if r.Phase == PhaseDiscovery {
			got[r.Path]++
		}
	}
	want := map[string]int{"/dcap": 1, "/a": 1, "/b": 1, "/c": 1}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("discovery rows per path = %v, want %v", got, want)
	}
	if n := res.LinksFollowed(); n != 3 {
		t.Errorf("LinksFollowed = %d, want 3 (/a, /b, /c)", n)
	}
}

// With the model naming /dcap itself, a /dcap that carries no parsable link
// still counts as one reached resource; only LinksFollowed shows the crawl
// went nowhere.
func TestCrawlThatFollowsNoLinkIsVisibleDespiteReachingDcap(t *testing.T) {
	tgt, _ := linkServer(t, map[string]string{
		"/dcap": `<DeviceCapability xmlns="urn:ieee:std:2030.5:ns" href="/dcap"/>`,
	})
	m := &Model{Methods: []Method{
		{ResourceID: "DeviceCapability", Path: "/dcap", Verb: "GET", Mode: ModeMandatory, DeclaredElements: []string{"DeviceCapability"}},
	}}

	res := Sweep(tgt, m)

	if res.CrawlReached != 1 {
		t.Fatalf("CrawlReached = %d, want 1 (/dcap alone): the precondition of this test", res.CrawlReached)
	}
	if n := res.LinksFollowed(); n != 0 {
		t.Errorf("LinksFollowed = %d, want 0", n)
	}
}
