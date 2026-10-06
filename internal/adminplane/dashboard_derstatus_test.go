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
	if !ahead.ClockAhead || ahead.AgeSeconds != 0 || ahead.Stale || ahead.NoReadingTime {
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

type failingStatuses struct {
	store.ScopedStore[sep2.DERStatus]
}

func (failingStatuses) Get(context.Context, string, string) (sep2.DERStatus, error) {
	return sep2.DERStatus{}, errors.New("disk gone")
}

func TestDashboardStatus_StatusReadFailureReachesPayloadError(t *testing.T) {
	f := newStatusFixture(t)
	f.device("1", "d")
	f.der("1", "1", nil)
	stores := dashboardTestStores(t, f.edevs)
	stores.DERs = f.ders
	stores.DERStatuses = failingStatuses{f.statuses}
	code, body := getDashboardData(t, stores)
	if code != http.StatusInternalServerError || !strings.Contains(body["error"].(string), "disk gone") {
		t.Errorf("code %d body %v, want 500 naming the read failure", code, body)
	}
}
