package handler_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/fsaprogram"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/handler"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

// #923: GET /api/der-programs.

type listedProgram struct {
	Device      string `json:"device"`
	FSA         string `json:"fsa"`
	ID          string `json:"id"`
	Href        string `json:"href"`
	MRID        string `json:"mRID"`
	Description string `json:"description"`
	Primacy     uint8  `json:"primacy"`
	Version     uint16 `json:"version"`
	Origin      string `json:"origin"`
	TemplateID  string `json:"templateId"`
	OnRestart   string `json:"onRestart"`
}

type programListReply struct {
	Persists bool            `json:"persists"`
	Total    int             `json:"total"`
	Start    int             `json:"start"`
	Limit    int             `json:"limit"`
	Programs []listedProgram `json:"programs"`
}

// newProgramListHandler builds the stores over dir (in memory when dir is
// empty, as with no data_dir) and seeds one program of each origin:
// device 1 holds a template copy and a device program, device 2 a fixture
// program, which has no origin entry.
func newProgramListHandler(t *testing.T, dir string) *handler.AdminProgramListHandler {
	t.Helper()
	return newProgramListHandlerDirs(t, dir, dir, dir)
}

// newProgramListHandlerDirs persists each store under its own directory, or
// keeps it in memory when that directory is empty, so a partly persisted set
// can be built.
func newProgramListHandlerDirs(t *testing.T, programsDir, originsDir, fsasDir string) *handler.AdminProgramListHandler {
	t.Helper()
	path := func(dir, name string) string {
		if dir == "" {
			return ""
		}
		return filepath.Join(dir, name+".json")
	}
	programs, err := memory.NewDERProgramStoreWithPersistence(path(programsDir, "derprograms"))
	if err != nil {
		t.Fatal(err)
	}
	origins, err := memory.NewProgramOriginStore(path(originsDir, "program-origins"))
	if err != nil {
		t.Fatal(err)
	}
	copies := fsaprogram.Copies{Programs: programs, Defaults: memory.NewScopedStore[sep2.DefaultDERControl](), Origins: origins}
	ctx := context.Background()

	tmpl := memory.ProgramTemplate{ID: "derp-t1", MRID: "AAAA0000000000000000000000000001", Description: "volt support",
		Primacy: 1, Version: 3, DefaultMRID: "AAAA0000000000000000000000000002"}
	own := memory.ProgramTemplate{ID: "derp-d1", MRID: "BBBB0000000000000000000000000001", Description: "device own",
		Primacy: 65, Version: 0, DefaultMRID: "BBBB0000000000000000000000000002"}
	if _, err := copies.Ensure(ctx, "1", "fsa-A", tmpl); err != nil {
		t.Fatal(err)
	}
	if _, err := copies.EnsureDevice(ctx, "1", "fsa-A", own); err != nil {
		t.Fatal(err)
	}
	v := uint16(5)
	fixture := sep2.DERProgram{MRID: "CCCC0000000000000000000000000001", Description: "from fixture", Primacy: 2, Version: &v}
	fixture.Href = "/edev/2/fsa/fsa-B/derp/fx1"
	if err := programs.Create(ctx, "2", "fx1", fixture); err != nil {
		t.Fatal(err)
	}
	return &handler.AdminProgramListHandler{
		Programs: programs,
		Origins:  origins,
		Persist:  programs,
	}
}

func getPrograms(t *testing.T, h *handler.AdminProgramListHandler, query string) (int, programListReply, string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/der-programs"+query, nil)
	w := httptest.NewRecorder()
	h.HandleList().ServeHTTP(w, req)
	var out programListReply
	if w.Code == http.StatusOK {
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatalf("decode %q: %v", w.Body.String(), err)
		}
	}
	return w.Code, out, w.Body.String()
}

