package handler_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net/http"
	"net/http/httptest"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/dercontrol"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/handler"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/handlers/sep2time"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

const (
	dcDevice    = "0"
	dcDeviceB   = "1"
	dcLFDI      = "65DE1159BA8C8897D5A7F94997D22544EB90A2B7"
	dcOtherLFDI = "0000000000000000000000000000000000000001"
	dcPEN       = uint32(0x0000A0B1)
)

var hex32 = regexp.MustCompile(`^[0-9A-F]{32}$`)

type recordedNotification struct {
	href   string
	status uint8
}

type recordingNotifier struct {
	mu   sync.Mutex
	sent []recordedNotification
}

func (n *recordingNotifier) Notify(_ context.Context, href string, status uint8) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.sent = append(n.sent, recordedNotification{href, status})
}

func (n *recordingNotifier) take() []recordedNotification {
	n.mu.Lock()
	defer n.mu.Unlock()
	out := n.sent
	n.sent = nil
	return out
}

type dcHarness struct {
	h          *handler.AdminDERControlHandler
	mux        *http.ServeMux
	controls   *memory.DERControlStore
	lifecycles *dercontrol.LifecycleStore
	programs   *memory.DERProgramStore
	devices    *memory.EndDeviceStore
	responses  *memory.ScopedStore[sep2.Response]
	notifier   *recordingNotifier
	logs       *bytes.Buffer
}

func newDCHarness(t *testing.T, pen *uint32) *dcHarness {
	t.Helper()
	d := &dcHarness{
		controls:   memory.NewDERControlStore(),
		lifecycles: dercontrol.NewLifecycleStore(),
		programs:   memory.NewDERProgramStore(),
		devices:    memory.NewEndDeviceStore(),
		responses:  memory.NewScopedStore[sep2.Response](),
		notifier:   &recordingNotifier{},
		logs:       &bytes.Buffer{},
	}
	issuer, err := dercontrol.NewIssuer(d.programs, d.controls, d.lifecycles, dercontrol.Config{PEN: pen})
	if err != nil {
		t.Fatalf("NewIssuer: %v", err)
	}
	d.h = &handler.AdminDERControlHandler{
		Issuer:     issuer,
		Controls:   d.controls,
		Lifecycles: d.lifecycles,
		Programs:   d.programs,
		EndDevices: d.devices,
		Responses:  d.responses,
		Notifier:   d.notifier,
		Logger:     slog.New(slog.NewTextHandler(d.logs, nil)),
	}
	d.mux = http.NewServeMux()
	d.mux.HandleFunc("POST /api/der/controls", d.h.HandleCreate())
	d.mux.HandleFunc("GET /api/der/controls", d.h.HandleList())
	d.mux.HandleFunc("POST /api/der/controls/{mrid}/cancel", d.h.HandleCancel())
	d.mux.HandleFunc("GET /api/devices/{id}/der-programs", d.h.HandleListPrograms())

	ctx := context.Background()
	for _, dev := range []struct{ id, lfdi string }{{dcDevice, dcLFDI}, {dcDeviceB, dcOtherLFDI}} {
		if err := d.devices.Create(ctx, dev.id, sep2.EndDevice{LFDI: dev.lfdi, SFDI: "1"}); err != nil {
			t.Fatalf("seed device: %v", err)
		}
	}
	d.seedProgram(t, dcDevice, "0", "0", "PROGRAM-0", 1, true)
	return d
}

func (d *dcHarness) seedProgram(t *testing.T, edev, fsa, derp, mrid string, primacy uint8, withLink bool) {
	t.Helper()
	p := sep2.DERProgram{MRID: mrid, Description: "program " + derp, Primacy: primacy}
	p.Href = "/edev/" + edev + "/fsa/" + fsa + "/derp/" + derp
	if withLink {
		p.DERControlListLink = &sep2.ListLink{Href: p.Href + "/derc"}
	}
	if err := d.programs.Create(context.Background(), edev, derp, p); err != nil {
		t.Fatalf("seed program: %v", err)
	}
}

func (d *dcHarness) do(t *testing.T, method, target, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = "192.0.2.7:4000"
	w := httptest.NewRecorder()
	d.mux.ServeHTTP(w, req)
	return w
}

// storedCounts reports how many controls and lifecycle records exist in
// every scope, so a refusal can be shown to have written nothing.
func (d *dcHarness) storedCounts(t *testing.T) (controls, lifecycles int) {
	t.Helper()
	ctx := context.Background()
	parents, err := d.controls.Parents(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range parents {
		page, err := d.controls.List(ctx, p, store.ListOptions{Unbounded: true})
		if err != nil {
			t.Fatal(err)
		}
		controls += len(page.Items)
		for _, c := range page.Items {
			id := c.Href[strings.LastIndex(c.Href, "/")+1:]
			if _, err := d.lifecycles.Get(ctx, p, id); err == nil {
				lifecycles++
			}
		}
	}
	return controls, lifecycles
}

func futureStart(offset int64) int64 { return sep2time.Now().Unix() + offset }

func maxLimWBody(start int64, value, duration int) string {
	return fmt.Sprintf(`{"derProgramHref":"/edev/0/fsa/0/derp/0","type":"maxLimW","maxLimW":%d,"startTime":%d,"durationSeconds":%d}`, value, start, duration)
}

func decodeCreated(t *testing.T, w *httptest.ResponseRecorder) handler.DERControlCreated {
	t.Helper()
	var got handler.DERControlCreated
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode 201 body: %v (%s)", err, w.Body.String())
	}
	return got
}

