package handler_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/dercontrol"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/derhref"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/fsaprogram"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/handler"
	coresingleton "github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/handlers/singleton"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

// #920: DER program templates on an admin FSA.

type templateFixture struct {
	h        *handler.AdminFSAHandler
	copies   fsaprogram.Copies
	notifier *recordingNotifier
	mux      *http.ServeMux
}

const templatePEN = uint32(0xA0B1)

func newTemplateFixture(t *testing.T) *templateFixture {
	t.Helper()
	devs := map[string]sep2.EndDevice{}
	for _, id := range []string{"1", "2", "3"} {
		devs[id] = sep2.EndDevice{}
	}
	return newTemplateFixtureWith(t, memory.NewDERProgramStore(), &stubEndDevices{devs: devs})
}

// newTemplateFixtureWith builds the fixture over the given program store and
// device reader, so a test can share them with the DER control handler.
func newTemplateFixtureWith(t *testing.T, programs *memory.DERProgramStore, devices handler.EndDeviceReader) *templateFixture {
	t.Helper()
	origins, err := memory.NewProgramOriginStore("")
	if err != nil {
		t.Fatal(err)
	}
	copies := fsaprogram.Copies{
		Programs: programs,
		Defaults: memory.NewScopedStore[sep2.DefaultDERControl](),
		Origins:  origins,
	}
	pen := templatePEN
	n := &recordingNotifier{}
	retirer, err := dercontrol.NewIssuer(programs, memory.NewDERControlStore(), dercontrol.NewLifecycleStore(), dercontrol.Config{PEN: &pen})
	if err != nil {
		t.Fatal(err)
	}
	h := &handler.AdminFSAHandler{
		AdminFSAs:   memory.NewAdminFSAStore(),
		DeviceFSAs:  memory.NewScopedStore[sep2.FunctionSetAssignments](),
		EndDevices:  devices,
		DERPrograms: &stubPrograms{known: map[string]bool{}},
		Copies:      &copies,
		Notifier:    n,
		PEN:         &pen,
		Retirer:     retirer,
	}
	if err := h.AdminFSAs.Create(context.Background(), "fsa-A", sep2.FunctionSetAssignments{MRID: "fsa-A", Description: "roof"}); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/fsas/{id}/der-programs", h.HandleCreateProgramTemplate())
	mux.HandleFunc("GET /api/fsas/{id}/der-programs", h.HandleListProgramTemplates())
	mux.HandleFunc("POST /api/devices/{id}/fsa-assignment", h.HandleAssignDeviceFSA())
	mux.HandleFunc("DELETE /api/devices/{id}/fsa-assignment", h.HandleUnassignDeviceFSA())
	mux.HandleFunc("POST /api/devices/{id}/fsas/{fsaId}/der-programs", h.HandleCreateDeviceProgram())
	mux.HandleFunc("PUT /api/fsas/{id}/der-programs/{derpId}", h.HandleUpdateProgramTemplate())
	mux.HandleFunc("DELETE /api/fsas/{id}/der-programs/{derpId}", h.HandleDeleteProgramTemplate())
	mux.HandleFunc("PUT /api/devices/{id}/der-programs/{derpId}", h.HandleUpdateDeviceProgram())
	mux.HandleFunc("DELETE /api/devices/{id}/der-programs/{derpId}", h.HandleDeleteDeviceProgram())
	return &templateFixture{h: h, copies: copies, notifier: n, mux: mux}
}

func (f *templateFixture) do(method, target, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	w := httptest.NewRecorder()
	f.mux.ServeHTTP(w, req)
	return w
}

func (f *templateFixture) assign(t *testing.T, dev, fsa string) {
	t.Helper()
	if w := f.do(http.MethodPost, "/api/devices/"+dev+"/fsa-assignment", `{"fsaHref":"/api/fsas/`+fsa+`"}`); w.Code != http.StatusOK {
		t.Fatalf("assign %s to %s: %d %s", dev, fsa, w.Code, w.Body.String())
	}
}

type templateView struct {
	ID             string   `json:"id"`
	MRID           string   `json:"mRID"`
	Description    string   `json:"description"`
	Primacy        uint8    `json:"primacy"`
	Version        uint16   `json:"version"`
	Copies         []string `json:"copies"`
	DefaultControl struct {
		MRID           string `json:"mRID"`
		Type           string `json:"type"`
		DERControlBase struct {
			OpModMaxLimW *uint16 `json:"opModMaxLimW"`
		} `json:"derControlBase"`
	} `json:"defaultControl"`
}