func wantPrograms(onRestart map[string]string) []listedProgram {
	return []listedProgram{
		{Device: "1", FSA: "fsa-A", ID: "derp-d1", Href: "/edev/1/fsa/fsa-A/derp/derp-d1", MRID: "BBBB0000000000000000000000000001",
			Description: "device own", Primacy: 65, Version: 0, Origin: "device", OnRestart: onRestart["device"]},
		{Device: "1", FSA: "fsa-A", ID: "derp-t1", Href: "/edev/1/fsa/fsa-A/derp/derp-t1", MRID: "AAAA0000000000000000000000000001",
			Description: "volt support", Primacy: 1, Version: 3, Origin: "template", TemplateID: "derp-t1", OnRestart: onRestart["template"]},
		{Device: "2", FSA: "fsa-B", ID: "fx1", Href: "/edev/2/fsa/fsa-B/derp/fx1", MRID: "CCCC0000000000000000000000000001",
			Description: "from fixture", Primacy: 2, Version: 5, Origin: "fixture", OnRestart: onRestart["fixture"]},
	}
}

func assertListed(t *testing.T, got, want []listedProgram) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("listed %d programs, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("program %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestListPrograms_WithDataDir_EveryOriginEveryField(t *testing.T) {
	t.Parallel()
	h := newProgramListHandler(t, t.TempDir())
	code, got, body := getPrograms(t, h, "")
	if code != http.StatusOK {
		t.Fatalf("status %d: %s", code, body)
	}
	if !got.Persists || got.Total != 3 || got.Start != 0 || got.Limit != 100 {
		t.Errorf("envelope = persists %v total %d start %d limit %d", got.Persists, got.Total, got.Start, got.Limit)
	}
	assertListed(t, got.Programs, wantPrograms(map[string]string{"device": "kept", "template": "kept", "fixture": "kept"}))
}

func TestListPrograms_WithoutDataDir_RestartBehaviorPerOrigin(t *testing.T) {
	t.Parallel()
	h := newProgramListHandler(t, "")
	code, got, body := getPrograms(t, h, "")
	if code != http.StatusOK {
		t.Fatalf("status %d: %s", code, body)
	}
	if got.Persists || got.Total != 3 {
		t.Errorf("envelope = persists %v total %d", got.Persists, got.Total)
	}
	assertListed(t, got.Programs, wantPrograms(map[string]string{"device": "gone", "template": "gone", "fixture": "reseeded"}))
}

func TestListPrograms_EmptyStoreIsAnEmptyArrayNotNull(t *testing.T) {
	t.Parallel()
	h := newProgramListHandler(t, "")
	empty := memory.NewDERProgramStore()
	h.Programs = empty
	_, _, body := getPrograms(t, h, "")
	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(body), &raw); err != nil {
		t.Fatal(err)
	}
	if string(raw["programs"]) != "[]" || string(raw["total"]) != "0" {
		t.Errorf("programs = %s total = %s, want [] and 0", raw["programs"], raw["total"])
	}
}

func TestListPrograms_PageLimit(t *testing.T) {
	t.Parallel()
	h := newProgramListHandler(t, "")
	all := wantPrograms(map[string]string{"device": "gone", "template": "gone", "fixture": "reseeded"})

	_, p1, _ := getPrograms(t, h, "?limit=2")
	if p1.Total != 3 || p1.Limit != 2 || p1.Start != 0 {
		t.Errorf("page 1 envelope = total %d limit %d start %d", p1.Total, p1.Limit, p1.Start)
	}
	assertListed(t, p1.Programs, all[:2])

	_, p2, _ := getPrograms(t, h, "?limit=2&start=2")
	if p2.Total != 3 || p2.Start != 2 {
		t.Errorf("page 2 envelope = total %d start %d", p2.Total, p2.Start)
	}
	assertListed(t, p2.Programs, all[2:])

	_, past, _ := getPrograms(t, h, "?start=9")
	if past.Total != 3 || len(past.Programs) != 0 {
		t.Errorf("start past the end = total %d, %d programs", past.Total, len(past.Programs))
	}
}

func TestListPrograms_LimitIsClampedAndBadInputRefused(t *testing.T) {
	t.Parallel()
	h := newProgramListHandler(t, "")
	_, got, _ := getPrograms(t, h, "?limit=100000")
	if got.Limit != 500 {
		t.Errorf("limit over the cap echoed %d, want 500", got.Limit)
	}
	for _, q := range []string{"?limit=0", "?limit=-1", "?limit=x", "?start=-1", "?start=x", "?limit=1&limit=2"} {
		if code, _, body := getPrograms(t, h, q); code != http.StatusBadRequest {
			t.Errorf("%s: status %d (%s), want 400", q, code, body)
		}
	}
}