// Criterion 1: a create answers 201 with Location and every documented field,
// and the stored control carries the same values.
func TestDERControlCreate_MaxLimW(t *testing.T) {
	d := newDCHarness(t, ptrU32(dcPEN))
	start := futureStart(600)
	body := fmt.Sprintf(`{"derProgramHref":"/edev/0/fsa/0/derp/0","type":"maxLimW","maxLimW":6000,"startTime":%d,"durationSeconds":300,"description":"feeder 12 curtail"}`, start)
	w := d.do(t, http.MethodPost, "/api/der/controls", body)
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d body = %s, want 201", w.Code, w.Body.String())
	}
	got := decodeCreated(t, w)

	if !hex32.MatchString(got.MRID) {
		t.Errorf("mRID = %q, want 32 uppercase hex digits", got.MRID)
	}
	if !strings.HasPrefix(got.Href, "/edev/0/fsa/0/derp/0/derc/") {
		t.Errorf("href = %q, want under /edev/0/fsa/0/derp/0/derc/", got.Href)
	}
	if loc := w.Header().Get("Location"); loc != got.Href {
		t.Errorf("Location = %q, want the body href %q", loc, got.Href)
	}
	if got.DERControlListHref != "/edev/0/fsa/0/derp/0/derc" || got.DERProgramHref != "/edev/0/fsa/0/derp/0" {
		t.Errorf("derControlListHref = %q derProgramHref = %q", got.DERControlListHref, got.DERProgramHref)
	}
	if got.Type != "maxLimW" || got.Description != "feeder 12 curtail" {
		t.Errorf("type = %q description = %q", got.Type, got.Description)
	}
	if got.DERControlBase.OpModMaxLimW == nil || *got.DERControlBase.OpModMaxLimW != 6000 {
		t.Errorf("derControlBase.opModMaxLimW = %v, want 6000", got.DERControlBase.OpModMaxLimW)
	}
	if got.DERControlBase.OpModConnect != nil || got.DERControlBase.OpModFixedPFInjectW != nil {
		t.Errorf("derControlBase carries fields of another type: %+v", got.DERControlBase)
	}
	if got.CreationTime <= 0 {
		t.Errorf("creationTime = %d, want > 0", got.CreationTime)
	}
	if got.Interval != (handler.IntervalView{Start: start, Duration: 300}) {
		t.Errorf("interval = %+v, want start %d duration 300", got.Interval, start)
	}
	if got.EventStatus.CurrentStatus != sep2.EventStatusScheduled || got.EventStatus.Status != "scheduled" || got.EventStatus.DateTime != got.CreationTime {
		t.Errorf("eventStatus = %+v, want scheduled at creationTime %d", got.EventStatus, got.CreationTime)
	}
	if got.Supersedes == nil || len(got.Supersedes) != 0 {
		t.Errorf("supersedes = %#v, want an empty array", got.Supersedes)
	}
	if !got.NotificationAttempted || got.Persisted {
		t.Errorf("notificationAttempted = %v persisted = %v, want true, false", got.NotificationAttempted, got.Persisted)
	}
	if !strings.Contains(w.Body.String(), `"supersedes":[]`) {
		t.Errorf("body does not carry supersedes as []: %s", w.Body.String())
	}

	parent, id, stored, err := d.controls.ByMRID(context.Background(), got.MRID)
	if err != nil {
		t.Fatalf("ByMRID: %v", err)
	}
	if parent != "0/0/0" || stored.Href != got.Href || !strings.HasSuffix(got.Href, "/"+id) {
		t.Errorf("stored under %q/%q href %q, response href %q", parent, id, stored.Href, got.Href)
	}
	if stored.Description != "feeder 12 curtail" || stored.CreationTime != got.CreationTime || *stored.DERControlBase.OpModMaxLimW != 6000 {
		t.Errorf("stored control = %+v", stored)
	}
	if stored.EventStatus != nil || stored.ReplyTo != "" || stored.ResponseRequired != nil {
		t.Errorf("stored control carries serve-time fields: %+v", stored.Event)
	}
}

// Range edges that must be accepted: maxLimW 10000 and displacement 1000.
func TestDERControlCreate_AcceptsRangeEdges(t *testing.T) {
	d := newDCHarness(t, ptrU32(dcPEN))
	w := d.do(t, http.MethodPost, "/api/der/controls", maxLimWBody(futureStart(60), 10000, 300))
	if got := decodeCreated(t, w); w.Code != http.StatusCreated || *got.DERControlBase.OpModMaxLimW != 10000 {
		t.Fatalf("maxLimW 10000: %d %s", w.Code, w.Body.String())
	}
	w = d.do(t, http.MethodPost, "/api/der/controls", fmt.Sprintf(`{"derProgramHref":"/edev/0/fsa/0/derp/0","type":"fixedPFInjectW","powerFactor":{"displacement":1000,"excitation":false},"startTime":%d,"durationSeconds":300}`, futureStart(60)))
	if got := decodeCreated(t, w); w.Code != http.StatusCreated || got.DERControlBase.OpModFixedPFInjectW.Displacement != 1000 {
		t.Fatalf("displacement 1000: %d %s", w.Code, w.Body.String())
	}
}

// The handler echoes the wiring's persistence answer rather than a constant.
func TestDERControlCreate_PersistedEchoesWiring(t *testing.T) {
	d := newDCHarness(t, ptrU32(dcPEN))
	d.h.Persisted = true
	w := d.do(t, http.MethodPost, "/api/der/controls", maxLimWBody(futureStart(60), 1, 300))
	if got := decodeCreated(t, w); !got.Persisted {
		t.Fatalf("persisted = false with Persisted wired true: %s", w.Body.String())
	}
}

func TestDERControlCreate_PowerFactorAndSupersede(t *testing.T) {
	d := newDCHarness(t, ptrU32(dcPEN))
	start := futureStart(600)
	first := d.do(t, http.MethodPost, "/api/der/controls", fmt.Sprintf(`{"derProgramHref":"/edev/0/fsa/0/derp/0","type":"fixedPFInjectW","powerFactor":{"displacement":950,"excitation":true},"startTime":%d,"durationSeconds":600}`, start))
	if first.Code != http.StatusCreated {
		t.Fatalf("first: %d %s", first.Code, first.Body.String())
	}
	a := decodeCreated(t, first)
	pf := a.DERControlBase.OpModFixedPFInjectW
	if pf == nil || *pf != (handler.FixedPowerFactorView{Displacement: 950, Excitation: true, Multiplier: -3}) {
		t.Fatalf("opModFixedPFInjectW = %+v, want 950 excitation true multiplier -3", pf)
	}

	second := d.do(t, http.MethodPost, "/api/der/controls", fmt.Sprintf(`{"derProgramHref":"/edev/0/fsa/0/derp/0","type":"fixedPFInjectW","powerFactor":{"displacement":900,"excitation":false},"startTime":%d,"durationSeconds":600}`, start+60))
	if second.Code != http.StatusCreated {
		t.Fatalf("second: %d %s", second.Code, second.Body.String())
	}
	b := decodeCreated(t, second)
	if !slices.Equal(b.Supersedes, []string{a.MRID}) {
		t.Fatalf("supersedes = %v, want [%s]", b.Supersedes, a.MRID)
	}
}

