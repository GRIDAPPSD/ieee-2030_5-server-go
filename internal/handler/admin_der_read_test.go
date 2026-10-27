package handler_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/handler"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

// #1209: GET /api/derms/devices/{edevId}/ders.

var derReadT0 = time.Unix(1_800_000_000, 0)

// failingGet makes every Get fail with a cause the reply must not repeat.
type failingGet[T store.Copier[T]] struct{ store.ScopedReader[T] }

func (failingGet[T]) Get(context.Context, string, string) (T, error) {
	var zero T
	return zero, errors.New("backend exploded at 10.1.2.3")
}

type failingEndDevices struct{ store.EndDeviceReader }

func (failingEndDevices) Get(context.Context, string) (sep2.EndDevice, error) {
	return sep2.EndDevice{}, errors.New("backend exploded at 10.1.2.3")
}

type failingList struct{ store.ScopedReader[sep2.DER] }

func (failingList) List(context.Context, string, store.ListOptions) (store.ListResult[sep2.DER], error) {
	return store.ListResult[sep2.DER]{}, errors.New("backend exploded at 10.1.2.3")
}

type derReadFixture struct {
	clock    time.Time
	devs     *memory.EndDeviceStore
	ders     *memory.ScopedStore[sep2.DER]
	statuses memory.SinceStore[sep2.DERStatus]
	caps     *memory.ScopedStore[sep2.DERCapability]
	settings *memory.ScopedStore[sep2.DERSettings]
	avails   memory.SinceStore[sep2.DERAvailability]
}

func newDERReadFixture(t *testing.T) *derReadFixture {
	t.Helper()
	f := &derReadFixture{
		clock: derReadT0,
		devs:  memory.NewEndDeviceStore(),
		ders:  memory.NewScopedStore[sep2.DER](),
		caps:  memory.NewScopedStore[sep2.DERCapability](),

		settings: memory.NewScopedStore[sep2.DERSettings](),
	}
	clock := func() time.Time { return f.clock }
	f.statuses = memory.NewDERStatusSinceStore(memory.NewScopedStore[sep2.DERStatus](), clock)
	f.avails = memory.NewDERAvailabilitySinceStore(memory.NewScopedStore[sep2.DERAvailability](), clock)
	ctx := context.Background()
	if err := f.devs.Create(ctx, "3", sep2.EndDevice{LFDI: "D001000000000000000000000000000000000001", SFDI: "3"}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"1", "2"} {
		if err := f.ders.Create(ctx, "3", id, sep2.DER{SubscribableResource: sep2.SubscribableResource{Resource: sep2.Resource{Href: "/edev/3/der/" + id}}}); err != nil {
			t.Fatal(err)
		}
	}
	return f
}

func (f *derReadFixture) handler() *handler.AdminDERReadHandler {
	return &handler.AdminDERReadHandler{
		EndDevices: f.devs, DERs: f.ders, DERStatuses: f.statuses, DERCapabilities: f.caps,
		DERSettings: f.settings, DERAvailabilities: f.avails,
		Now: func() time.Time { return derReadT0.Add(2 * time.Minute) },
	}
}

func serveDERRead(t *testing.T, h *handler.AdminDERReadHandler, edevID string) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/derms/devices/"+edevID+"/ders", nil)
	req.SetPathValue("edevId", edevID)
	w := httptest.NewRecorder()
	h.HandleList()(w, req)
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("status %d, body %q: %v", w.Code, w.Body.String(), err)
	}
	return w.Code, body
}

// at walks a decoded reply by key, or by index for a number-like step.
func at(t *testing.T, v any, path ...any) any {
	t.Helper()
	for _, step := range path {
		switch s := step.(type) {
		case string:
			m, ok := v.(map[string]any)
			if !ok {
				t.Fatalf("at %v: %v is not an object", path, v)
			}
			next, present := m[s]
			if !present {
				t.Fatalf("at %v: key %q missing (keys %v)", path, s, keysOf(m))
			}
			v = next
		case int:
			a, ok := v.([]any)
			if !ok || s >= len(a) {
				t.Fatalf("at %v: %v has no index %d", path, v, s)
			}
			v = a[s]
		}
	}
	return v
}