func TestListPrograms_ABoundedReplyForManyPrograms(t *testing.T) {
	t.Parallel()
	h := newProgramListHandler(t, "")
	ctx := context.Background()
	for i := 0; i < 130; i++ {
		id := "bulk" + strconv.Itoa(1000+i)
		p := sep2.DERProgram{MRID: "DDDD" + strconv.Itoa(100000+i), Primacy: 2}
		p.Href = "/edev/3/fsa/fsa-C/derp/" + id
		if err := h.Programs.(*memory.DERProgramStore).Create(ctx, "3", id, p); err != nil {
			t.Fatal(err)
		}
	}
	_, got, _ := getPrograms(t, h, "")
	if got.Total != 133 || len(got.Programs) != 100 || got.Limit != 100 {
		t.Errorf("default page = total %d, %d programs, limit %d; want 133, 100, 100", got.Total, len(got.Programs), got.Limit)
	}
}

// Only the program store decides whether a program survives a restart: the
// boot rebuild (fsaprogram.ReconcileCopies) skips a copy it cannot rebuild
// and removes only a device origin whose program is missing, so an
// unpersisted origin index or template store changes how a program is listed
// afterwards, not whether it is kept.
func TestListPrograms_PartlyPersistedStores(t *testing.T) {
	t.Parallel()
	for _, programs := range []bool{false, true} {
		for _, origins := range []bool{false, true} {
			for _, fsas := range []bool{false, true} {
				dir := func(on bool) string {
					if on {
						return t.TempDir()
					}
					return ""
				}
				h := newProgramListHandlerDirs(t, dir(programs), dir(origins), dir(fsas))
				_, got, _ := getPrograms(t, h, "")
				want := map[string]string{"device": "gone", "template": "gone", "fixture": "reseeded"}
				if programs {
					want = map[string]string{"device": "kept", "template": "kept", "fixture": "kept"}
				}
				if got.Persists != programs {
					t.Errorf("programs=%v origins=%v fsas=%v: persists = %v", programs, origins, fsas, got.Persists)
				}
				assertListed(t, got.Programs, wantPrograms(want))
			}
		}
	}
}

// A program with no origin entry is listed as a fixture whatever its id looks
// like, so a device program whose origin entry was lost reads as fixture.
func TestListPrograms_NoOriginEntryIsAFixtureWhateverItsId(t *testing.T) {
	t.Parallel()
	h := newProgramListHandler(t, t.TempDir())
	p := sep2.DERProgram{MRID: "EEEE0000000000000000000000000001", Primacy: 2}
	p.Href = "/edev/4/fsa/fsa-A/derp/derp-00000000000000ab"
	if err := h.Programs.(*memory.DERProgramStore).Create(context.Background(), "4", "derp-00000000000000ab", p); err != nil {
		t.Fatal(err)
	}
	_, got, _ := getPrograms(t, h, "")
	last := got.Programs[len(got.Programs)-1]
	if last.ID != "derp-00000000000000ab" || last.Origin != "fixture" || last.OnRestart != "kept" || last.TemplateID != "" {
		t.Errorf("program = %+v, want origin fixture, kept", last)
	}
}

type persistStub bool

func (p persistStub) Persists() bool { return bool(p) }

func TestListPrograms_PersistsReadsTheProgramStore(t *testing.T) {
	t.Parallel()
	h := newProgramListHandler(t, "")
	h.Persist = persistStub(true)
	if _, got, _ := getPrograms(t, h, ""); !got.Persists {
		t.Error("persists = false with a persisting program store")
	}
	h.Persist = nil
	if _, got, _ := getPrograms(t, h, ""); got.Persists {
		t.Error("persists = true with no program store persistence")
	}
}

