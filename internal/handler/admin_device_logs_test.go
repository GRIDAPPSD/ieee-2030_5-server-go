package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/devicelog"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
)

// Mounted bare, with no reader, both routes fail closed. The response headers
// are the admin wrapper's, asserted through the router in internal/server.
func TestAdminDeviceLogFailClosedWithoutAReader(t *testing.T) {
	const lfdi = "0123456789ABCDEF0123456789ABCDEF01234567"
	for name, h := range map[string]http.HandlerFunc{
		"read":     (&AdminDeviceLogHandler{}).HandleRead(),
		"download": (&AdminDeviceLogHandler{}).HandleDownload(),
	} {
		req := httptest.NewRequest(http.MethodGet, "/api/device-logs/"+lfdi, nil)
		req.SetPathValue("lfdi", lfdi)
		rec := httptest.NewRecorder()
		h(rec, req)
		if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "device log disabled") {
			t.Errorf("%s with no reader = %d %s, want 503 device log disabled", name, rec.Code, rec.Body)
		}
	}
}

func TestParseDeviceLogQueryBounds(t *testing.T) {
	for _, c := range []struct {
		query   string
		limit   int
		wantMsg bool
	}{
		{"", 200, false},
		{"limit=1", 1, false},
		{"limit=1000", 1000, false},
		{"limit=0", 0, true},
		{"limit=1001", 0, true},
		{"limit=%2B5", 5, false},
		{"limit=1e3", 0, true},
		{"scope=managed", 200, false},
		{"scope=Own", 0, true},
		{"kind=request,logevent", 200, false},
		{"kind=request,", 0, true},
	} {
		q, msg := parseDeviceLogQuery(httptest.NewRequest(http.MethodGet, "/x?"+c.query, nil))
		if (msg != "") != c.wantMsg || (!c.wantMsg && q.Limit != c.limit) {
			t.Errorf("query %q: limit=%d msg=%q, want limit=%d refused=%v", c.query, q.Limit, msg, c.limit, c.wantMsg)
		}
	}
}

type stubManagers struct {
	manager string
	managed []string
}

func (s stubManagers) ManagerOf(context.Context, string) (string, error) {
	if s.manager == "" {
		return "", store.ErrNotFound
	}
	return s.manager, nil
}

func (s stubManagers) ManagedBy(context.Context, string) ([]string, error) { return s.managed, nil }

// A manager or managed id the store holds in a non-canonical form is a 400
// naming the id, not a 500 from the reader's refusal of the file key.
func TestAdminDeviceLogNonCanonicalManagedIDIs400(t *testing.T) {
	const lfdi = "0123456789ABCDEF0123456789ABCDEF01234567"
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	body := `{"seq":1,"t":"2026-10-21T12:00:00Z","kind":"request","actor":"` + lfdi + `"}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, lfdi+".jsonl"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	reader, err := devicelog.NewReader(dir)
	if err != nil {
		t.Fatal(err)
	}
	for name, c := range map[string]struct {
		managers stubManagers
		query    string
	}{
		"managed id": {stubManagers{managed: []string{"../etc/passwd"}}, "?scope=managed"},
		"manager id": {stubManagers{manager: "not-an-lfdi"}, ""},
	} {
		h := &AdminDeviceLogHandler{Reader: reader, Managers: c.managers}
		req := httptest.NewRequest(http.MethodGet, "/api/device-logs/"+lfdi+c.query, nil)
		req.SetPathValue("lfdi", lfdi)
		rec := httptest.NewRecorder()
		h.HandleRead()(rec, req)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "canonical") {
			t.Errorf("%s: %d %s, want 400 naming a canonical LFDI", name, rec.Code, rec.Body)
		}
	}
	// Control: canonical ids read normally.
	h := &AdminDeviceLogHandler{Reader: reader, Managers: stubManagers{manager: "FEDCBA9876543210FEDCBA9876543210FEDCBA98"}}
	req := httptest.NewRequest(http.MethodGet, "/api/device-logs/"+lfdi, nil)
	req.SetPathValue("lfdi", lfdi)
	rec := httptest.NewRecorder()
	h.HandleRead()(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("canonical manager: %d %s, want 200", rec.Code, rec.Body)
	}
}