func keysOf(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func TestDERReadOperatorValues(t *testing.T) {
	f := newDERReadFixture(t)
	ctx := context.Background()
	reading := derReadT0.Add(time.Minute).Unix()

	// First post at t0, then a second post at t0+30m that changes only the
	// state of charge: the other elements keep their t0 since time.
	status := sep2.DERStatus{
		ReadingTime:           reading,
		GenConnectStatus:      &sep2.ConnectStatusType{DateTime: reading, Value: 0x07},
		OperationalModeStatus: &sep2.OperationalModeStatusType{DateTime: reading, Value: 2},
		StateOfChargeStatus:   &sep2.StateOfChargeStatusType{DateTime: reading, Value: 7000},
		AlarmStatus:           ptr(sep2.HexBinary32(0x805)),
	}
	if err := f.statuses.Create(ctx, "3/1", "default", status); err != nil {
		t.Fatal(err)
	}
	f.clock = derReadT0.Add(30 * time.Minute)
	status.StateOfChargeStatus = &sep2.StateOfChargeStatusType{DateTime: reading, Value: 8000}
	if err := f.statuses.Update(ctx, "3/1", "default", status); err != nil {
		t.Fatal(err)
	}
	f.clock = derReadT0
	if err := f.caps.Create(ctx, "3/1", "default", sep2.DERCapability{
		Type:                 ptr(uint8(82)),
		ModesSupported:       ptr(sep2.DERControlType(1<<0 | 1<<1 | 1<<2 | 1<<26)),
		RTGMaxW:              &sep2.ActivePower{Value: 12000, Multiplier: 0},
		RTGMaxChargeRateW:    &sep2.ActivePower{Value: 11520, Multiplier: 0},
		RTGMaxDischargeRateW: &sep2.ActivePower{Value: 1152, Multiplier: 1},
		RTGMaxVA:             &sep2.ApparentPower{Value: 13000, Multiplier: 0},
		RTGMaxVar:            &sep2.ReactivePower{Value: -400, Multiplier: 1},
		RTGMaxV:              &sep2.VoltageRMS{Value: 240, Multiplier: 0},
		RTGMaxA:              ptr(int32(50)),
	}); err != nil {
		t.Fatal(err)
	}
	if err := f.settings.Create(ctx, "3/1", "default", sep2.DERSettings{
		UpdatedTime:  derReadT0.Unix(),
		ModesEnabled: ptr(sep2.DERControlType(1 << 3)),
		SetMaxW:      &sep2.ActivePower{Value: 9000, Multiplier: 0},
	}); err != nil {
		t.Fatal(err)
	}
	if err := f.avails.Create(ctx, "3/1", "default", sep2.DERAvailability{
		ReadingTime:          reading,
		AvailabilityDuration: ptr(uint32(7200)),
		StatWAvail:           &sep2.ActivePower{Value: 5000, Multiplier: 0},
	}); err != nil {
		t.Fatal(err)
	}

	code, body := serveDERRead(t, f.handler(), "3")
	if code != http.StatusOK {
		t.Fatalf("status %d: %v", code, body)
	}
	if got := at(t, body, "device"); got != "3" {
		t.Errorf("device = %v", got)
	}
	if got := at(t, body, "now"); got != float64(derReadT0.Add(2*time.Minute).Unix()) {
		t.Errorf("now = %v", got)
	}
	if got := at(t, body, "pollRateSeconds"); got != float64(900) {
		t.Errorf("pollRateSeconds = %v", got)
	}
	if n := len(at(t, body, "ders").([]any)); n != 2 {
		t.Fatalf("ders = %d, want 2", n)
	}
	der := at(t, body, "ders", 0)
	if at(t, der, "id") != "1" || at(t, der, "href") != "/edev/3/der/1" {
		t.Errorf("der identity = %v %v", at(t, der, "id"), at(t, der, "href"))
	}

	checks := []struct {
		name string
		path []any
		want any
	}{
		{"status reported", []any{"status", "reported"}, true},
		{"status error", []any{"status", "error"}, ""},
		{"connect raw", []any{"status", "connect", "raw"}, float64(7)},
		{"connect source", []any{"status", "connect", "source"}, "genConnectStatus"},
		{"connect connected", []any{"status", "connect", "connected"}, true},
		{"connect available", []any{"status", "connect", "available"}, true},
		{"connect operating", []any{"status", "connect", "operating"}, true},
		{"connect test", []any{"status", "connect", "test"}, false},
		{"operationalMode", []any{"status", "operationalMode"}, float64(2)},
		{"stateOfCharge", []any{"status", "stateOfCharge"}, float64(8000)},
		{"storageMode absent", []any{"status", "storageMode"}, nil},
		{"inverter absent", []any{"status", "inverter"}, nil},
		{"alarm raw", []any{"status", "alarm", "raw"}, float64(0x805)},
		{"alarm reserved", []any{"status", "alarm", "reservedBits"}, float64(0x800)},
		{"status readingTime", []any{"status", "readingTime"}, float64(reading)},
		{"status fresh", []any{"status", "freshness", "stale"}, false},
		{"status age", []any{"status", "freshness", "ageSeconds"}, float64(60)},
		{"soc since", []any{"status", "since", "stateOfChargeStatus"}, float64(derReadT0.Add(30 * time.Minute).Unix())},
		{"mode since", []any{"status", "since", "operationalModeStatus"}, float64(derReadT0.Unix())},
		{"connect since", []any{"status", "since", "genConnectStatus"}, float64(derReadT0.Unix())},

		{"capability type", []any{"capability", "type"}, float64(82)},
		{"rtgMaxChargeRateW value", []any{"capability", "rtgMaxChargeRateW", "value"}, float64(11520)},
		{"rtgMaxChargeRateW multiplier", []any{"capability", "rtgMaxChargeRateW", "multiplier"}, float64(0)},
		{"rtgMaxDischargeRateW value", []any{"capability", "rtgMaxDischargeRateW", "value"}, float64(1152)},
		{"rtgMaxDischargeRateW multiplier", []any{"capability", "rtgMaxDischargeRateW", "multiplier"}, float64(1)},
		{"rtgMaxW", []any{"capability", "rtgMaxW", "value"}, float64(12000)},
		{"rtgMaxVA", []any{"capability", "rtgMaxVA", "value"}, float64(13000)},
		{"rtgMaxVar negative kept", []any{"capability", "rtgMaxVar", "value"}, float64(-400)},
		{"rtgMaxV", []any{"capability", "rtgMaxV", "value"}, float64(240)},
		{"rtgMaxA", []any{"capability", "rtgMaxA"}, float64(50)},
		{"modesSupported raw", []any{"capability", "modesSupported", "raw"}, float64(1<<0 | 1<<1 | 1<<2 | 1<<26)},
		{"modesSupported reserved", []any{"capability", "modesSupported", "reservedBits"}, float64(0)},

		{"settings reported", []any{"settings", "reported"}, true},
		{"settings updatedTime", []any{"settings", "updatedTime"}, float64(derReadT0.Unix())},
		{"setMaxW", []any{"settings", "setMaxW", "value"}, float64(9000)},
		{"setMaxVar absent", []any{"settings", "setMaxVar"}, nil},
		{"modesEnabled raw", []any{"settings", "modesEnabled", "raw"}, float64(8)},

		{"availability reported", []any{"availability", "reported"}, true},
		{"availabilityDuration", []any{"availability", "availabilityDuration"}, float64(7200)},
		{"statWAvail value", []any{"availability", "statWAvail", "value"}, float64(5000)},
		{"statWAvail multiplier", []any{"availability", "statWAvail", "multiplier"}, float64(0)},
		{"statVarAvail absent", []any{"availability", "statVarAvail"}, nil},
		{"availability fresh", []any{"availability", "freshness", "stale"}, false},
		{"availability age", []any{"availability", "freshness", "ageSeconds"}, float64(60)},
		{"statWAvail since", []any{"availability", "since", "statWAvail"}, float64(derReadT0.Unix())},
	}
	for _, c := range checks {
		if got := at(t, der, c.path...); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s = %v (%T), want %v (%T)", c.name, got, got, c.want, c.want)
		}
	}

	if got := at(t, der, "status", "alarm", "bits"); !reflect.DeepEqual(got, []any{float64(0), float64(2)}) {
		t.Errorf("alarm bits = %v", got)
	}
	if got := at(t, der, "capability", "modesSupported", "names"); !reflect.DeepEqual(got, []any{"Charge mode", "Discharge mode", "opModConnect", "opModWattVar"}) {
		t.Errorf("modesSupported names = %v", got)
	}
	if got := at(t, der, "settings", "modesEnabled", "names"); !reflect.DeepEqual(got, []any{"opModEnergize"}) {
		t.Errorf("modesEnabled names = %v", got)
	}
}

