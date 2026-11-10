package metering_test

import (
	"context"
	"encoding/xml"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/handlers/metering"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

const (
	listDeviceA  = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	listDeviceB  = "BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB"
	listManager  = "CCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCC"
	listManagedD = "DDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDD"
)

func get(h http.Handler, path string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
	return w
}

// mirrorListFixture holds one mirror each for devices A and B, and one for the
// device D that the manager manages.
func mirrorListFixture(t *testing.T) (*memory.Store[sep2.MirrorUsagePoint], *memory.EndDeviceManagementStore, *memory.EndDeviceStore) {
	t.Helper()
	ctx := context.Background()
	mups := memory.NewStore[sep2.MirrorUsagePoint]()
	for id, dev := range map[string]string{"m1": listDeviceA, "m2": listDeviceB, "m3": listManagedD, "m4": listDeviceA} {
		m := sep2.MirrorUsagePoint{IdentifiedObject: sep2.IdentifiedObject{MRID: "mrid-" + id}, DeviceLFDI: dev}
		m.Href = metering.MirrorHref(id)
		if err := mups.Create(ctx, id, m); err != nil {
			t.Fatal(err)
		}
	}
	orphan := sep2.MirrorUsagePoint{IdentifiedObject: sep2.IdentifiedObject{MRID: "mrid-orphan"}}
	orphan.Href = metering.MirrorHref("m5")
	if err := mups.Create(ctx, "m5", orphan); err != nil {
		t.Fatal(err)
	}
	managers := memory.NewEndDeviceManagementStore()
	if err := managers.Assign(ctx, listManager, listManagedD); err != nil {
		t.Fatal(err)
	}
	devs := memory.NewEndDeviceStore()
	if err := devs.Create(ctx, "d", sep2.EndDevice{LFDI: listManagedD}); err != nil {
		t.Fatal(err)
	}
	return mups, managers, devs
}

func decodeMirrors(t *testing.T, w *httptest.ResponseRecorder) sep2.MirrorUsagePointList {
	t.Helper()
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	var list sep2.MirrorUsagePointList
	if err := xml.Unmarshal(w.Body.Bytes(), &list); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return list
}

func mirrorMRIDs(list sep2.MirrorUsagePointList) []string {
	var out []string
	for _, m := range list.MirrorUsagePoint {
		out = append(out, m.MRID)
	}
	return out
}

func TestMirrorList_ShowsEachCallerOnlyWhatItMayActFor(t *testing.T) {
	t.Parallel()
	mups, managers, devs := mirrorListFixture(t)

	cases := []struct {
		name   string
		caller string
		want   []string
	}{
		{"device A sees its two", listDeviceA, []string{"mrid-m1", "mrid-m4"}},
		{"device B sees its one", listDeviceB, []string{"mrid-m2"}},
		{"the manager sees only the managed device's", listManager, []string{"mrid-m3"}},
		{"an unrelated device sees none", "EEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEE", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := metering.HandleMirrorUsagePointList(mups, managers, identityProvider(tc.caller), 300, metering.RequireDeviceRecords(devs))
			list := decodeMirrors(t, get(h, "/mup?l=255"))
			got := mirrorMRIDs(list)
			if fmt.Sprint(got) != fmt.Sprint(tc.want) {
				t.Errorf("listed %v, want %v", got, tc.want)
			}
			if int(list.All) != len(tc.want) || int(list.Results) != len(tc.want) {
				t.Errorf("all=%d results=%d, want both %d: a hidden mirror must not be counted", list.All, list.Results, len(tc.want))
			}
		})
	}
}

func TestMirrorList_ManagerWithoutADeviceRecordSeesNothingOfIt(t *testing.T) {
	t.Parallel()
	mups, managers, _ := mirrorListFixture(t)
	emptyDevs := memory.NewEndDeviceStore()
	h := metering.HandleMirrorUsagePointList(mups, managers, identityProvider(listManager), 300, metering.RequireDeviceRecords(emptyDevs))
	if got := mirrorMRIDs(decodeMirrors(t, get(h, "/mup"))); len(got) != 0 {
		t.Errorf("listed %v for a managed device with no EndDevice record, want none", got)
	}
}