// Criterion 2: every refusal answers a fixed message naming the field, never
// the submitted value, and stores nothing.
func TestDERControlCreate_Refusals(t *testing.T) {
	const marker = "SUBMITTED9731"
	past := sep2time.Now().Unix() - 3600
	farAhead := sep2time.Now().Unix() + 30*24*3600
	big := `{"description":"` + strings.Repeat("x", 70<<10) + `"}`

	cases := []struct {
		name       string
		pen        *uint32
		body       string
		wantStatus int
		wantError  string
	}{
		{"not JSON", ptrU32(dcPEN), `{"derProgramHref":` + marker, 400, "invalid JSON"},
		{"trailing data", ptrU32(dcPEN), maxLimWBody(futureStart(60), 1, 300) + ` {"x":"` + marker + `"}`, 400, "invalid JSON"},
		{"unknown field", ptrU32(dcPEN), `{"` + marker + `":1}`, 400, "unknown field"},
		{"body over 64 KiB", ptrU32(dcPEN), big, 413, "request body too large"},
		{"href missing", ptrU32(dcPEN), `{"type":"connect","durationSeconds":300}`, 400, "derProgramHref: required"},
		{"href wrong type", ptrU32(dcPEN), `{"derProgramHref":7}`, 400, "derProgramHref: must be a string"},
		{"href bad format", ptrU32(dcPEN), `{"derProgramHref":"/edev/` + marker + `","type":"connect","durationSeconds":300}`, 400, "derProgramHref: invalid format"},
		{"type missing", ptrU32(dcPEN), `{"derProgramHref":"/edev/0/fsa/0/derp/0","durationSeconds":300}`, 400, "type: required"},
		{"type unknown", ptrU32(dcPEN), `{"derProgramHref":"/edev/0/fsa/0/derp/0","type":"` + marker + `","durationSeconds":300}`, 400, "type: unknown control type"},
		{"maxLimW missing", ptrU32(dcPEN), `{"derProgramHref":"/edev/0/fsa/0/derp/0","type":"maxLimW","durationSeconds":300}`, 400, "maxLimW: required for type maxLimW"},
		{"maxLimW over range", ptrU32(dcPEN), `{"derProgramHref":"/edev/0/fsa/0/derp/0","type":"maxLimW","maxLimW":10001,"durationSeconds":300}`, 400, "maxLimW: must be 0 to 10000"},
		{"maxLimW negative", ptrU32(dcPEN), `{"derProgramHref":"/edev/0/fsa/0/derp/0","type":"maxLimW","maxLimW":-1,"durationSeconds":300}`, 400, "maxLimW: must be 0 to 10000"},
		{"maxLimW not an integer", ptrU32(dcPEN), `{"derProgramHref":"/edev/0/fsa/0/derp/0","type":"maxLimW","maxLimW":"` + marker + `","durationSeconds":300}`, 400, "maxLimW: must be an integer"},
		{"maxLimW on connect", ptrU32(dcPEN), `{"derProgramHref":"/edev/0/fsa/0/derp/0","type":"connect","maxLimW":5,"durationSeconds":300}`, 400, "maxLimW: not allowed for this type"},
		{"powerFactor on maxLimW", ptrU32(dcPEN), `{"derProgramHref":"/edev/0/fsa/0/derp/0","type":"maxLimW","maxLimW":5,"powerFactor":{"displacement":1,"excitation":true},"durationSeconds":300}`, 400, "powerFactor: not allowed for this type"},
		{"powerFactor missing", ptrU32(dcPEN), `{"derProgramHref":"/edev/0/fsa/0/derp/0","type":"fixedPFInjectW","durationSeconds":300}`, 400, "powerFactor: required for type fixedPFInjectW"},
		{"displacement missing", ptrU32(dcPEN), `{"derProgramHref":"/edev/0/fsa/0/derp/0","type":"fixedPFInjectW","powerFactor":{"excitation":true},"durationSeconds":300}`, 400, "powerFactor.displacement: required"},
		{"displacement zero", ptrU32(dcPEN), `{"derProgramHref":"/edev/0/fsa/0/derp/0","type":"fixedPFInjectW","powerFactor":{"displacement":0,"excitation":true},"durationSeconds":300}`, 400, "powerFactor.displacement: must be 1 to 1000"},
		{"displacement wrong type", ptrU32(dcPEN), `{"derProgramHref":"/edev/0/fsa/0/derp/0","type":"fixedPFInjectW","powerFactor":{"displacement":"` + marker + `","excitation":true},"durationSeconds":300}`, 400, "powerFactor.displacement: must be an integer"},
		{"excitation missing", ptrU32(dcPEN), `{"derProgramHref":"/edev/0/fsa/0/derp/0","type":"fixedPFInjectW","powerFactor":{"displacement":950},"durationSeconds":300}`, 400, "powerFactor.excitation: required"},
		{"duration missing", ptrU32(dcPEN), `{"derProgramHref":"/edev/0/fsa/0/derp/0","type":"connect"}`, 400, "durationSeconds: required"},
		{"duration zero", ptrU32(dcPEN), `{"derProgramHref":"/edev/0/fsa/0/derp/0","type":"connect","durationSeconds":0}`, 400, "durationSeconds: out of range"},
		{"duration under issuer minimum", ptrU32(dcPEN), `{"derProgramHref":"/edev/0/fsa/0/derp/0","type":"connect","durationSeconds":59}`, 400, "durationSeconds: out of range"},
		{"start in the past", ptrU32(dcPEN), fmt.Sprintf(`{"derProgramHref":"/edev/0/fsa/0/derp/0","type":"connect","startTime":%d,"durationSeconds":300}`, past), 400, "startTime: in the past"},
		{"start too far ahead", ptrU32(dcPEN), fmt.Sprintf(`{"derProgramHref":"/edev/0/fsa/0/derp/0","type":"connect","startTime":%d,"durationSeconds":300}`, farAhead), 400, "startTime: too far in the future"},
		{"start negative", ptrU32(dcPEN), `{"derProgramHref":"/edev/0/fsa/0/derp/0","type":"connect","startTime":-5,"durationSeconds":300}`, 400, "startTime: out of range"},
		{"description too long", ptrU32(dcPEN), `{"derProgramHref":"/edev/0/fsa/0/derp/0","type":"connect","durationSeconds":300,"description":"` + marker + strings.Repeat("d", 30) + `"}`, 400, "description: at most 32 octets"},
		{"program not found", ptrU32(dcPEN), `{"derProgramHref":"/edev/0/fsa/0/derp/9","type":"connect","durationSeconds":300}`, 404, "derProgramHref: DERProgram not found"},
		{"program without control list link", ptrU32(dcPEN), `{"derProgramHref":"/edev/0/fsa/0/derp/nolink","type":"connect","durationSeconds":300}`, 409, "derProgramHref: DERProgram has no usable DERControlListLink"},
		{"description of 17 two-octet characters", ptrU32(dcPEN), `{"derProgramHref":"/edev/0/fsa/0/derp/0","type":"connect","durationSeconds":300,"description":"` + strings.Repeat("\u00e9", 17) + `"}`, 400, "description: at most 32 octets"},
		{"duration past UInt32", ptrU32(dcPEN), `{"derProgramHref":"/edev/0/fsa/0/derp/0","type":"connect","durationSeconds":4294967596}`, 400, "durationSeconds: out of range"},
		{"displacement over range", ptrU32(dcPEN), `{"derProgramHref":"/edev/0/fsa/0/derp/0","type":"fixedPFInjectW","powerFactor":{"displacement":1001,"excitation":true},"durationSeconds":300}`, 400, "powerFactor.displacement: must be 1 to 1000"},
		{"type wrong type", ptrU32(dcPEN), `{"derProgramHref":"/edev/0/fsa/0/derp/0","type":7,"durationSeconds":300}`, 400, "type: must be a string"},
		{"powerFactor wrong type", ptrU32(dcPEN), `{"derProgramHref":"/edev/0/fsa/0/derp/0","type":"fixedPFInjectW","powerFactor":"` + marker + `","durationSeconds":300}`, 400, "powerFactor: must be an object"},
		{"excitation wrong type", ptrU32(dcPEN), `{"derProgramHref":"/edev/0/fsa/0/derp/0","type":"fixedPFInjectW","powerFactor":{"displacement":950,"excitation":"` + marker + `"},"durationSeconds":300}`, 400, "powerFactor.excitation: must be a boolean"},
		{"startTime wrong type", ptrU32(dcPEN), `{"derProgramHref":"/edev/0/fsa/0/derp/0","type":"connect","startTime":"` + marker + `","durationSeconds":300}`, 400, "startTime: must be an integer"},
		{"durationSeconds wrong type", ptrU32(dcPEN), `{"derProgramHref":"/edev/0/fsa/0/derp/0","type":"connect","durationSeconds":"` + marker + `"}`, 400, "durationSeconds: must be an integer"},
		{"description wrong type", ptrU32(dcPEN), `{"derProgramHref":"/edev/0/fsa/0/derp/0","type":"connect","durationSeconds":300,"description":7}`, 400, "description: must be a string"},
		{"fsa segment with control bytes", ptrU32(dcPEN), `{"derProgramHref":"/edev/0/fsa/0\r\n` + marker + `/derp/0","type":"connect","durationSeconds":300}`, 400, "derProgramHref: invalid format"},
		{"href with surrounding space", ptrU32(dcPEN), `{"derProgramHref":" /edev/0/fsa/0/derp/0","type":"connect","durationSeconds":300}`, 400, "derProgramHref: invalid format"},
		{"PEN not configured", nil, `{"derProgramHref":"/edev/0/fsa/0/derp/0","type":"connect","durationSeconds":300}`, 503, "server PEN not configured"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := newDCHarness(t, tc.pen)
			d.seedProgram(t, dcDevice, "0", "nolink", "PROGRAM-NOLINK", 2, false)
			w := d.do(t, http.MethodPost, "/api/der/controls", tc.body)
			assertRefusal(t, w, tc.wantStatus, tc.wantError)
			if strings.Contains(w.Body.String(), marker) {
				t.Errorf("refusal body echoes the submitted value: %s", w.Body.String())
			}
			if c, l := d.storedCounts(t); c != 0 || l != 0 {
				t.Errorf("stored %d controls and %d lifecycle records after a refusal, want 0 and 0", c, l)
			}
			if got := d.notifier.take(); len(got) != 0 {
				t.Errorf("refusal sent notifications: %v", got)
			}
		})
	}
}