func TestDERReadThreeAbsencesAreDistinct(t *testing.T) {
	f := newDERReadFixture(t)
	ctx := context.Background()
	// DER 1: status stored with no state of charge; availability stored with
	// no elements at all. Nothing else stored.
	if err := f.statuses.Create(ctx, "3/1", "default", sep2.DERStatus{
		ReadingTime:      derReadT0.Unix(),
		GenConnectStatus: &sep2.ConnectStatusType{DateTime: derReadT0.Unix(), Value: 1},
	}); err != nil {
		t.Fatal(err)
	}
	if err := f.avails.Create(ctx, "3/1", "default", sep2.DERAvailability{ReadingTime: derReadT0.Unix()}); err != nil {
		t.Fatal(err)
	}
	h := f.handler()
	h.DERSettings = failingGet[sep2.DERSettings]{f.settings}

	code, body := serveDERRead(t, h, "3")
	if code != http.StatusOK {
		t.Fatalf("status %d: %v", code, body)
	}
	der := at(t, body, "ders", 0)

	// A missing element: the resource is reported, the field is null, and
	// the null is not a zero.
	if at(t, der, "status", "reported") != true || at(t, der, "status", "stateOfCharge") != nil {
		t.Errorf("missing element: reported=%v stateOfCharge=%v", at(t, der, "status", "reported"), at(t, der, "status", "stateOfCharge"))
	}
	if at(t, der, "availability", "reported") != true || at(t, der, "availability", "statWAvail") != nil || at(t, der, "availability", "availabilityDuration") != nil {
		t.Errorf("empty availability: %v", at(t, der, "availability"))
	}
	// A missing resource: not reported, no error.
	if at(t, der, "capability", "reported") != false || at(t, der, "capability", "error") != "" || at(t, der, "capability", "type") != nil {
		t.Errorf("missing resource: %v", at(t, der, "capability"))
	}
	// A failed read: not reported, error set, and the cause is not in the reply.
	if at(t, der, "settings", "reported") != false || at(t, der, "settings", "error") == "" {
		t.Errorf("failed read: %v", at(t, der, "settings"))
	}
	raw, _ := json.Marshal(body)
	if strings.Contains(string(raw), "10.1.2.3") {
		t.Errorf("the reply carries the backend cause: %s", raw)
	}
	// A DER with nothing stored reads as all three resources not reported.
	other := at(t, body, "ders", 1)
	for _, sec := range []string{"status", "capability", "availability"} {
		if at(t, other, sec, "reported") != false || at(t, other, sec, "error") != "" {
			t.Errorf("DER 2 %s = %v", sec, at(t, other, sec))
		}
	}
}