const voltSupportBody = `{"description":"volt support","primacy":2,"defaultControl":{"type":"maxLimW","maxLimW":6000}}`

var (
	templateIDPattern = regexp.MustCompile(`^derp-[0-9a-f]{16}$`)
	penMRIDPattern    = regexp.MustCompile(`^[0-9A-F]{24}0000A0B1$`)
)

// assertDeviceCopy checks the stored program, default control and origin a
// device reads for template v under fsa.
func assertDeviceCopy(t *testing.T, c fsaprogram.Copies, dev, fsa string, v templateView) {
	t.Helper()
	ctx := context.Background()
	href := "/edev/" + dev + "/fsa/" + fsa + "/derp/" + v.ID
	p, err := c.Programs.Get(ctx, dev, v.ID)
	if err != nil {
		t.Fatalf("device %s program: %v", dev, err)
	}
	if p.Href != href || p.MRID != v.MRID || p.Description != v.Description || p.Primacy != v.Primacy ||
		p.Version == nil || *p.Version != v.Version ||
		p.DERControlListLink == nil || p.DERControlListLink.Href != href+"/derc" ||
		p.DefaultDERControlLink == nil || p.DefaultDERControlLink.Href != href+"/dderc" {
		t.Errorf("device %s program = %+v, want the copy of %+v at %s", dev, p, v, href)
	}
	d, err := c.Defaults.Get(ctx, derhref.ScopeKey(dev, fsa, v.ID), coresingleton.SingletonKey)
	if err != nil {
		t.Fatalf("device %s default control: %v", dev, err)
	}
	if d.Href != href+"/dderc" || d.MRID != v.DefaultControl.MRID || d.DERControlBase == nil ||
		d.DERControlBase.OpModMaxLimW == nil || v.DefaultControl.DERControlBase.OpModMaxLimW == nil ||
		uint16(*d.DERControlBase.OpModMaxLimW) != *v.DefaultControl.DERControlBase.OpModMaxLimW {
		t.Errorf("device %s default = %+v, want href %s/dderc mRID %s", dev, d, href, v.DefaultControl.MRID)
	}
	o, err := c.Origins.Get(ctx, dev, v.ID)
	if err != nil || o.Kind != memory.ProgramOriginTemplate || o.FSAID != fsa || o.TemplateID != v.ID {
		t.Errorf("device %s origin = %+v, %v", dev, o, err)
	}
}

