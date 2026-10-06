package adminplane

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/activity"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

type statusPayload struct {
	Error   string `json:"error"`
	Devices []struct {
		LFDI      string `json:"lfdi"`
		Comms     string `json:"comms"`
		LastKnown bool   `json:"lastKnown"`
		DERError  string `json:"derError"`
		DERs      []struct {
			ID       string `json:"id"`
			Reported bool   `json:"reported"`
			Connect  *struct {
				Source       string `json:"source"`
				Raw          uint8  `json:"raw"`
				Since        int64  `json:"since"`
				Connected    bool   `json:"connected"`
				Energized    *bool  `json:"energized"`
				Available    bool   `json:"available"`
				Operating    bool   `json:"operating"`
				Test         bool   `json:"test"`
				Fault        bool   `json:"fault"`
				ReservedBits uint8  `json:"reservedBits"`
				Also         *struct {
					Source    string `json:"source"`
					Raw       uint8  `json:"raw"`
					Connected bool   `json:"connected"`
				} `json:"also"`
			} `json:"connect"`
			Inverter *struct {
				Code  uint8 `json:"code"`
				Since int64 `json:"since"`
			} `json:"inverter"`
			ReadingTime   int64 `json:"readingTime"`
			AgeSeconds    int64 `json:"ageSeconds"`
			Stale         bool  `json:"stale"`
			NoReadingTime bool  `json:"noReadingTime"`
			ClockAhead    bool  `json:"clockAhead"`
		} `json:"ders"`
	} `json:"devices"`
}

type statusFixture struct {
	t        *testing.T
	edevs    *memory.EndDeviceStore
	ders     *memory.ScopedStore[sep2.DER]
	statuses *memory.ScopedStore[sep2.DERStatus]
}

func newStatusFixture(t *testing.T) *statusFixture {
	t.Helper()
	return &statusFixture{
		t:        t,
		edevs:    memory.NewEndDeviceStore(),
		ders:     memory.NewScopedStore[sep2.DER](),
		statuses: memory.NewScopedStore[sep2.DERStatus](),
	}
}

func (f *statusFixture) device(edevID, lfdi string) {
	f.t.Helper()
	dev := sep2.EndDevice{SFDI: "s" + edevID, LFDI: lfdi}
	dev.Href = "/edev/" + edevID
	if err := f.edevs.Create(context.Background(), edevID, dev); err != nil {
		f.t.Fatal(err)
	}
}

func (f *statusFixture) der(edevID, derID string, status *sep2.DERStatus) {
	f.t.Helper()
	d := sep2.DER{}
	d.Href = "/edev/" + edevID + "/der/" + derID
	if err := f.ders.Create(context.Background(), edevID, derID, d); err != nil {
		f.t.Fatal(err)
	}
	if status == nil {
		return
	}
	if err := f.statuses.Create(context.Background(), edevID+"/"+derID, "default", *status); err != nil {
		f.t.Fatal(err)
	}
}

func (f *statusFixture) get(rec *activity.Recorder, now time.Time) (int, string, statusPayload) {
	f.t.Helper()
	stores := dashboardTestStores(f.t, f.edevs)
	stores.DERs = f.ders
	stores.DERStatuses = f.statuses
	h := NewDashboardHandler(stores, "TLS").WithActivity(rec, 0)
	h.now = func() time.Time { return now }
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/dashboard/data", nil))
	var p statusPayload
	if err := json.Unmarshal(w.Body.Bytes(), &p); err != nil {
		f.t.Fatalf("body %q: %v", w.Body.String(), err)
	}
	return w.Code, w.Body.String(), p
}

var statusNow = time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)