func TestDERReadStaleAndClockAhead(t *testing.T) {
	f := newDERReadFixture(t)
	ctx := context.Background()
	old := derReadT0.Add(2*time.Minute).Unix() - 1861
	if err := f.statuses.Create(ctx, "3/1", "default", sep2.DERStatus{ReadingTime: old}); err != nil {
		t.Fatal(err)
	}
	if err := f.avails.Create(ctx, "3/1", "default", sep2.DERAvailability{}); err != nil {
		t.Fatal(err)
	}
	_, body := serveDERRead(t, f.handler(), "3")
	der := at(t, body, "ders", 0)
	if at(t, der, "status", "freshness", "stale") != true || at(t, der, "status", "freshness", "ageSeconds") != float64(1861) {
		t.Errorf("status freshness = %v", at(t, der, "status", "freshness"))
	}
	if at(t, der, "availability", "freshness", "stale") != true || at(t, der, "availability", "freshness", "noReadingTime") != true {
		t.Errorf("availability freshness = %v", at(t, der, "availability", "freshness"))
	}
}

func TestDERReadSinceNullWhenStoreKeepsNone(t *testing.T) {
	f := newDERReadFixture(t)
	plain := memory.NewScopedStore[sep2.DERStatus]()
	if err := plain.Create(context.Background(), "3/1", "default", sep2.DERStatus{ReadingTime: derReadT0.Unix()}); err != nil {
		t.Fatal(err)
	}
	h := f.handler()
	h.DERStatuses = plain
	_, body := serveDERRead(t, h, "3")
	if got := at(t, body, "ders", 0, "status", "since"); got != nil {
		t.Errorf("since = %v, want null", got)
	}
}