func assertNoDeviceCopy(t *testing.T, c fsaprogram.Copies, dev string) {
	t.Helper()
	list, err := c.Programs.List(context.Background(), dev, store.ListOptions{Unbounded: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Items) != 0 {
		t.Errorf("device %s holds %d programs, want 0: %+v", dev, len(list.Items), list.Items)
	}
}

func TestCreateProgramTemplate_CopiesToEveryAssignedDevice(t *testing.T) {
	t.Parallel()
	f := newTemplateFixture(t)
	f.assign(t, "1", "fsa-A")
	f.assign(t, "2", "fsa-A")
	f.notifier.take()

	w := f.do(http.MethodPost, "/api/fsas/fsa-A/der-programs", voltSupportBody)
	if w.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	var v templateView
	if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	if !templateIDPattern.MatchString(v.ID) || !penMRIDPattern.MatchString(v.MRID) || !penMRIDPattern.MatchString(v.DefaultControl.MRID) || v.MRID == v.DefaultControl.MRID {
		t.Errorf("id %q mRID %q default mRID %q: want a derp- id and two distinct PEN mRIDs", v.ID, v.MRID, v.DefaultControl.MRID)
	}
	if v.Description != "volt support" || v.Primacy != 2 || v.Version != 0 || v.DefaultControl.Type != "maxLimW" ||
		v.DefaultControl.DERControlBase.OpModMaxLimW == nil || *v.DefaultControl.DERControlBase.OpModMaxLimW != 6000 {
		t.Errorf("view = %+v", v)
	}
	wantCopies := []string{"/edev/1/fsa/fsa-A/derp/" + v.ID, "/edev/2/fsa/fsa-A/derp/" + v.ID}
	if !slices.Equal(v.Copies, wantCopies) {
		t.Errorf("copies = %q, want %q", v.Copies, wantCopies)
	}

	stored, err := f.h.AdminFSAs.Template(context.Background(), "fsa-A", v.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.MRID != v.MRID || stored.DefaultMRID != v.DefaultControl.MRID || stored.Description != "volt support" || stored.Primacy != 2 || stored.Version != 0 ||
		stored.DefaultControlBase.OpModMaxLimW == nil || *stored.DefaultControlBase.OpModMaxLimW != 6000 {
		t.Errorf("stored template = %+v", stored)
	}
	for _, dev := range []string{"1", "2"} {
		assertDeviceCopy(t, f.copies, dev, "fsa-A", v)
	}
	assertNoDeviceCopy(t, f.copies, "3")

	want := []recordedNotification{{"/edev/1/fsa/fsa-A/derp", sep2.NotificationStatusDefault}, {"/edev/2/fsa/fsa-A/derp", sep2.NotificationStatusDefault}}
	if got := f.notifier.take(); !slices.Equal(got, want) {
		t.Errorf("notifications = %+v, want %+v", got, want)
	}
}

func TestCreateProgramTemplate_SuppliedMRIDIsKeptUppercase(t *testing.T) {
	t.Parallel()
	f := newTemplateFixture(t)
	w := f.do(http.MethodPost, "/api/fsas/fsa-A/der-programs", `{"description":"x","primacy":0,"mRID":"0123456789abcdef0123456789abcdef","defaultControl":{"type":"connect"}}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	var v templateView
	if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	if v.MRID != "0123456789ABCDEF0123456789ABCDEF" || v.DefaultControl.Type != "connect" {
		t.Errorf("view = %+v", v)
	}
	if v.Copies == nil || len(v.Copies) != 0 {
		t.Errorf("copies = %#v, want an empty list", v.Copies)
	}
}

func TestCreateProgramTemplate_BadInput_400(t *testing.T) {
	t.Parallel()
	dc := `"defaultControl":{"type":"connect"}`
	const (
		descMsg    = "description: 1 to 32 printable ASCII characters required"
		primacyMsg = "primacy: must be 0, 1, 2 or 65 to 191"
		mridMsg    = "mRID: must be 32 hex digits, not the reserved form whose first 24 digits are F"
	)
	type badCase struct{ body, msg string }
	cases := map[string]badCase{
		"description missing":          {`{"primacy":1,` + dc + `}`, descMsg},
		"description empty":            {`{"description":"","primacy":1,` + dc + `}`, descMsg},
		"description 33 characters":    {`{"description":"` + strings.Repeat("d", 33) + `","primacy":1,` + dc + `}`, descMsg},
		"description not ASCII":        {`{"description":"caf\u00e9","primacy":1,` + dc + `}`, descMsg},
		"description control char":     {`{"description":"a\tb","primacy":1,` + dc + `}`, descMsg},
		"description 0x1F":             {`{"description":"a\u001fb","primacy":1,` + dc + `}`, descMsg},
		"description 0x7F":             {`{"description":"a\u007fb","primacy":1,` + dc + `}`, descMsg},
		"primacy missing":              {`{"description":"d",` + dc + `}`, primacyMsg},
		"primacy 3":                    {`{"description":"d","primacy":3,` + dc + `}`, primacyMsg},
		"primacy 64":                   {`{"description":"d","primacy":64,` + dc + `}`, primacyMsg},
		"primacy 192":                  {`{"description":"d","primacy":192,` + dc + `}`, primacyMsg},
		"primacy negative":             {`{"description":"d","primacy":-1,` + dc + `}`, primacyMsg},
		"primacy not a number":         {`{"description":"d","primacy":"1",` + dc + `}`, "invalid JSON"},
		"mRID 31 digits":               {`{"description":"d","primacy":1,"mRID":"` + strings.Repeat("A", 31) + `",` + dc + `}`, mridMsg},
		"mRID not hex":                 {`{"description":"d","primacy":1,"mRID":"` + strings.Repeat("G", 32) + `",` + dc + `}`, mridMsg},
		"mRID all F":                   {`{"description":"d","primacy":1,"mRID":"` + strings.Repeat("f", 32) + `",` + dc + `}`, mridMsg},
		"mRID reserved with a PEN":     {`{"description":"d","primacy":1,"mRID":"` + strings.Repeat("F", 24) + `0000B1C2",` + dc + `}`, mridMsg},
		"defaultControl missing":       {`{"description":"d","primacy":1}`, "defaultControl: required"},
		"defaultControl type unknown":  {`{"description":"d","primacy":1,"defaultControl":{"type":"boost"}}`, "defaultControl.type: unknown control type"},
		"defaultControl value missing": {`{"description":"d","primacy":1,"defaultControl":{"type":"maxLimW"}}`, "defaultControl.maxLimW: required for type maxLimW"},
		"defaultControl out of range":  {`{"description":"d","primacy":1,"defaultControl":{"type":"maxLimW","maxLimW":10001}}`, "defaultControl.maxLimW: must be 0 to 10000"},
		"unknown field":                {`{"description":"d","primacy":1,"version":4,` + dc + `}`, "unknown field"},
		"invalid JSON":                 {`{"description":`, "invalid JSON"},
		"trailing data":                {`{"description":"d","primacy":1,` + dc + `}{}`, "invalid JSON"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := newTemplateFixture(t)
			f.assign(t, "1", "fsa-A")
			f.notifier.take()
			w := f.do(http.MethodPost, "/api/fsas/fsa-A/der-programs", tc.body)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("status = %d body %s, want 400", w.Code, w.Body.String())
			}
			if msg := errorBody(t, w.Body.Bytes()); msg != tc.msg {
				t.Errorf("message = %q, want %q", msg, tc.msg)
			}
			if n := len(f.h.AdminFSAs.Templates(context.Background(), "fsa-A")); n != 0 {
				t.Errorf("%d templates stored after a refusal", n)
			}
			assertNoDeviceCopy(t, f.copies, "1")
			if got := f.notifier.take(); len(got) != 0 {
				t.Errorf("notifications after a refusal: %+v", got)
			}
		})
	}
}

func TestCreateProgramTemplate_Boundaries_201(t *testing.T) {
	t.Parallel()
	for _, body := range []string{
		`{"description":"` + strings.Repeat("~", 32) + `","primacy":0,"defaultControl":{"type":"disconnect"}}`,
		`{"description":" ","primacy":65,"defaultControl":{"type":"targetW","targetW":{"value":-5,"multiplier":3}}}`,
		`{"description":"d","primacy":191,"defaultControl":{"type":"fixedPFInjectW","powerFactor":{"displacement":900,"excitation":false}}}`,
	} {
		f := newTemplateFixture(t)
		if w := f.do(http.MethodPost, "/api/fsas/fsa-A/der-programs", body); w.Code != http.StatusCreated {
			t.Errorf("%s: status = %d %s, want 201", body, w.Code, w.Body.String())
		}
	}
}

func TestCreateProgramTemplate_FSAMissing_404(t *testing.T) {
	t.Parallel()
	f := newTemplateFixture(t)
	if w := f.do(http.MethodPost, "/api/fsas/nope/der-programs", voltSupportBody); w.Code != http.StatusNotFound {
		t.Fatalf("status = %d %s, want 404", w.Code, w.Body.String())
	}
	if w := f.do(http.MethodGet, "/api/fsas/nope/der-programs", ""); w.Code != http.StatusNotFound {
		t.Fatalf("list status = %d %s, want 404", w.Code, w.Body.String())
	}
}

// An admin FSA id is operator text, and a copy's href is built from it.
func TestCreateProgramTemplate_FSAIDThatCannotFormAnHref_409(t *testing.T) {
	t.Parallel()
	f := newTemplateFixture(t)
	if err := f.h.AdminFSAs.Create(context.Background(), "roof fleet", sep2.FunctionSetAssignments{MRID: "roof fleet"}); err != nil {
		t.Fatal(err)
	}
	if w := f.do(http.MethodPost, "/api/fsas/roof%20fleet/der-programs", voltSupportBody); w.Code != http.StatusConflict {
		t.Fatalf("status = %d %s, want 409", w.Code, w.Body.String())
	}
	if n := len(f.h.AdminFSAs.Templates(context.Background(), "roof fleet")); n != 0 {
		t.Errorf("%d templates stored", n)
	}
}

func TestCreateProgramTemplate_DuplicateMRIDOnFSA_409(t *testing.T) {
	t.Parallel()
	f := newTemplateFixture(t)
	body := `{"description":"d","primacy":1,"mRID":"0123456789ABCDEF0123456789ABCDEF","defaultControl":{"type":"connect"}}`
	if w := f.do(http.MethodPost, "/api/fsas/fsa-A/der-programs", body); w.Code != http.StatusCreated {
		t.Fatalf("first: %d %s", w.Code, w.Body.String())
	}
	if w := f.do(http.MethodPost, "/api/fsas/fsa-A/der-programs", strings.ToLower(body)); w.Code != http.StatusConflict {
		t.Fatalf("second: %d %s, want 409", w.Code, w.Body.String())
	}
	if n := len(f.h.AdminFSAs.Templates(context.Background(), "fsa-A")); n != 1 {
		t.Errorf("%d templates stored, want 1", n)
	}
}

// A device that already holds a program with the template's mRID refuses the
// whole create: no template and no copy on any other device.
func TestCreateProgramTemplate_DeviceMRIDConflict_409_WritesNothing(t *testing.T) {
	t.Parallel()
	f := newTemplateFixture(t)
	f.assign(t, "1", "fsa-A")
	f.assign(t, "2", "fsa-A")
	f.notifier.take()
	fixture := sep2.DERProgram{MRID: "0123456789ABCDEF0123456789ABCDEF"}
	fixture.Href = "/edev/2/fsa/fx/derp/0"
	if err := f.copies.Programs.Create(context.Background(), "2", "0", fixture); err != nil {
		t.Fatal(err)
	}
	w := f.do(http.MethodPost, "/api/fsas/fsa-A/der-programs", `{"description":"d","primacy":1,"mRID":"0123456789abcdef0123456789abcdef","defaultControl":{"type":"connect"}}`)
	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d %s, want 409", w.Code, w.Body.String())
	}
	if n := len(f.h.AdminFSAs.Templates(context.Background(), "fsa-A")); n != 0 {
		t.Errorf("%d templates stored", n)
	}
	assertNoDeviceCopy(t, f.copies, "1")
	if got := f.notifier.take(); len(got) != 0 {
		t.Errorf("notifications: %+v", got)
	}
}

func TestCreateProgramTemplate_CappedAt16PerFSA(t *testing.T) {
	t.Parallel()
	f := newTemplateFixture(t)
	for i := range 16 {
		if w := f.do(http.MethodPost, "/api/fsas/fsa-A/der-programs", voltSupportBody); w.Code != http.StatusCreated {
			t.Fatalf("template %d: %d %s", i, w.Code, w.Body.String())
		}
	}
	if w := f.do(http.MethodPost, "/api/fsas/fsa-A/der-programs", voltSupportBody); w.Code != http.StatusConflict {
		t.Fatalf("17th: %d %s, want 409", w.Code, w.Body.String())
	}
	if n := len(f.h.AdminFSAs.Templates(context.Background(), "fsa-A")); n != 16 {
		t.Errorf("%d templates stored, want 16", n)
	}
}

type failOnDevice struct {
	fsaprogram.CopyProgramStore
	device string
	err    error
	id     *string
}

func (s failOnDevice) Create(ctx context.Context, parentID, id string, p sep2.DERProgram) error {
	*s.id = id
	if parentID == s.device {
		return s.err
	}
	return s.CopyProgramStore.Create(ctx, parentID, id, p)
}

// A copy that cannot be written removes the template and every copy already
// written, and the 500 carries a fixed message, not the store's error.
func TestCreateProgramTemplate_CopyWriteFailureUndoesEverything(t *testing.T) {
	t.Parallel()
	f := newTemplateFixture(t)
	f.assign(t, "1", "fsa-A")
	f.assign(t, "2", "fsa-A")
	f.notifier.take()
	var id string
	f.h.Copies.Programs = failOnDevice{CopyProgramStore: f.copies.Programs, device: "2", err: errors.New("disk /secret/path full"), id: &id}

	w := f.do(http.MethodPost, "/api/fsas/fsa-A/der-programs", voltSupportBody)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d %s, want 500", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "secret") {
		t.Errorf("500 body leaks the store error: %s", w.Body.String())
	}
	if n := len(f.h.AdminFSAs.Templates(context.Background(), "fsa-A")); n != 0 {
		t.Errorf("%d templates left after the failed create", n)
	}
	if id == "" {
		t.Fatal("no program write was attempted")
	}
	for _, dev := range []string{"1", "2"} {
		assertNoDeviceCopy(t, f.copies, dev)
		if _, err := f.copies.Defaults.Get(context.Background(), dev+"/fsa-A/"+id, coresingleton.SingletonKey); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("device %s default control lookup err = %v", dev, err)
		}
		if _, err := f.copies.Origins.Get(context.Background(), dev, id); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("device %s origin lookup err = %v", dev, err)
		}
	}
	if got := f.notifier.take(); len(got) != 0 {
		t.Errorf("notifications: %+v", got)
	}
}

func TestListProgramTemplates(t *testing.T) {
	t.Parallel()
	f := newTemplateFixture(t)
	w := f.do(http.MethodGet, "/api/fsas/fsa-A/der-programs", "")
	if w.Code != http.StatusOK || strings.TrimSpace(w.Body.String()) != `{"templates":[]}` {
		t.Fatalf("empty list = %d %s, want 200 {\"templates\":[]}", w.Code, w.Body.String())
	}
	f.assign(t, "3", "fsa-A")
	created := f.do(http.MethodPost, "/api/fsas/fsa-A/der-programs", voltSupportBody)
	var want templateView
	if err := json.Unmarshal(created.Body.Bytes(), &want); err != nil {
		t.Fatal(err)
	}
	w = f.do(http.MethodGet, "/api/fsas/fsa-A/der-programs", "")
	var got struct {
		Templates []templateView `json:"templates"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil || w.Code != http.StatusOK {
		t.Fatalf("list = %d %s: %v", w.Code, w.Body.String(), err)
	}
	if len(got.Templates) != 1 || got.Templates[0].ID != want.ID || got.Templates[0].MRID != want.MRID ||
		got.Templates[0].DefaultControl.MRID != want.DefaultControl.MRID ||
		!slices.Equal(got.Templates[0].Copies, []string{"/edev/3/fsa/fsa-A/derp/" + want.ID}) {
		t.Errorf("list = %+v, want the created template %+v", got.Templates, want)
	}
}

// A device assigned after the template exists gets a copy, and its FSA list
// and the FSA's program list are notified.
func TestAssignDeviceFSA_CopiesTheFSAsTemplates(t *testing.T) {
	t.Parallel()
	f := newTemplateFixture(t)
	created := f.do(http.MethodPost, "/api/fsas/fsa-A/der-programs", voltSupportBody)
	var v templateView
	if err := json.Unmarshal(created.Body.Bytes(), &v); err != nil || created.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", created.Code, created.Body.String())
	}
	f.notifier.take()

	f.assign(t, "3", "fsa-A")
	assertDeviceCopy(t, f.copies, "3", "fsa-A", v)
	want := []recordedNotification{{"/edev/3/fsa", sep2.NotificationStatusDefault}, {"/edev/3/fsa/fsa-A/derp", sep2.NotificationStatusDefault}}
	if got := f.notifier.take(); !slices.Equal(got, want) {
		t.Errorf("notifications = %+v, want %+v", got, want)
	}
}

func TestAssignDeviceFSA_ConflictingProgram_409_AssignsNothing(t *testing.T) {
	t.Parallel()
	f := newTemplateFixture(t)
	if w := f.do(http.MethodPost, "/api/fsas/fsa-A/der-programs", `{"description":"d","primacy":1,"mRID":"0123456789ABCDEF0123456789ABCDEF","defaultControl":{"type":"connect"}}`); w.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	fixture := sep2.DERProgram{MRID: "0123456789ABCDEF0123456789ABCDEF"}
	fixture.Href = "/edev/3/fsa/fx/derp/0"
	if err := f.copies.Programs.Create(context.Background(), "3", "0", fixture); err != nil {
		t.Fatal(err)
	}
	f.notifier.take()

	w := f.do(http.MethodPost, "/api/devices/3/fsa-assignment", `{"fsaHref":"/api/fsas/fsa-A"}`)
	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d %s, want 409", w.Code, w.Body.String())
	}
	if devs := f.h.AdminFSAs.Devices(context.Background(), "fsa-A"); len(devs) != 0 {
		t.Errorf("devices assigned = %q, want none", devs)
	}
	if _, err := f.h.DeviceFSAs.Get(context.Background(), "3", "fsa-A"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("scoped FSA err = %v, want ErrNotFound", err)
	}
	if got := f.notifier.take(); len(got) != 0 {
		t.Errorf("notifications: %+v", got)
	}
}

// A copy left behind by an unassign is the same template's copy, so assigning
// the device again succeeds instead of reporting a conflict with itself.
func TestAssignDeviceFSA_AgainAfterUnassign(t *testing.T) {
	t.Parallel()
	f := newTemplateFixture(t)
	f.assign(t, "1", "fsa-A")
	created := f.do(http.MethodPost, "/api/fsas/fsa-A/der-programs", voltSupportBody)
	var v templateView
	if err := json.Unmarshal(created.Body.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	if w := f.do(http.MethodDelete, "/api/devices/1/fsa-assignment?fsaHref=/api/fsas/fsa-A", ""); w.Code != http.StatusNoContent {
		t.Fatalf("unassign: %d %s", w.Code, w.Body.String())
	}
	f.assign(t, "1", "fsa-A")
	assertDeviceCopy(t, f.copies, "1", "fsa-A", v)
}