func TestMirrorList_PagesOverTheVisibleList(t *testing.T) {
	t.Parallel()
	mups, managers, devs := mirrorListFixture(t)
	h := metering.HandleMirrorUsagePointList(mups, managers, identityProvider(listDeviceA), 300, metering.RequireDeviceRecords(devs))

	first := decodeMirrors(t, get(h, "/mup?l=1"))
	if got := mirrorMRIDs(first); fmt.Sprint(got) != "[mrid-m1]" || first.All != 2 {
		t.Errorf("l=1: listed %v all=%d, want [mrid-m1] all=2", got, first.All)
	}
	if got := mirrorMRIDs(decodeMirrors(t, get(h, "/mup?s=1&l=5"))); fmt.Sprint(got) != "[mrid-m4]" {
		t.Errorf("s=1: listed %v, want [mrid-m4]", got)
	}
	if got := mirrorMRIDs(decodeMirrors(t, get(h, "/mup?a=m1&l=5"))); fmt.Sprint(got) != "[mrid-m4]" {
		t.Errorf("a=m1: listed %v, want [mrid-m4]", got)
	}
	if got := mirrorMRIDs(decodeMirrors(t, get(h, "/mup?s=9"))); len(got) != 0 {
		t.Errorf("s=9: listed %v, want none", got)
	}
}

func TestMirrorList_FailsClosed(t *testing.T) {
	t.Parallel()
	mups, managers, _ := mirrorListFixture(t)

	for name, h := range map[string]http.Handler{
		"no identity":  metering.HandleMirrorUsagePointList(mups, managers, identityProvider(""), 300),
		"nil provider": metering.HandleMirrorUsagePointList(mups, managers, nil, 300),
	} {
		if w := get(h, "/mup"); w.Code != http.StatusForbidden {
			t.Errorf("%s: status = %d, want 403", name, w.Code)
		}
	}

	failing := metering.HandleMirrorUsagePointList(mups, erroringManagers{err: store.ErrInvalidManagementPair}, identityProvider(listManager), 300)
	if w := get(failing, "/mup"); w.Code != http.StatusInternalServerError {
		t.Errorf("management store failure: status = %d, want 500, not a list built from a failed check", w.Code)
	}
}

func decodeUsagePoints(t *testing.T, w *httptest.ResponseRecorder) sep2.UsagePointList {
	t.Helper()
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	var list sep2.UsagePointList
	if err := xml.Unmarshal(w.Body.Bytes(), &list); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return list
}

func TestUsagePointList_OnlyAManagerSeesTheRecords(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	upts := memory.NewStore[sep2.UsagePoint]()
	for i := 1; i <= 2; i++ {
		u := sep2.UsagePoint{IdentifiedObject: sep2.IdentifiedObject{MRID: "upt-" + strconv.Itoa(i)}}
		u.Href = metering.UsagePointHref(strconv.Itoa(i))
		if err := upts.Create(ctx, strconv.Itoa(i), u); err != nil {
			t.Fatal(err)
		}
	}
	managers := memory.NewEndDeviceManagementStore()
	if err := managers.Assign(ctx, listManager, listManagedD); err != nil {
		t.Fatal(err)
	}

	asManager := decodeUsagePoints(t, get(metering.HandleUsagePointList(upts, managers, identityProvider(listManager), 900), "/upt"))
	if asManager.All != 2 || len(asManager.UsagePoint) != 2 || asManager.UsagePoint[0].MRID != "upt-1" {
		t.Errorf("manager list: all=%d items=%d, want both records", asManager.All, len(asManager.UsagePoint))
	}

	for _, caller := range []string{listDeviceA, listDeviceB, listManagedD} {
		list := decodeUsagePoints(t, get(metering.HandleUsagePointList(upts, managers, identityProvider(caller), 900), "/upt"))
		if list.All != 0 || list.Results != 0 || len(list.UsagePoint) != 0 {
			t.Errorf("caller %s: all=%d results=%d items=%d, want an empty list", caller, list.All, list.Results, len(list.UsagePoint))
		}
		if list.Href != "/upt" || list.PollRate != 900 {
			t.Errorf("caller %s: empty list href=%q pollRate=%d, want the list's own href and rate", caller, list.Href, list.PollRate)
		}
	}
}

