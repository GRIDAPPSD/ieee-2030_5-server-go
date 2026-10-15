package handler_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/fsaprogram"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/handler"
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
	path := func(name string) string {
		if dir == "" {
			return ""
		}
		return filepath.Join(dir, name+".json")
	}
	programs, err := memory.NewDERProgramStoreWithPersistence(path("derprograms"))
	if err != nil {
		t.Fatal(err)
	}
	origins, err := memory.NewProgramOriginStore(path("program-origins"))
	if err != nil {
		t.Fatal(err)
	}
	fsas, err := memory.NewAdminFSAStoreWithPersistence(path("fsas"))
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
		Persist:  handler.ProgramPersistence{Programs: programs, Origins: origins, Templates: fsas},
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
