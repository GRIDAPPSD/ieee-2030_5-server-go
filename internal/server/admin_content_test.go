package server_test

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2/encoding"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/adminplane"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/auth"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/content"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
)

// #1245: one family registered on the content owner, loaded through the real
// admin router, read back over a protocol route, replaced, deleted, and read
// again after a restart. The protocol route stands in for the handler a
// function set's own slice mounts: it reads the store Register returned.

const contentFamily = "messages"

func contentXML(mrid, desc string) string {
	return `<MessagingProgram xmlns="urn:ieee:std:2030.5:ns" href="/msg/p1"><mRID>` + mrid +
		`</mRID><description>` + desc + `</description><primacy>3</primacy></MessagingProgram>`
}

// contentHarness is one server run: an owner over dataDir, its admin router
// and the protocol mux reading the registered store.
type contentHarness struct {
	admin, protocol http.Handler
}

func newContentHarness(t *testing.T, dataDir string) *contentHarness {
	t.Helper()
	owner := content.NewOwner(func(name string) string { return filepath.Join(dataDir, name+".json") })
	st, err := content.Register(owner, content.Spec[sep2.MessagingProgram]{
		Name: contentFamily,
		Validate: func(p sep2.MessagingProgram) error {
			if p.Href == "" || p.MRID == "" {
				return errRequired
			}
			return nil
		},
	})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	stores := newTestStores()
	stores.Content = owner
	admin, _ := adminplane.BuildAdminRouter(
		"the-key", newScopeTestCertService(t), stores, "GCM",
		auth.NewTicketStore(30*time.Second), auth.NewSessionStore(30*time.Minute, 8*time.Hour),
		adminplane.DefaultAdminAllowedHosts(), nil,
	)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /msg/{id}", func(w http.ResponseWriter, r *http.Request) {
		p, err := st.Get(r.Context(), r.PathValue("id"))
		if err == store.ErrNotFound {
			http.NotFound(w, r)
			return
		}
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		encoding.WriteXML(w, http.StatusOK, p)
	})
	return &contentHarness{admin: admin, protocol: mux}
}

type requiredErr struct{}

func (requiredErr) Error() string { return "href and mRID are required" }

var errRequired = requiredErr{}

func (h *contentHarness) write(method, id, body string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.admin.ServeHTTP(rec, bearerFromLoopbackRequest(method, "/api/content/"+contentFamily+"/"+id, "application/sep+xml", body))
	return rec
}

func (h *contentHarness) read(id string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.protocol.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/msg/"+id, nil))
	return rec
}

func TestContentLoadServeReplaceDeleteAndRestart(t *testing.T) {
	dir := t.TempDir()
	h := newContentHarness(t, dir)

	if rec := h.read("p1"); rec.Code != http.StatusNotFound {
		t.Fatalf("read before load = %d, want 404", rec.Code)
	}

	if rec := h.write(http.MethodPost, "p1", contentXML("AA01", "first")); rec.Code != http.StatusCreated {
		t.Fatalf("load = %d %s, want 201", rec.Code, rec.Body)
	}
	rec := h.read("p1")
	if rec.Code != http.StatusOK {
		t.Fatalf("read after load = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{`href="/msg/p1"`, "<mRID>AA01</mRID>", "<description>first</description>", "<primacy>3</primacy>"} {
		if !strings.Contains(body, want) {
			t.Errorf("served body lacks %q: %s", want, body)
		}
	}
	if strings.Index(body, "<mRID>") > strings.Index(body, "<description>") {
		t.Errorf("served body is not in XSD order (mRID before description): %s", body)
	}

	if rec := h.write(http.MethodPost, "p1", contentXML("AA01", "dup")); rec.Code != http.StatusConflict {
		t.Errorf("second load = %d, want 409", rec.Code)
	}
	if rec := h.write(http.MethodPut, "p1", contentXML("AA01", "second")); rec.Code != http.StatusOK {
		t.Fatalf("replace = %d %s, want 200", rec.Code, rec.Body)
	}
	if body := h.read("p1").Body.String(); !strings.Contains(body, "<description>second</description>") {
		t.Errorf("served body after replace: %s, want description second", body)
	}
	if rec := h.write(http.MethodPut, "absent", contentXML("AA09", "x")); rec.Code != http.StatusNotFound {
		t.Errorf("replace of an absent id = %d, want 404", rec.Code)
	}

	// A new server run over the same data directory serves what was loaded.
	restarted := newContentHarness(t, dir)
	if body := restarted.read("p1").Body.String(); !strings.Contains(body, "<description>second</description>") || !strings.Contains(body, "<mRID>AA01</mRID>") {
		t.Errorf("after restart served %q, want the replaced content", body)
	}

	if rec := restarted.write(http.MethodDelete, "p1", ""); rec.Code != http.StatusNoContent {
		t.Fatalf("delete = %d %s, want 204", rec.Code, rec.Body)
	}
	if rec := restarted.read("p1"); rec.Code != http.StatusNotFound {
		t.Errorf("read after delete = %d, want 404", rec.Code)
	}
	if rec := newContentHarness(t, dir).read("p1"); rec.Code != http.StatusNotFound {
		t.Errorf("read after delete and a second restart = %d, want 404", rec.Code)
	}
}

func TestContentWritesRefuseBadInputAndMissingCredential(t *testing.T) {
	h := newContentHarness(t, t.TempDir())

	for _, c := range []struct {
		name, id, body string
		want           int
	}{
		{"not xml", "p1", "{nope", http.StatusBadRequest},
		{"missing required field", "p1", strings.Replace(contentXML("AA01", "d"), `href="/msg/p1"`, "", 1), http.StatusBadRequest},
		{"unknown element", "p1", strings.Replace(contentXML("AA01", "d"), "<primacy>", "<surcharge>9</surcharge><primacy>", 1), http.StatusBadRequest},
		{"bad id", "a%20b", contentXML("AA01", "d"), http.StatusBadRequest},
	} {
		rec := h.write(http.MethodPost, c.id, c.body)
		if rec.Code != c.want {
			t.Errorf("%s: status = %d %s, want %d", c.name, rec.Code, rec.Body, c.want)
		}
		if rec := h.read("p1"); rec.Code != http.StatusNotFound {
			t.Errorf("%s: a refused body left p1 served (%d)", c.name, rec.Code)
		}
	}

	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete} {
		req := bypassOnlyRequest(method, "/api/content/"+contentFamily+"/p1")
		req.Header.Set("Content-Type", "application/sep+xml")
		rec := httptest.NewRecorder()
		h.admin.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized || rec.Body.String() != sensitiveRefusalBody {
			t.Errorf("%s from loopback with no credential = %d %q, want the 401 refusal", method, rec.Code, rec.Body)
		}
	}
	if rec := h.read("p1"); rec.Code != http.StatusNotFound {
		t.Errorf("an uncredentialed write left p1 served (%d)", rec.Code)
	}

	rec := httptest.NewRecorder()
	h.admin.ServeHTTP(rec, bypassOnlyRequest(http.MethodGet, "/api/content/"+contentFamily+"/p1"))
	if rec.Code != http.StatusUnauthorized || rec.Body.String() != sensitiveRefusalBody {
		t.Errorf("GET under /api/content/ with no credential = %d %q, want the 401 refusal", rec.Code, rec.Body)
	}
}