func TestUsagePointList_FailsClosed(t *testing.T) {
	t.Parallel()
	upts := memory.NewStore[sep2.UsagePoint]()
	managers := memory.NewEndDeviceManagementStore()
	if w := get(metering.HandleUsagePointList(upts, managers, identityProvider(""), 900), "/upt"); w.Code != http.StatusForbidden {
		t.Errorf("no identity: status = %d, want 403", w.Code)
	}
	if w := get(metering.HandleUsagePointList(upts, managers, nil, 900), "/upt"); w.Code != http.StatusForbidden {
		t.Errorf("nil provider: status = %d, want 403", w.Code)
	}
	failing := metering.HandleUsagePointList(upts, erroringManagers{err: store.ErrInvalidManagementPair}, identityProvider(listManager), 900)
	if w := get(failing, "/upt"); w.Code != http.StatusInternalServerError {
		t.Errorf("management store failure: status = %d, want 500", w.Code)
	}
	// Without a management store nobody is a manager: the list is empty.
	noStore := metering.HandleUsagePointList(upts, nil, identityProvider(listManager), 900)
	if w := get(noStore, "/upt"); w.Code != http.StatusOK {
		t.Errorf("no management store: status = %d, want 200 with an empty list", w.Code)
	}
}

func TestHandleCreateUsagePoint_CapRefusesNewRecordsButNotRepeats(t *testing.T) {
	t.Parallel()
	upts := memory.NewStore[sep2.UsagePoint]()
	mux := http.NewServeMux()
	mux.HandleFunc("POST /upt", metering.HandleCreateUsagePoint(upts, metering.WithMaxUsagePoints(2)))

	for _, mrid := range []string{"one", "two"} {
		if w := postUsagePoint(t, mux, mrid); w.Code != http.StatusCreated {
			t.Fatalf("POST %s: status = %d, want 201", mrid, w.Code)
		}
	}
	if w := postUsagePoint(t, mux, "three"); w.Code != http.StatusInsufficientStorage {
		t.Errorf("POST at the cap: status = %d, want 507", w.Code)
	}
	if n, _ := upts.Count(context.Background()); n != 2 {
		t.Errorf("stored %d usage points after a refused POST, want 2", n)
	}
	if w := postUsagePoint(t, mux, "one"); w.Code != http.StatusOK {
		t.Errorf("repeat POST of an existing mRID at the cap: status = %d, want 200", w.Code)
	}
	if w := postUsagePoint(t, mux, ""); w.Code != http.StatusInsufficientStorage {
		t.Errorf("POST with no mRID at the cap: status = %d, want 507", w.Code)
	}
}

func TestHandleCreateUsagePoint_DefaultCapApplies(t *testing.T) {
	t.Parallel()
	upts := memory.NewStore[sep2.UsagePoint]()
	mux := http.NewServeMux()
	mux.HandleFunc("POST /upt", metering.HandleCreateUsagePoint(upts))

	for i := 0; i < metering.DefaultMaxUsagePoints; i++ {
		if w := postUsagePoint(t, mux, "m"+strconv.Itoa(i)); w.Code != http.StatusCreated {
			t.Fatalf("POST %d: status = %d, want 201", i, w.Code)
		}
	}
	if w := postUsagePoint(t, mux, "overflow"); w.Code != http.StatusInsufficientStorage {
		t.Errorf("POST past DefaultMaxUsagePoints: status = %d, want 507", w.Code)
	}
}