func TestListPrograms_OrderedByHrefAcrossFSAsOnOneDevice(t *testing.T) {
	t.Parallel()
	h := newProgramListHandler(t, "")
	ctx := context.Background()
	for _, c := range []struct{ fsa, id string }{{"fsa-B", "a"}, {"fsa-A", "z"}} {
		p := sep2.DERProgram{MRID: "FFFF00000000000000000000000000" + c.id + "0", Primacy: 2}
		p.Href = "/edev/0/fsa/" + c.fsa + "/derp/" + c.id
		if err := h.Programs.(*memory.DERProgramStore).Create(ctx, "0", c.id, p); err != nil {
			t.Fatal(err)
		}
	}
	_, got, _ := getPrograms(t, h, "?limit=2")
	if len(got.Programs) != 2 || got.Programs[0].Href != "/edev/0/fsa/fsa-A/derp/z" || got.Programs[1].Href != "/edev/0/fsa/fsa-B/derp/a" {
		t.Errorf("order = %+v, want fsa-A then fsa-B", got.Programs)
	}
}

func TestListPrograms_RepeatedStartRefused(t *testing.T) {
	t.Parallel()
	h := newProgramListHandler(t, "")
	if code, _, _ := getPrograms(t, h, "?start=1&start=2"); code != http.StatusBadRequest {
		t.Errorf("status %d, want 400", code)
	}
}

type failingLister struct {
	parentsErr, listErr error
	handler.ProgramLister
}

func (f failingLister) Parents(ctx context.Context) ([]string, error) {
	if f.parentsErr != nil {
		return nil, f.parentsErr
	}
	return f.ProgramLister.Parents(ctx)
}

func (f failingLister) List(ctx context.Context, parent string, opts store.ListOptions) (store.ListResult[sep2.DERProgram], error) {
	if f.listErr != nil {
		return store.ListResult[sep2.DERProgram]{}, f.listErr
	}
	return f.ProgramLister.List(ctx, parent, opts)
}

type failingOrigins struct{ err error }

func (f failingOrigins) Get(context.Context, string, string) (memory.ProgramOrigin, error) {
	return memory.ProgramOrigin{}, f.err
}

func TestListPrograms_StoreFailuresAre500WithAFixedBodyAndANamedLog(t *testing.T) {
	t.Parallel()
	boom := errors.New("disk /secret/path exploded")
	cases := []struct {
		name    string
		mutate  func(h *handler.AdminProgramListHandler)
		wantLog []string
	}{
		{"parents", func(h *handler.AdminProgramListHandler) {
			h.Programs = failingLister{parentsErr: boom, ProgramLister: h.Programs}
		}, []string{"step=parents"}},
		{"list", func(h *handler.AdminProgramListHandler) {
			h.Programs = failingLister{listErr: boom, ProgramLister: h.Programs}
		}, []string{"step=list", "device=1"}},
		{"origin", func(h *handler.AdminProgramListHandler) { h.Origins = failingOrigins{err: boom} },
			[]string{"step=origin", "device=1", "program=derp-d1"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newProgramListHandler(t, "")
			var logs bytes.Buffer
			h.Logger = slog.New(slog.NewTextHandler(&logs, nil))
			c.mutate(h)
			code, _, body := getPrograms(t, h, "")
			if code != http.StatusInternalServerError || strings.TrimSpace(body) != `{"error":"program list failed"}` {
				t.Errorf("status %d body %q, want 500 and the fixed message", code, body)
			}
			for _, w := range c.wantLog {
				if !strings.Contains(logs.String(), w) {
					t.Errorf("log %q lacks %q", logs.String(), w)
				}
			}
		})
	}
}

func TestListPrograms_UnparsableHrefIsLogged(t *testing.T) {
	t.Parallel()
	h := newProgramListHandler(t, "")
	var logs bytes.Buffer
	h.Logger = slog.New(slog.NewTextHandler(&logs, nil))
	p := sep2.DERProgram{MRID: "ABAB0000000000000000000000000001", Primacy: 2}
	p.Href = "/edev/9/derp/odd"
	if err := h.Programs.(*memory.DERProgramStore).Create(context.Background(), "9", "odd", p); err != nil {
		t.Fatal(err)
	}
	_, got, _ := getPrograms(t, h, "")
	last := got.Programs[len(got.Programs)-1]
	if last.ID != "odd" || last.FSA != "" || last.Origin != "fixture" {
		t.Errorf("program = %+v", last)
	}
	if !strings.Contains(logs.String(), "der_program_list_bad_href") || !strings.Contains(logs.String(), "device=9") {
		t.Errorf("log %q lacks the bad-href line naming device 9", logs.String())
	}
}
