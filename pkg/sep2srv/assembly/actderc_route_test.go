package assembly_test

import (
	"context"
	"encoding/xml"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/dercontrol"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/derhref"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/assembly"
)

const actProgramPath = "/edev/e1/fsa/f1/derp/p1"

// seedActiveFixture stores one program with seven controls spanning every
// status, and returns the mRIDs of the active ones in the order the control
// list serves them (interval.start ascending).
func seedActiveFixture(t *testing.T, stores *assembly.Stores) []string {
	t.Helper()
	ctx := context.Background()
	now := time.Now().Unix()

	prog := sep2.DERProgram{MRID: "PROG", Primacy: 1}
	prog.Href = actProgramPath
	prog.DERControlListLink = &sep2.ListLink{Href: actProgramPath + "/derc"}
	if err := stores.DERPrograms.Create(ctx, "e1", "p1", prog); err != nil {
		t.Fatalf("seed program: %v", err)
	}

	scope := derhref.ScopeKey("e1", "f1", "p1")
	put := func(id string, start, dur int64, lc *dercontrol.LifecycleRecord) {
		c := sep2.DERControl{DERControlBase: &sep2.DERControlBase{OpModTargetW: &sep2.ActivePower{Value: 1}}}
		c.Href = actProgramPath + "/derc/" + id
		c.MRID = id
		c.Interval = &sep2.DateTimeInterval{Start: start, Duration: uint32(dur)}
		c.CreationTime = start - 100
		if err := stores.DERControls.Create(ctx, scope, id, c); err != nil {
			t.Fatalf("seed control %s: %v", id, err)
		}
		if lc != nil {
			if err := stores.DERControlLifecycles.Create(ctx, scope, id, *lc); err != nil {
				t.Fatalf("seed lifecycle %s: %v", id, err)
			}
		}
	}
	cancelled, superseded := now-50, now-50
	put("A3", now-100, 3600, nil)
	put("A1", now-500, 3600, nil)
	put("A2", now-300, 3600, nil)
	put("ENDED", now-10000, 100, nil)
	put("SCHEDULED", now+3600, 600, nil)
	put("CANCELLED", now-200, 3600, &dercontrol.LifecycleRecord{CancelledAt: &cancelled})
	put("SUPERSEDED", now-200, 3600, &dercontrol.LifecycleRecord{SupersededAt: &superseded})
	return []string{"A1", "A2", "A3"}
}

func getBody(t *testing.T, srv *httptest.Server, path string) (int, []byte) {
	t.Helper()
	resp, err := srv.Client().Get(srv.URL + path)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return resp.StatusCode, raw
}

func activeServer(t *testing.T) (*httptest.Server, []string) {
	t.Helper()
	stores := testStores()
	seedOwnedDevices(t, stores.EndDevices, "e1")
	want := seedActiveFixture(t, stores)
	handler, _ := assembly.BuildProtocolRouter(assembly.RouterConfig{}, stores, testAuthPolicy(), "serverSFDI", "serverLFDI", nil)
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return srv, want
}

func decodeControlList(t *testing.T, raw []byte) sep2.DERControlList {
	t.Helper()
	var list sep2.DERControlList
	if err := xml.Unmarshal(raw, &list); err != nil {
		t.Fatalf("decode DERControlList: %v; body=%s", err, raw)
	}
	return list
}

func controlMRIDs(list sep2.DERControlList) []string {
	var ids []string
	for _, c := range list.DERControl {
		ids = append(ids, c.MRID)
	}
	return ids
}

// TestActiveDERControlList_HoldsExactlyTheActiveControls pins the served
// membership: ended, scheduled, cancelled and superseded controls are absent,
// the order is the control list's, and all counts only the active members.
func TestActiveDERControlList_HoldsExactlyTheActiveControls(t *testing.T) {
	t.Parallel()
	srv, want := activeServer(t)

	status, raw := getBody(t, srv, actProgramPath+"/actderc")
	if status != http.StatusOK {
		t.Fatalf("GET actderc status = %d, want 200; body=%s", status, raw)
	}
	list := decodeControlList(t, raw)
	if got := controlMRIDs(list); !slices.Equal(got, want) {
		t.Errorf("active mRIDs = %v, want %v", got, want)
	}
	if list.All != 3 || list.Results != 3 {
		t.Errorf("all=%d results=%d, want 3 and 3", list.All, list.Results)
	}
	if list.Href != actProgramPath+"/actderc" {
		t.Errorf("list href = %q, want the request path", list.Href)
	}

	// The control list still holds all seven: the filter is not a deletion.
	_, rawAll := getBody(t, srv, actProgramPath+"/derc?l=100")
	if all := decodeControlList(t, rawAll); all.All != 7 {
		t.Errorf("derc all = %d, want 7 (the active filter must not touch the control list)", all.All)
	}
}

// TestActiveDERControlList_PagesTheFilteredCollection proves start and limit
// cut the active members, not the whole list.
func TestActiveDERControlList_PagesTheFilteredCollection(t *testing.T) {
	t.Parallel()
	srv, _ := activeServer(t)

	cases := []struct {
		query   string
		wantIDs []string
		results uint32
	}{
		{"?s=1&l=1", []string{"A2"}, 1},
		{"?s=2&l=5", []string{"A3"}, 1},
		{"?s=3&l=5", nil, 0},
		{"?l=0", nil, 0},
	}
	for _, tc := range cases {
		status, raw := getBody(t, srv, actProgramPath+"/actderc"+tc.query)
		if status != http.StatusOK {
			t.Fatalf("GET actderc%s status = %d; body=%s", tc.query, status, raw)
		}
		list := decodeControlList(t, raw)
		if got := controlMRIDs(list); !slices.Equal(got, tc.wantIDs) {
			t.Errorf("%s: mRIDs = %v, want %v", tc.query, got, tc.wantIDs)
		}
		if list.All != 3 || list.Results != tc.results {
			t.Errorf("%s: all=%d results=%d, want 3 and %d", tc.query, list.All, list.Results, tc.results)
		}
	}
}

// TestDERProgram_AdvertisesActiveDERControlListLink proves the link is served,
// points at the mounted route, and carries the live active count.
func TestDERProgram_AdvertisesActiveDERControlListLink(t *testing.T) {
	t.Parallel()
	srv, _ := activeServer(t)

	status, raw := getBody(t, srv, actProgramPath)
	if status != http.StatusOK {
		t.Fatalf("GET program status = %d; body=%s", status, raw)
	}
	var prog sep2.DERProgram
	if err := xml.Unmarshal(raw, &prog); err != nil {
		t.Fatalf("decode DERProgram: %v", err)
	}
	if prog.ActiveDERControlListLink == nil {
		t.Fatalf("ActiveDERControlListLink absent; body=%s", raw)
	}
	if got := prog.ActiveDERControlListLink.Href; got != actProgramPath+"/actderc" {
		t.Errorf("ActiveDERControlListLink.Href = %q, want %q", got, actProgramPath+"/actderc")
	}
	if got := prog.ActiveDERControlListLink.All; got != 3 {
		t.Errorf("ActiveDERControlListLink.All = %d, want 3", got)
	}
	if prog.DERControlListLink == nil || prog.DERControlListLink.All != 7 {
		t.Errorf("DERControlListLink = %+v, want all 7", prog.DERControlListLink)
	}
}