// failingIssuer returns a fixed error from both methods, for the 500 path.
type failingIssuer struct{ err error }

func (f failingIssuer) Issue(context.Context, dercontrol.CreateRequest) (dercontrol.Result, error) {
	return dercontrol.Result{}, f.err
}

func (f failingIssuer) Cancel(context.Context, dercontrol.Scope, string, string) (dercontrol.LifecycleRecord, error) {
	return dercontrol.LifecycleRecord{}, f.err
}

func TestDERControlCreate_InternalErrorHasNoDetail(t *testing.T) {
	d := newDCHarness(t, ptrU32(dcPEN))
	d.h.Issuer = failingIssuer{err: errors.New("disk /var/lib/sep2/dercontrols.json: no space left")}
	w := d.do(t, http.MethodPost, "/api/der/controls", maxLimWBody(futureStart(60), 1, 300))
	assertRefusal(t, w, http.StatusInternalServerError, "internal error")
	if strings.Contains(w.Body.String(), "disk") || strings.Contains(d.logs.String(), "no space") {
		t.Errorf("internal detail leaked: body %s log %s", w.Body.String(), d.logs.String())
	}
}

// The #714 seam: a commitment conflict answers 409 and a check that could
// not complete answers 500, so the two stay distinguishable; neither stores
// anything, and the check sees the validated request.
func TestDERControlCreate_CommitmentCheck(t *testing.T) {
	for _, tc := range []struct {
		name       string
		err        error
		wantStatus int
		wantError  string
		wantLog    string
	}{
		{"conflict", fmt.Errorf("overlaps grant X: %w", handler.ErrCommitmentConflict), http.StatusConflict, "control conflicts with an existing commitment", "level=WARN"},
		{"check failed", errors.New("reservation store unavailable"), http.StatusInternalServerError, "internal error", "cause=commitment_check_failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := newDCHarness(t, ptrU32(dcPEN))
			var seen dercontrol.CreateRequest
			d.h.Commitments = func(_ context.Context, req dercontrol.CreateRequest) error {
				seen = req
				return tc.err
			}
			w := d.do(t, http.MethodPost, "/api/der/controls", maxLimWBody(futureStart(60), 1234, 300))
			assertRefusal(t, w, tc.wantStatus, tc.wantError)
			if c, l := d.storedCounts(t); c != 0 || l != 0 {
				t.Errorf("stored %d/%d after a commitment refusal", c, l)
			}
			if seen.Type != dercontrol.MaxLimW || seen.MaxLimW == nil || *seen.MaxLimW != 1234 || seen.DERProgramHref != "/edev/0/fsa/0/derp/0" {
				t.Errorf("check saw %+v, want the validated request", seen)
			}
			if !strings.Contains(d.logs.String(), tc.wantLog) || strings.Contains(d.logs.String(), "reservation store") {
				t.Errorf("log = %s, want %q and no check error text", d.logs.String(), tc.wantLog)
			}
		})
	}
}