func TestDashboardStatus_DecodesFieldByField(t *testing.T) {
	f := newStatusFixture(t)
	rt := statusNow.Unix() - 30
	f.device("1", "stor")
	f.der("1", "1", &sep2.DERStatus{
		ReadingTime:       rt,
		StorConnectStatus: &sep2.ConnectStatusType{DateTime: rt - 100, Value: 0x0B},
		InverterStatus:    &sep2.InverterStatusType{DateTime: rt - 50, Value: 8},
	})
	f.device("2", "gen")
	f.der("2", "1", &sep2.DERStatus{
		ReadingTime:      rt,
		GenConnectStatus: &sep2.ConnectStatusType{DateTime: rt - 200, Value: 0x11},
	})
	f.device("3", "v2023")
	f.der("3", "1", &sep2.DERStatus{
		ReadingTime:   rt,
		ConnectStatus: &sep2.ConnectStatusType2{DateTime: rt - 300, Value: 0x01},
	})

	code, _, p := f.get(nil, statusNow)
	if code != http.StatusOK || p.Error != "" {
		t.Fatalf("code %d error %q", code, p.Error)
	}
	if len(p.Devices) != 3 {
		t.Fatalf("devices = %d, want 3", len(p.Devices))
	}

	stor := p.Devices[0].DERs[0]
	if !stor.Reported || stor.Connect == nil || stor.Connect.Source != "storConnectStatus" ||
		stor.Connect.Raw != 0x0B || stor.Connect.Since != rt-100 || !stor.Connect.Connected ||
		!stor.Connect.Available || stor.Connect.Operating || !stor.Connect.Test || stor.Connect.Fault ||
		stor.Connect.Energized != nil {
		t.Errorf("2018 storage connect = %+v", stor.Connect)
	}
	if stor.Inverter == nil || stor.Inverter.Code != 8 || stor.Inverter.Since != rt-50 {
		t.Errorf("2018 storage inverter = %+v", stor.Inverter)
	}
	if stor.ReadingTime != rt || stor.AgeSeconds != 30 || stor.Stale || stor.NoReadingTime || stor.ClockAhead {
		t.Errorf("2018 storage freshness = %+v", stor)
	}

	gen := p.Devices[1].DERs[0]
	if gen.Connect == nil || gen.Connect.Source != "genConnectStatus" || gen.Connect.Raw != 0x11 ||
		!gen.Connect.Connected || !gen.Connect.Fault || gen.Connect.Since != rt-200 {
		t.Errorf("2018 generator connect = %+v", gen.Connect)
	}
	if gen.Inverter != nil {
		t.Errorf("2018 generator inverter = %+v, want null (not reported)", gen.Inverter)
	}

	v23 := p.Devices[2].DERs[0]
	if v23.Connect == nil || v23.Connect.Source != "connectStatus" || v23.Connect.Raw != 1 ||
		!v23.Connect.Connected || v23.Connect.Energized == nil || *v23.Connect.Energized ||
		v23.Connect.Since != rt-300 {
		t.Errorf("2023 connect = %+v", v23.Connect)
	}
}

func TestDashboardStatus_StaleBoundaryAtTwoPollRatesPlusSixty(t *testing.T) {
	cases := []struct {
		name  string
		age   int64
		stale bool
	}{
		{"limit minus 1 s", 1859, false},
		{"limit plus 1 s", 1861, true},
	}
	for _, tc := range cases {
		f := newStatusFixture(t)
		f.device("1", "d")
		f.der("1", "1", &sep2.DERStatus{
			ReadingTime:   statusNow.Unix() - tc.age,
			ConnectStatus: &sep2.ConnectStatusType2{Value: 1},
		})
		_, _, p := f.get(nil, statusNow)
		got := p.Devices[0].DERs[0]
		if got.Stale != tc.stale || got.AgeSeconds != tc.age {
			t.Errorf("%s: stale=%v age=%d, want %v and %d", tc.name, got.Stale, got.AgeSeconds, tc.stale, tc.age)
		}
	}
}

func TestDashboardStatus_NoReadingTimeAndClockAhead(t *testing.T) {
	f := newStatusFixture(t)
	f.device("1", "none")
	f.der("1", "1", &sep2.DERStatus{GenConnectStatus: &sep2.ConnectStatusType{Value: 1}})
	f.device("2", "ahead")
	f.der("2", "1", &sep2.DERStatus{
		ReadingTime:      statusNow.Unix() + 901,
		GenConnectStatus: &sep2.ConnectStatusType{Value: 1},
	})
	_, _, p := f.get(nil, statusNow)
	none, ahead := p.Devices[0].DERs[0], p.Devices[1].DERs[0]
	if !none.NoReadingTime || none.AgeSeconds != 0 || !none.Stale || none.ClockAhead {
		t.Errorf("readingTime 0 = %+v", none)
	}
	if !ahead.ClockAhead || ahead.AgeSeconds != 0 || !ahead.Stale || ahead.NoReadingTime {
		t.Errorf("clock ahead = %+v", ahead)
	}
}

