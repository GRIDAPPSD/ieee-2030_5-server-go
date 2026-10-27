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
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/dercontrol"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

type controlPayload struct {
	Error   string `json:"error"`
	Devices []struct {
		LFDI         string `json:"lfdi"`
		UnderControl *bool  `json:"underControl"`
		ControlError string `json:"controlError"`
	} `json:"devices"`
}

type controlFixture struct {
	t          *testing.T
	edevs      *memory.EndDeviceStore
	controls   *memory.DERControlStore
	lifecycles store.ScopedStore[dercontrol.LifecycleRecord]
}

func newControlFixture(t *testing.T) *controlFixture {
	t.Helper()
	return &controlFixture{
		t:          t,
		edevs:      memory.NewEndDeviceStore(),
		controls:   memory.NewDERControlStore(),
		lifecycles: memory.NewScopedStore[dercontrol.LifecycleRecord](),
	}
}

func (f *controlFixture) device(id string) {
	f.t.Helper()
	dev := sep2.EndDevice{SFDI: "s" + id, LFDI: "lfdi" + id}
	dev.Href = "/edev/" + id
	if err := f.edevs.Create(context.Background(), id, dev); err != nil {
		f.t.Fatal(err)
	}
}

// control stores a control under the scope (edev, fsa, derp) with its
// lifecycle record, when lc is not nil.
func (f *controlFixture) control(edev, fsa, id string, iv *sep2.DateTimeInterval, status *sep2.EventStatus, lc *dercontrol.LifecycleRecord) {
	f.t.Helper()
	key := edev + "/" + fsa + "/p1"
	c := sep2.DERControl{}
	c.Href = "/edev/" + edev + "/fsa/" + fsa + "/derp/p1/derc/" + id
	c.Interval, c.EventStatus = iv, status
	if err := f.controls.Create(context.Background(), key, id, c); err != nil {
		f.t.Fatal(err)
	}
	if lc != nil {
		if err := f.lifecycles.Create(context.Background(), key, id, *lc); err != nil {
			f.t.Fatal(err)
		}
	}
}

func (f *controlFixture) get(stores func(*Stores)) (string, controlPayload) {
	f.t.Helper()
	s := dashboardTestStores(f.t, f.edevs)
	s.DERControls = f.controls
	s.DERControlLifecycles = f.lifecycles
	if stores != nil {
		stores(s)
	}
	h := NewDashboardHandler(s, "TLS")
	h.now = func() time.Time { return statusNow }
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/dashboard/data", nil))
	var p controlPayload
	if err := json.Unmarshal(w.Body.Bytes(), &p); err != nil {
		f.t.Fatalf("body %q: %v", w.Body.String(), err)
	}
	return w.Body.String(), p
}

func TestDashboardUnderControl_PerDeviceValues(t *testing.T) {
	now := statusNow.Unix()
	f := newControlFixture(t)
	for _, id := range []string{"1", "2", "3", "4", "5", "6", "7"} {
		f.device(id)
	}
	live := &sep2.DateTimeInterval{Start: now - 10, Duration: 600}
	f.control("1", "0", "c1", live, nil, &dercontrol.LifecycleRecord{})
	f.control("2", "0", "c1", &sep2.DateTimeInterval{Start: now + 1000, Duration: 60}, nil, &dercontrol.LifecycleRecord{})
	f.control("3", "0", "c1", &sep2.DateTimeInterval{Start: 1_530_000_000, Duration: 60},
		&sep2.EventStatus{CurrentStatus: sep2.EventStatusActive}, &dercontrol.LifecycleRecord{})
	cancelled := now - 5
	f.control("4", "0", "c1", live, nil, &dercontrol.LifecycleRecord{CancelledAt: &cancelled})
	f.control("5", "0", "c1", live, nil, &dercontrol.LifecycleRecord{SupersededAt: &cancelled})
	// Device 6 holds a copy of a control sent to its FSA, under its own scope.
	f.control("6", "fsa", "c1", live, nil, &dercontrol.LifecycleRecord{})
	// Device 7 has no control at all.

	_, p := f.get(nil)
	if p.Error != "" || len(p.Devices) != 7 {
		t.Fatalf("error %q, %d devices; want none and 7", p.Error, len(p.Devices))
	}
	want := map[string]string{
		"lfdi1": "true: interval contains now",
		"lfdi2": "false: scheduled",
		"lfdi3": "false: ended 2018 control whose status says Active",
		"lfdi4": "false: cancelled",
		"lfdi5": "false: superseded",
		"lfdi6": "true: control copied from an FSA",
		"lfdi7": "false: no control",
	}
	for _, d := range p.Devices {
		w := want[d.LFDI]
		if d.UnderControl == nil || (*d.UnderControl) != strings.HasPrefix(w, "true") || d.ControlError != "" {
			t.Errorf("%s (%s): underControl = %v, controlError %q", d.LFDI, w, d.UnderControl, d.ControlError)
		}
	}
}

type failingGetLifecycles struct {
	store.ScopedStore[dercontrol.LifecycleRecord]
}

func (failingGetLifecycles) Get(context.Context, string, string) (dercontrol.LifecycleRecord, error) {
	return dercontrol.LifecycleRecord{}, errors.New("lifecycle backend down")
}

func TestDashboardUnderControl_FailedReadIsNullWithAnError(t *testing.T) {
	now := statusNow.Unix()
	f := newControlFixture(t)
	f.device("1")
	f.device("2")
	f.control("1", "0", "c1", &sep2.DateTimeInterval{Start: now - 10, Duration: 600}, nil, &dercontrol.LifecycleRecord{})

	body, p := f.get(func(s *Stores) { s.DERControlLifecycles = failingGetLifecycles{f.lifecycles} })
	if len(p.Devices) != 2 {
		t.Fatalf("devices = %d, want 2", len(p.Devices))
	}
	// Device 2 has no control, so nothing of it is read and it still answers.
	if d := p.Devices[0]; d.UnderControl != nil || !strings.Contains(d.ControlError, "lifecycle backend down") {
		t.Errorf("device 1: underControl = %v, controlError %q; want null and the cause", d.UnderControl, d.ControlError)
	}
	if d := p.Devices[1]; d.UnderControl == nil || *d.UnderControl {
		t.Errorf("device 2: underControl = %v, want false", d.UnderControl)
	}
	if !strings.Contains(body, `"underControl":null`) {
		t.Errorf("body %s lacks an explicit underControl null", body)
	}
}

func TestDashboardUnderControl_UnwiredStoresAreNullWithNoError(t *testing.T) {
	f := newControlFixture(t)
	f.device("1")
	body, p := f.get(func(s *Stores) { s.DERControls, s.DERControlLifecycles = nil, nil })
	if d := p.Devices[0]; d.UnderControl != nil || d.ControlError != "" {
		t.Errorf("underControl = %v, controlError %q; want null and no error", d.UnderControl, d.ControlError)
	}
	if !strings.Contains(body, `"underControl":null`) || strings.Contains(body, "controlError") {
		t.Errorf("body %s: want underControl null and no controlError key", body)
	}
}