// Criterion 3: the list shows admin-issued controls for the device with
// derived status and Response counts matched on subject and endDeviceLFDI.
func TestDERControlList(t *testing.T) {
	d := newDCHarness(t, ptrU32(dcPEN))
	ctx := context.Background()
	d.seedProgram(t, dcDevice, "0", "1", "PROGRAM-1", 2, true)
	d.seedProgram(t, dcDeviceB, "0", "0", "PROGRAM-B", 1, true)

	create := func(program string, offset int64) handler.DERControlCreated {
		t.Helper()
		w := d.do(t, http.MethodPost, "/api/der/controls", fmt.Sprintf(`{"derProgramHref":"%s","type":"connect","startTime":%d,"durationSeconds":300}`, program, futureStart(offset)))
		if w.Code != http.StatusCreated {
			t.Fatalf("create: %d %s", w.Code, w.Body.String())
		}
		return decodeCreated(t, w)
	}
	a := create("/edev/0/fsa/0/derp/0", 60)
	b := create("/edev/0/fsa/0/derp/1", 60)
	other := create("/edev/1/fsa/0/derp/0", 60)

	// A fixture control with no lifecycle record is not admin-issued.
	if err := d.controls.Create(ctx, "0/0/0", "fixture", sep2.DERControl{RandomizableEvent: sep2.RandomizableEvent{Event: sep2.Event{MRID: "FIXTURE", SubscribableResource: sep2.SubscribableResource{Resource: sep2.Resource{Href: "/edev/0/fsa/0/derp/0/derc/fixture"}}}}}); err != nil {
		t.Fatal(err)
	}

	status := func(v uint8) *uint8 { return &v }
	responses := []sep2.Response{
		{EndDeviceLFDI: dcLFDI, Subject: a.MRID, Status: status(1)},
		{EndDeviceLFDI: strings.ToLower(dcLFDI), Subject: a.MRID, Status: status(1)},
		{EndDeviceLFDI: dcLFDI, Subject: a.MRID, Status: status(2)},
		{EndDeviceLFDI: dcLFDI, Subject: a.MRID},
		{EndDeviceLFDI: dcOtherLFDI, Subject: a.MRID, Status: status(1)},
		{EndDeviceLFDI: dcOtherLFDI, Subject: other.MRID, Status: status(1)},
	}
	for i, rsp := range responses {
		if err := d.responses.Create(ctx, "1", fmt.Sprint(i), rsp); err != nil {
			t.Fatal(err)
		}
	}

	w := d.do(t, http.MethodGet, "/api/der/controls?device=0", "")
	if w.Code != http.StatusOK {
		t.Fatalf("list: %d %s", w.Code, w.Body.String())
	}
	var got handler.DERControlList
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Device != "0" || len(got.Controls) != 2 {
		t.Fatalf("device %q controls %d, want device 0 with 2 controls: %s", got.Device, len(got.Controls), w.Body.String())
	}
	byMRID := map[string]handler.DERControlListItem{}
	for _, c := range got.Controls {
		byMRID[c.MRID] = c
	}
	ga, gb := byMRID[a.MRID], byMRID[b.MRID]
	if ga.Href != a.Href || gb.Href != b.Href {
		t.Fatalf("listed hrefs %q %q, want %q %q", ga.Href, gb.Href, a.Href, b.Href)
	}
	if ga.EventStatus.Status != "scheduled" || ga.Type != "connect" {
		t.Errorf("a: status %q type %q", ga.EventStatus.Status, ga.Type)
	}
	wantA := handler.DERControlResponseCounts{Total: 4, ByStatus: map[string]int{"1": 2, "2": 1, "absent": 1}}
	if ga.Responses.Total != wantA.Total || !maps.Equal(ga.Responses.ByStatus, wantA.ByStatus) {
		t.Errorf("a responses = %+v, want %+v", ga.Responses, wantA)
	}
	if gb.Responses.Total != 0 || gb.Responses.ByStatus == nil || len(gb.Responses.ByStatus) != 0 {
		t.Errorf("b responses = %+v, want total 0 and an empty byStatus", gb.Responses)
	}
	if !strings.Contains(w.Body.String(), `"byStatus":{}`) {
		t.Errorf("an empty byStatus is not serialized as {}: %s", w.Body.String())
	}

	w = d.do(t, http.MethodGet, "/api/der/controls?device=0&derProgramHref=/edev/0/fsa/0/derp/1", "")
	var filtered handler.DERControlList
	if err := json.Unmarshal(w.Body.Bytes(), &filtered); err != nil || len(filtered.Controls) != 1 || filtered.Controls[0].MRID != b.MRID {
		t.Fatalf("filtered list = %s, want only %s", w.Body.String(), b.MRID)
	}

	for _, tc := range []struct {
		target, want string
		status       int
	}{
		{"/api/der/controls", "device: required", 400},
		{"/api/der/controls?device=77", "device not found", 404},
		{"/api/der/controls?device=0&derProgramHref=nope", "derProgramHref: invalid format", 400},
		{"/api/der/controls?device=0&derProgramHref=/edev/0/fsa/0%0D%0A/derp/0", "derProgramHref: invalid format", 400},
		{"/api/der/controls?device=0&derProgramHref=/edev/1/fsa/0/derp/0", "derProgramHref: not a program of this device", 400},
	} {
		assertRefusal(t, d.do(t, http.MethodGet, tc.target, ""), tc.status, tc.want)
	}

	w = d.do(t, http.MethodGet, "/api/der/controls?device=1", "")
	if !strings.Contains(w.Body.String(), other.MRID) {
		t.Errorf("device 1 list misses its control: %s", w.Body.String())
	}
}