func TestDashboardStatus_LastKnownFollowsComms(t *testing.T) {
	f := newStatusFixture(t)
	f.device("1", "seen")
	f.device("2", "never")
	for _, id := range []string{"1", "2"} {
		f.der(id, "1", &sep2.DERStatus{ReadingTime: statusNow.Unix(), GenConnectStatus: &sep2.ConnectStatusType{Value: 1}})
	}
	rec := activity.NewWithClock(func() time.Time { return statusNow })
	rec.Record("seen")

	cases := []struct {
		name string
		rec  *activity.Recorder
		now  time.Time
		want map[string]struct {
			comms string
			last  bool
		}
	}{
		{"within threshold", rec, statusNow.Add(activity.DefaultOfflineAfter - time.Second), map[string]struct {
			comms string
			last  bool
		}{"seen": {"online", false}, "never": {"not_seen", true}}},
		{"at threshold", rec, statusNow.Add(activity.DefaultOfflineAfter), map[string]struct {
			comms string
			last  bool
		}{"seen": {"offline", true}, "never": {"not_seen", true}}},
		{"no recorder wired", nil, statusNow, map[string]struct {
			comms string
			last  bool
		}{"seen": {"unknown", false}, "never": {"unknown", false}}},
	}
	for _, tc := range cases {
		_, _, p := f.get(tc.rec, tc.now)
		for _, d := range p.Devices {
			if w := tc.want[d.LFDI]; d.Comms != w.comms || d.LastKnown != w.last {
				t.Errorf("%s, %s: comms=%q lastKnown=%v, want %q and %v", tc.name, d.LFDI, d.Comms, d.LastKnown, w.comms, w.last)
			}
		}
	}
}

func TestDashboardStatus_NoDERsIsEmptyListNotNull(t *testing.T) {
	f := newStatusFixture(t)
	f.device("1", "bare")
	f.der("1", "9", nil)
	f.device("2", "empty")
	_, raw, p := f.get(nil, statusNow)
	if strings.Contains(raw, `"ders":null`) {
		t.Errorf("payload carries null ders: %s", raw)
	}
	if got := p.Devices[0].DERs; len(got) != 1 || got[0].ID != "9" || got[0].Reported || got[0].Connect != nil {
		t.Errorf("DER with no status = %+v, want id 9, reported false, null connect", got)
	}
	if !strings.Contains(raw, `"ders":[]`) {
		t.Errorf("device with no DERs lacks \"ders\":[] in %s", raw)
	}
}

func TestDashboardStatus_BothStorageAndGeneratorFieldsInPayload(t *testing.T) {
	f := newStatusFixture(t)
	f.device("1", "hybrid")
	f.der("1", "1", &sep2.DERStatus{
		ReadingTime:       statusNow.Unix(),
		GenConnectStatus:  &sep2.ConnectStatusType{Value: 0x00},
		StorConnectStatus: &sep2.ConnectStatusType{Value: 0x03},
	})
	_, _, p := f.get(nil, statusNow)
	c := p.Devices[0].DERs[0].Connect
	if c == nil || c.Source != "genConnectStatus" || c.Connected || c.Also == nil ||
		c.Also.Source != "storConnectStatus" || c.Also.Raw != 3 || !c.Also.Connected {
		t.Errorf("connect = %+v, want generator primary and storage in also", c)
	}
}

// Two DERs on one device report different statuses; each must come back
// under its own id, which fails if the status key is not built from the DER id.
func TestDashboardStatus_EachDERReadsItsOwnStatus(t *testing.T) {
	f := newStatusFixture(t)
	f.device("1", "two")
	f.der("1", "1", &sep2.DERStatus{ReadingTime: statusNow.Unix(), GenConnectStatus: &sep2.ConnectStatusType{Value: 0x01}, InverterStatus: &sep2.InverterStatusType{Value: 4}})
	f.der("1", "2", &sep2.DERStatus{ReadingTime: statusNow.Unix() - 10, GenConnectStatus: &sep2.ConnectStatusType{Value: 0x10}, InverterStatus: &sep2.InverterStatusType{Value: 7}})
	_, _, p := f.get(nil, statusNow)
	ders := p.Devices[0].DERs
	if len(ders) != 2 {
		t.Fatalf("ders = %d, want 2", len(ders))
	}
	byID := map[string]int{}
	for i, d := range ders {
		byID[d.ID] = i
	}
	one, two := ders[byID["1"]], ders[byID["2"]]
	if !one.Reported || !one.Connect.Connected || one.Connect.Fault || one.Inverter.Code != 4 || one.AgeSeconds != 0 {
		t.Errorf("DER 1 = %+v", one)
	}
	if !two.Reported || two.Connect.Connected || !two.Connect.Fault || two.Inverter.Code != 7 || two.AgeSeconds != 10 {
		t.Errorf("DER 2 = %+v", two)
	}
}

type failingStatuses struct {
	store.ScopedStore[sep2.DERStatus]
	failKey string
}

func (f failingStatuses) Get(ctx context.Context, parent, key string) (sep2.DERStatus, error) {
	if parent == f.failKey {
		return sep2.DERStatus{}, errors.New("disk gone")
	}
	return f.ScopedStore.Get(ctx, parent, key)
}

type failingDERs struct {
	store.ScopedStore[sep2.DER]
	failParent string
}