func TestDERReadDeviceErrors(t *testing.T) {
	f := newDERReadFixture(t)
	cases := []struct {
		name string
		edev string
		mod  func(*handler.AdminDERReadHandler)
		want int
	}{
		{"unknown device", "99", func(*handler.AdminDERReadHandler) {}, http.StatusNotFound},
		{"device lookup failure", "3", func(h *handler.AdminDERReadHandler) { h.EndDevices = failingEndDevices{f.devs} }, http.StatusInternalServerError},
		{"DER list failure", "3", func(h *handler.AdminDERReadHandler) { h.DERs = failingList{f.ders} }, http.StatusInternalServerError},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := f.handler()
			c.mod(h)
			code, body := serveDERRead(t, h, c.edev)
			if code != c.want {
				t.Fatalf("status %d, want %d: %v", code, c.want, body)
			}
			raw, _ := json.Marshal(body)
			if strings.Contains(string(raw), "10.1.2.3") {
				t.Errorf("the reply carries the backend cause: %s", raw)
			}
		})
	}
}

func TestDERReadUnwiredResourceIsNotReported(t *testing.T) {
	f := newDERReadFixture(t)
	h := f.handler()
	h.DERSettings = nil
	_, body := serveDERRead(t, h, "3")
	der := at(t, body, "ders", 0)
	if at(t, der, "settings", "reported") != false || at(t, der, "settings", "error") == "" {
		t.Errorf("unwired settings = %v", at(t, der, "settings"))
	}
}

// The notStored lists must name only elements the vendored structs lack, or
// a reader is told a stored field is dropped.
func TestDERReadNotStoredNamesNoStoredElement(t *testing.T) {
	f := newDERReadFixture(t)
	if err := f.statuses.Create(context.Background(), "3/1", "default", sep2.DERStatus{}); err != nil {
		t.Fatal(err)
	}
	_, body := serveDERRead(t, f.handler(), "3")
	der := at(t, body, "ders", 0)
	for _, c := range []struct {
		section string
		typ     reflect.Type
	}{
		{"status", reflect.TypeOf(sep2.DERStatus{})},
		{"capability", reflect.TypeOf(sep2.DERCapability{})},
		{"settings", reflect.TypeOf(sep2.DERSettings{})},
	} {
		tags := map[string]bool{}
		for i := 0; i < c.typ.NumField(); i++ {
			tags[strings.Split(c.typ.Field(i).Tag.Get("xml"), ",")[0]] = true
		}
		names := at(t, der, c.section, "notStored").([]any)
		if len(names) == 0 {
			t.Errorf("%s notStored is empty", c.section)
		}
		for _, n := range names {
			if tags[n.(string)] {
				t.Errorf("%s notStored names %v, which the vendored struct stores", c.section, n)
			}
		}
	}
	// Control: the check fires on a stored element.
	if !(reflect.TypeOf(sep2.DERStatus{}).NumField() > 0) {
		t.Fatal("no fields")
	}
	if _, ok := reflect.TypeOf(sep2.DERCapability{}).FieldByName("RTGMaxW"); !ok {
		t.Fatal("control field missing")
	}
}