// Criterion 4: cancel answers the control view, and refuses unknown,
// already-cancelled, superseded and ended controls.
func TestDERControlCancel(t *testing.T) {
	d := newDCHarness(t, ptrU32(dcPEN))
	w := d.do(t, http.MethodPost, "/api/der/controls", maxLimWBody(futureStart(600), 100, 300))
	created := decodeCreated(t, w)
	d.notifier.take()

	w = d.do(t, http.MethodPost, "/api/der/controls/"+strings.ToLower(created.MRID)+"/cancel", `{"reason":"operator request"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("cancel: %d %s", w.Code, w.Body.String())
	}
	var view handler.DERControlView
	if err := json.Unmarshal(w.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	if view.MRID != created.MRID || view.Href != created.Href || view.EventStatus.CurrentStatus != sep2.EventStatusCancelled || view.EventStatus.Status != "cancelled" {
		t.Fatalf("cancel view = %+v", view)
	}
	parent, id, _, _ := d.controls.ByMRID(context.Background(), created.MRID)
	lc, err := d.lifecycles.Get(context.Background(), parent, id)
	if err != nil || lc.CancelledAt == nil || lc.CancelReason != "operator request" || view.EventStatus.DateTime != *lc.CancelledAt {
		t.Fatalf("stored lifecycle = %+v, %v", lc, err)
	}

	assertRefusal(t, d.do(t, http.MethodPost, "/api/der/controls/"+created.MRID+"/cancel", ""), http.StatusConflict, "control already cancelled")
	assertRefusal(t, d.do(t, http.MethodPost, "/api/der/controls/"+strings.Repeat("A", 32)+"/cancel", ""), http.StatusNotFound, "control not found")
	assertRefusal(t, d.do(t, http.MethodPost, "/api/der/controls/not-an-mrid/cancel", ""), http.StatusBadRequest, "mrid: invalid format")
	assertRefusal(t, d.do(t, http.MethodPost, "/api/der/controls/"+created.MRID+"/cancel", `{"reason":"`+strings.Repeat("r", 193)+`"}`), http.StatusBadRequest, "reason: at most 192 octets")
	assertRefusal(t, d.do(t, http.MethodPost, "/api/der/controls/"+created.MRID+"/cancel", `{"reason":"`+strings.Repeat("\u00e9", 97)+`"}`), http.StatusBadRequest, "reason: at most 192 octets")
	assertRefusal(t, d.do(t, http.MethodPost, "/api/der/controls/"+created.MRID+"/cancel", `{"reason":7}`), http.StatusBadRequest, "reason: must be a string")
	assertRefusal(t, d.do(t, http.MethodPost, "/api/der/controls/"+created.MRID+"/cancel", `{"why":"x"}`), http.StatusBadRequest, "unknown field")

	// Superseded and ended both turn on the issuer's clock reaching a
	// future instant, which the untagged build cannot advance; the issuer's
	// own tests cover the decision, and the mapping is checked here.
	for code, want := range map[dercontrol.RefusalCode]string{
		dercontrol.RefusalAlreadySuperseded: "control already superseded",
		dercontrol.RefusalEnded:             "control already ended",
	} {
		d.h.Issuer = failingIssuer{err: &dercontrol.RefusalError{Code: code}}
		assertRefusal(t, d.do(t, http.MethodPost, "/api/der/controls/"+created.MRID+"/cancel", ""), http.StatusConflict, want)
	}
}

// Criterion 5: programs in primacy ascending, then mRID descending order,
// with a null derControlListHref when the program has none.
func TestDERControlListPrograms(t *testing.T) {
	d := newDCHarness(t, ptrU32(dcPEN)) // seeds PROGRAM-0 at primacy 1
	d.seedProgram(t, dcDevice, "0", "5", "PROGRAM-A", 1, true)
	d.seedProgram(t, dcDevice, "2", "6", "PROGRAM-Z", 0, false)
	d.seedProgram(t, dcDevice, "0", "7", "PROGRAM-B", 1, true)

	w := d.do(t, http.MethodGet, "/api/devices/0/der-programs", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status %d %s", w.Code, w.Body.String())
	}
	var got handler.DERProgramListView
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	var order []string
	for _, p := range got.Programs {
		order = append(order, p.MRID)
	}
	want := []string{"PROGRAM-Z", "PROGRAM-B", "PROGRAM-A", "PROGRAM-0"}
	if !slices.Equal(order, want) {
		t.Fatalf("order = %v, want %v", order, want)
	}
	z := got.Programs[0]
	if z.Href != "/edev/0/fsa/2/derp/6" || z.Primacy != 0 || z.Description != "program 6" || z.DERControlListHref != nil {
		t.Errorf("PROGRAM-Z = %+v", z)
	}
	if b := got.Programs[1]; b.DERControlListHref == nil || *b.DERControlListHref != "/edev/0/fsa/0/derp/7/derc" {
		t.Errorf("PROGRAM-B derControlListHref = %v", b.DERControlListHref)
	}
	if !strings.Contains(w.Body.String(), `"derControlListHref":null`) {
		t.Errorf("a missing link is not serialized as null: %s", w.Body.String())
	}
	assertRefusal(t, d.do(t, http.MethodGet, "/api/devices/77/der-programs", ""), http.StatusNotFound, "device not found")

	empty := d.do(t, http.MethodGet, "/api/devices/1/der-programs", "")
	if !strings.Contains(empty.Body.String(), `"programs":[]`) {
		t.Errorf("a device with no programs is not an empty array: %s", empty.Body.String())
	}
}

// Criterion 6: a create and a cancel each notify the program list and the
// control list the control is stored under, once each, with status Changed.
// The program's link names fsa 3, so the request's fsa segment must not leak
// into either href.
func TestDERControlNotifications(t *testing.T) {
	d := newDCHarness(t, ptrU32(dcPEN))
	p := sep2.DERProgram{MRID: "LINKED", DERControlListLink: &sep2.ListLink{Href: "/edev/0/fsa/3/derp/8/derc"}}
	p.Href = "/edev/0/fsa/3/derp/8"
	if err := d.programs.Create(context.Background(), dcDevice, "8", p); err != nil {
		t.Fatal(err)
	}
	want := []recordedNotification{
		{"/edev/0/fsa/3/derp", sep2.NotificationStatusChanged},
		{"/edev/0/fsa/3/derp/8/derc", sep2.NotificationStatusChanged},
	}

	w := d.do(t, http.MethodPost, "/api/der/controls", fmt.Sprintf(`{"derProgramHref":"/edev/0/fsa/9/derp/8","type":"connect","startTime":%d,"durationSeconds":300}`, futureStart(60)))
	if w.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	if got := d.notifier.take(); !slices.Equal(got, want) {
		t.Fatalf("create notifications = %v, want %v", got, want)
	}
	created := decodeCreated(t, w)
	if w := d.do(t, http.MethodPost, "/api/der/controls/"+created.MRID+"/cancel", ""); w.Code != http.StatusOK {
		t.Fatalf("cancel: %d %s", w.Code, w.Body.String())
	}
	if got := d.notifier.take(); !slices.Equal(got, want) {
		t.Fatalf("cancel notifications = %v, want %v", got, want)
	}

	d.h.Notifier = nil
	w = d.do(t, http.MethodPost, "/api/der/controls", maxLimWBody(futureStart(60), 1, 300))
	if got := decodeCreated(t, w); got.NotificationAttempted {
		t.Errorf("notificationAttempted = true with no notifier")
	}
}

// Criterion 8: one log line per create, cancel and post-auth refusal, and no
// request-supplied text in any of them.
func TestDERControlAuditLog(t *testing.T) {
	d := newDCHarness(t, ptrU32(dcPEN))
	// JSON escapes, so each body decodes to a real CR and LF.
	const forged = `\r\nevent=forged`

	create := d.do(t, http.MethodPost, "/api/der/controls", fmt.Sprintf(`{"derProgramHref":"/edev/0/fsa/0/derp/0","type":"maxLimW","maxLimW":4200,"startTime":%d,"durationSeconds":300,"description":"a\r\nevent=forged"}`, futureStart(60)))
	if create.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", create.Code, create.Body.String())
	}
	created := decodeCreated(t, create)
	cancel := d.do(t, http.MethodPost, "/api/der/controls/"+created.MRID+"/cancel", `{"reason":"r\r\nevent=forged"}`)
	if cancel.Code != http.StatusOK {
		t.Fatalf("cancel: %d %s", cancel.Code, cancel.Body.String())
	}
	refusals := []string{
		`{"derProgramHref":"/edev/0/fsa/0/derp/0","type":"` + forged + `","durationSeconds":300}`,
		`{"derProgramHref":"/edev/0/fsa/0/derp/0` + forged + `","type":"connect","durationSeconds":300}`,
		`{"` + forged + `":1}`,
		`{"derProgramHref":"/edev/0/fsa/0/derp/0","type":"connect","durationSeconds":300,"description":"` + strings.Repeat("x", 30) + forged + `"}`,
		`{"derProgramHref":"/edev/0/fsa/0` + forged + `/derp/0","type":"connect","durationSeconds":300}`,
		// Passes every handler check and is refused by the issuer.
		`{"derProgramHref":"/edev/0/fsa/0/derp/9","type":"connect","durationSeconds":300,"description":"` + forged + `"}`,
	}
	for _, body := range refusals {
		if w := d.do(t, http.MethodPost, "/api/der/controls", body); w.Code < 400 {
			t.Fatalf("refusal case answered %d", w.Code)
		}
	}
	if w := d.do(t, http.MethodPost, "/api/der/controls/"+created.MRID+"/cancel", `{"reason":"`+forged+`"}`); w.Code != http.StatusConflict {
		t.Fatalf("second cancel answered %d", w.Code)
	}
	// A 5xx after full validation.
	d.h.Commitments = func(context.Context, dercontrol.CreateRequest) error { return errors.New("down") }
	if w := d.do(t, http.MethodPost, "/api/der/controls", `{"derProgramHref":"/edev/0/fsa/0/derp/0","type":"connect","durationSeconds":300,"description":"`+forged+`"}`); w.Code != http.StatusInternalServerError {
		t.Fatalf("failing check answered %d", w.Code)
	}

	out := d.logs.String()
	lines := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
	wantLines := 2 + len(refusals) + 1 + 1
	if len(lines) != wantLines {
		t.Fatalf("got %d log lines, want %d:\n%s", len(lines), wantLines, out)
	}
	for _, line := range lines {
		if !strings.Contains(line, "admission=none") {
			t.Errorf("line carries no admission field: %s", line)
		}
		refused := strings.Contains(line, "_refused ")
		if refused && !strings.Contains(line, "level=WARN") {
			t.Errorf("refusal not at WARN: %s", line)
		}
		if strings.Contains(line, "status=5") && (refused || !strings.Contains(line, "level=ERROR") || !strings.Contains(line, "cause=")) {
			t.Errorf("5xx not logged as an ERROR failure with a cause: %s", line)
		}
	}
	if strings.Contains(out, "\r") || strings.Contains(out, "forged") {
		t.Fatalf("log carries request-supplied bytes:\n%s", out)
	}
	for _, want := range []string{
		"event=der_control_created", "event=der_control_cancelled", "event=der_control_create_refused", "event=der_control_cancel_refused",
		"mrid=" + created.MRID, "href=" + created.Href, "type=maxLimW", "value=4200", "duration=300",
		"remote_addr=192.0.2.7:4000", "admission=none", "code=type_unknown", "code=already_cancelled", "code=program_not_found", "event=der_control_create_failed",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("log misses %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "description=") || strings.Contains(out, "reason=") {
		t.Errorf("log names a description or reason field:\n%s", out)
	}
}

func assertRefusal(t *testing.T, w *httptest.ResponseRecorder, status int, message string) {
	t.Helper()
	if w.Code != status {
		t.Fatalf("status = %d, want %d; body = %s", w.Code, status, w.Body.String())
	}
	var body struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("refusal body is not JSON: %v (%s)", err, w.Body.String())
	}
	if body.Error != message {
		t.Fatalf("error = %q, want %q", body.Error, message)
	}
}

func ptrU32(v uint32) *uint32 { return &v }

// failingUpdates is a lifecycle store whose Update fails while fail is set.
// It does not claim to roll back its own failures, so the issuer's undo
// really runs its compensating writes against it.
type failingUpdates struct {
	inner *dercontrol.LifecycleStore
	fail  bool
}

func (f *failingUpdates) Get(ctx context.Context, parentID, id string) (dercontrol.LifecycleRecord, error) {
	return f.inner.Get(ctx, parentID, id)
}

func (f *failingUpdates) Create(ctx context.Context, parentID, id string, r dercontrol.LifecycleRecord) error {
	return f.inner.Create(ctx, parentID, id, r)
}

func (f *failingUpdates) Update(ctx context.Context, parentID, id string, r dercontrol.LifecycleRecord) error {
	if f.fail {
		return errors.New("lifecycle store /var/lib/sep2 unavailable")
	}
	return f.inner.Update(ctx, parentID, id, r)
}

func (f *failingUpdates) Delete(ctx context.Context, parentID, id string) error {
	return f.inner.Delete(ctx, parentID, id)
}

func withFailingUpdates(t *testing.T, d *dcHarness) *failingUpdates {
	t.Helper()
	f := &failingUpdates{inner: d.lifecycles}
	issuer, err := dercontrol.NewIssuer(d.programs, d.controls, f, dercontrol.Config{PEN: ptrU32(dcPEN)})
	if err != nil {
		t.Fatal(err)
	}
	d.h.Issuer, d.h.Lifecycles = issuer, f
	return f
}

// A create whose supersede mark fails and cannot be undone leaves the new
// control stored and served to devices. The API says so, names it, logs it
// at ERROR and notifies, since devices can see it.
func TestDERControlCreate_UndoFailureKeepsControlLive(t *testing.T) {
	d := newDCHarness(t, ptrU32(dcPEN))
	f := withFailingUpdates(t, d)
	start := futureStart(600)
	if w := d.do(t, http.MethodPost, "/api/der/controls", maxLimWBody(start, 100, 600)); w.Code != http.StatusCreated {
		t.Fatalf("first create: %d %s", w.Code, w.Body.String())
	}
	d.notifier.take()
	d.logs.Reset()
	f.fail = true

	w := d.do(t, http.MethodPost, "/api/der/controls", maxLimWBody(start+60, 200, 600))
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d body %s, want 500", w.Code, w.Body.String())
	}
	var got handler.DERControlIncomplete
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	parent, id, stored, err := d.controls.ByMRID(context.Background(), got.MRID)
	if err != nil {
		t.Fatalf("the reported mRID %q is not stored: %v", got.MRID, err)
	}
	if !got.ControlKept || got.Href != stored.Href || parent != "0/0/0" || !strings.HasSuffix(got.Href, "/"+id) {
		t.Fatalf("body = %+v, stored %s under %s", got, stored.Href, parent)
	}
	if got.Error != "control may be live: its write could not be undone" || strings.Contains(w.Body.String(), "/var/lib") {
		t.Errorf("body = %s", w.Body.String())
	}
	want := []recordedNotification{
		{"/edev/0/fsa/0/derp", sep2.NotificationStatusChanged},
		{"/edev/0/fsa/0/derp/0/derc", sep2.NotificationStatusChanged},
	}
	if n := d.notifier.take(); !slices.Equal(n, want) {
		t.Errorf("notifications = %v, want %v", n, want)
	}
	out := d.logs.String()
	if strings.Count(out, "\n") != 1 || !strings.Contains(out, "level=ERROR") || !strings.Contains(out, "event=der_control_create_incomplete") ||
		!strings.Contains(out, "mrid="+got.MRID) || !strings.Contains(out, "control_kept=true") || !strings.Contains(out, "cause=undo_incomplete") || strings.Contains(out, "/var/lib") {
		t.Errorf("log = %s", out)
	}
	list := d.do(t, http.MethodGet, "/api/der/controls?device=0", "")
	if !strings.Contains(list.Body.String(), got.MRID) {
		t.Errorf("the kept control is not listed: %s", list.Body.String())
	}
}

// A cancel whose write fails and cannot be restored may have recorded the
// cancellation, which devices read; it is reported, logged and notified.
func TestDERControlCancel_UndoFailureNotifies(t *testing.T) {
	d := newDCHarness(t, ptrU32(dcPEN))
	f := withFailingUpdates(t, d)
	created := decodeCreated(t, d.do(t, http.MethodPost, "/api/der/controls", maxLimWBody(futureStart(600), 100, 300)))
	d.notifier.take()
	d.logs.Reset()
	f.fail = true

	w := d.do(t, http.MethodPost, "/api/der/controls/"+created.MRID+"/cancel", "")
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d body %s, want 500", w.Code, w.Body.String())
	}
	var got handler.DERControlIncomplete
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil || got.MRID != created.MRID || got.Href != created.Href || !got.ControlKept {
		t.Fatalf("body = %s (%v), want the control named and controlKept true", w.Body.String(), err)
	}
	list := d.do(t, http.MethodGet, "/api/der/controls?device=0", "")
	if !strings.Contains(list.Body.String(), created.MRID) {
		t.Errorf("the control is not listed after the failed cancel: %s", list.Body.String())
	}
	if n := d.notifier.take(); len(n) != 2 {
		t.Errorf("notifications = %v, want the program list and the control list", n)
	}
	if out := d.logs.String(); !strings.Contains(out, "level=ERROR") || !strings.Contains(out, "event=der_control_cancel_incomplete") || !strings.Contains(out, "mrid="+created.MRID) {
		t.Errorf("log = %s", out)
	}
}

// getFails is a control store whose Get always fails. A committed cancel
// must not depend on reading the control again.
type getFails struct{ *memory.DERControlStore }

func (getFails) Get(context.Context, string, string) (sep2.DERControl, error) {
	return sep2.DERControl{}, errors.New("read failed")
}

func TestDERControlCancel_CommittedCancelNotifiesWithoutRereading(t *testing.T) {
	d := newDCHarness(t, ptrU32(dcPEN))
	created := decodeCreated(t, d.do(t, http.MethodPost, "/api/der/controls", maxLimWBody(futureStart(600), 100, 300)))
	d.notifier.take()
	d.h.Controls = getFails{d.controls}

	w := d.do(t, http.MethodPost, "/api/der/controls/"+created.MRID+"/cancel", "")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"status":"cancelled"`) {
		t.Fatalf("cancel: %d %s", w.Code, w.Body.String())
	}
	if n := d.notifier.take(); len(n) != 2 {
		t.Errorf("notifications = %v, want 2", n)
	}
}