func (f failingDERs) List(ctx context.Context, parent string, opts store.ListOptions) (store.ListResult[sep2.DER], error) {
	if parent == f.failParent {
		return store.ListResult[sep2.DER]{}, errors.New("list broke")
	}
	return f.ScopedStore.List(ctx, parent, opts)
}

func TestDashboardStatus_ReadFailuresMarkOnlyTheirDevice(t *testing.T) {
	f := newStatusFixture(t)
	st := func() *sep2.DERStatus {
		return &sep2.DERStatus{ReadingTime: statusNow.Unix(), GenConnectStatus: &sep2.ConnectStatusType{Value: 1}}
	}
	f.device("1", "listfail")
	f.der("1", "1", st())
	f.device("2", "getfail")
	f.der("2", "1", st())
	f.der("2", "2", st())
	f.der("2", "3", st())
	f.device("3", "healthy")
	f.der("3", "1", st())
	f.der("3", "2", st())

	stores := dashboardTestStores(t, f.edevs)
	stores.DERs = failingDERs{f.ders, "1"}
	stores.DERStatuses = failingStatuses{f.statuses, "2/2"}
	h := NewDashboardHandler(stores, "TLS")
	h.now = func() time.Time { return statusNow }
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/dashboard/data", nil))
	var p statusPayload
	if err := json.Unmarshal(w.Body.Bytes(), &p); err != nil {
		t.Fatal(err)
	}
	if w.Code != http.StatusOK || p.Error != "" {
		t.Errorf("code %d error %q: a per-device read failure must not fail the payload", w.Code, p.Error)
	}
	byL := map[string]int{}
	for i, d := range p.Devices {
		byL[d.LFDI] = i
	}
	lf, gf, ok := p.Devices[byL["listfail"]], p.Devices[byL["getfail"]], p.Devices[byL["healthy"]]
	if !strings.Contains(lf.DERError, "list broke") || len(lf.DERs) != 0 {
		t.Errorf("list failure device: derError %q ders %d, want the error and an empty list", lf.DERError, len(lf.DERs))
	}
	if !strings.Contains(gf.DERError, "disk gone") || !strings.Contains(gf.DERError, "2") {
		t.Errorf("get failure device derError = %q, want it to name DER 2 and the cause", gf.DERError)
	}
	ids := []string{}
	for _, d := range gf.DERs {
		ids = append(ids, d.ID)
	}
	if strings.Join(ids, ",") != "1,3" && strings.Join(ids, ",") != "3,1" {
		t.Errorf("get failure device kept DERs %v, want 1 and 3", ids)
	}
	if ok.DERError != "" || len(ok.DERs) != 2 {
		t.Errorf("healthy device = derError %q, %d DERs, want none and 2", ok.DERError, len(ok.DERs))
	}
}

type stallStatuses struct {
	store.ScopedStore[sep2.DERStatus]
}

func (stallStatuses) Get(ctx context.Context, _, _ string) (sep2.DERStatus, error) {
	<-ctx.Done()
	return sep2.DERStatus{}, ctx.Err()
}

func stalledHandler(t *testing.T) *DashboardHandler {
	t.Helper()
	f := newStatusFixture(t)
	f.device("1", "d")
	f.der("1", "1", nil)
	stores := dashboardTestStores(t, f.edevs)
	stores.DERs = f.ders
	stores.DERStatuses = stallStatuses{f.statuses}
	return NewDashboardHandler(stores, "TLS")
}

func TestDashboardCollect_StalledStoreIsBounded(t *testing.T) {
	h := stalledHandler(t)
	h.collectTimeout = 50 * time.Millisecond
	start := time.Now()
	data := h.collectData(context.Background())
	if el := time.Since(start); el > 3*time.Second {
		t.Fatalf("collectData took %v with a stalled store, want it bounded by the 50ms timeout", el)
	}
	if len(data.Devices) != 1 || data.Devices[0].DERError == "" {
		t.Errorf("devices = %+v, want the stalled device marked with a derError", data.Devices)
	}
}

func TestDashboardCollect_ClientCancelStopsIt(t *testing.T) {
	h := stalledHandler(t)
	h.collectTimeout = time.Minute
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(30 * time.Millisecond)
		cancel()
	}()
	start := time.Now()
	h.collectData(ctx)
	if el := time.Since(start); el > 3*time.Second {
		t.Fatalf("collectData took %v after cancel, want it to stop at once", el)
	}
}

func TestDashboardCollect_DefaultTimeoutIsFourSeconds(t *testing.T) {
	h := NewDashboardHandler(dashboardTestStores(t, memory.NewEndDeviceStore()), "TLS")
	if h.collectTimeout != 4*time.Second {
		t.Errorf("collectTimeout = %v, want 4s", h.collectTimeout)
	}
}