// failingCreateAndDelete is a lifecycle store whose Create stores the record
// and then reports failure, and whose Delete fails, so the issuer's undo
// leaves an orphan record but no control.
type failingCreateAndDelete struct{ failingUpdates }

func (f *failingCreateAndDelete) Create(ctx context.Context, parentID, id string, r dercontrol.LifecycleRecord) error {
	if err := f.inner.Create(ctx, parentID, id, r); err != nil {
		return err
	}
	return errors.New("lifecycle create failed")
}

func (f *failingCreateAndDelete) Delete(context.Context, string, string) error {
	return errors.New("lifecycle delete failed")
}

// An undo that fails without keeping the control leaves nothing a device
// can read, so the create is a plain 500 naming no control and sending no
// notification.
func TestDERControlCreate_UndoFailureWithoutControlIsPlain500(t *testing.T) {
	d := newDCHarness(t, ptrU32(dcPEN))
	f := &failingCreateAndDelete{failingUpdates{inner: d.lifecycles}}
	issuer, err := dercontrol.NewIssuer(d.programs, d.controls, f, dercontrol.Config{PEN: ptrU32(dcPEN)})
	if err != nil {
		t.Fatal(err)
	}
	d.h.Issuer = issuer

	w := d.do(t, http.MethodPost, "/api/der/controls", maxLimWBody(futureStart(60), 100, 300))
	assertRefusal(t, w, http.StatusInternalServerError, "internal error")
	if strings.Contains(w.Body.String(), "mRID") || strings.Contains(w.Body.String(), "controlKept") {
		t.Errorf("body names a control that was never stored: %s", w.Body.String())
	}
	if c, _ := d.storedCounts(t); c != 0 {
		t.Errorf("%d controls stored, want 0", c)
	}
	if n := d.notifier.take(); len(n) != 0 {
		t.Errorf("notifications = %v, want none", n)
	}
	if out := d.logs.String(); !strings.Contains(out, "event=der_control_create_failed") || !strings.Contains(out, "cause=undo_incomplete") || !strings.Contains(out, "control_kept=false") {
		t.Errorf("log = %s", out)
	}
}
