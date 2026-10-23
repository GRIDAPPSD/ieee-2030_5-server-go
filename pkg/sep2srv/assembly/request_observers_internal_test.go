package assembly

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/devicelog"
)

func TestEdevID(t *testing.T) {
	t.Parallel()
	cases := []struct{ path, want string }{
		{"/edev/7", "7"},
		{"/edev/7/der/1/ders", "7"},
		{"/edev/", ""},
		{"/edev", ""},
		{"/dcap", ""},
		{"/tm", ""},
		{"//edev/7", ""},
		{"/edev/" + strings.Repeat("9", maxLoggedEdevID) + "/x", strings.Repeat("9", maxLoggedEdevID)},
		{"/edev/" + strings.Repeat("9", maxLoggedEdevID+1) + "/x", ""},
	}
	for _, c := range cases {
		if got := edevID(c.path); got != c.want {
			t.Errorf("edevID(%q) = %q, want %q", c.path, got, c.want)
		}
	}
}

// observedLines runs one request through requestObservers.wrap around h,
// drains the device log, and returns the JSON lines it wrote.
func observedLines(t *testing.T, h http.HandlerFunc) (raw string, lines []map[string]any) {
	t.Helper()
	dir := t.TempDir()
	w, err := devicelog.New(devicelog.Config{Dir: dir, MaxBytes: 1_000_000})
	if err != nil {
		t.Fatal(err)
	}
	const lfdi = "AABBCCDDEEFF0011223344556677889900112233"
	o := requestObservers{
		identity: func(context.Context) (string, string, bool) { return lfdi, "", true },
		log:      w,
		resolver: devicelog.NewResolver(devicelog.ResolverConfig{}),
		now:      time.Now,
	}
	rr := httptest.NewRecorder()
	o.wrap(h).ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/x", nil))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := w.Close(ctx); err != nil {
		t.Fatal(err)
	}
	files, err := filepath.Glob(filepath.Join(dir, "*.jsonl"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no log file in %s: %v", dir, err)
	}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		raw += string(b)
		for _, l := range strings.Split(strings.TrimSuffix(string(b), "\n"), "\n") {
			var m map[string]any
			if err := json.Unmarshal([]byte(l), &m); err != nil {
				t.Fatalf("line %q is not one JSON object: %v", l, err)
			}
			lines = append(lines, m)
		}
	}
	return raw, lines
}

func TestLogRequestLocationRules(t *testing.T) {
	t.Parallel()
	long := "/" + strings.Repeat("a", 400)
	cases := []struct {
		name   string
		status int
		loc    string
		want   any // nil means the field is absent
	}{
		{"relative 201", http.StatusCreated, "/mup/7", "/mup/7"},
		{"relative 204", http.StatusNoContent, "/mup/7", "/mup/7"},
		{"absolute url dropped", http.StatusCreated, "http://evil.example/mup/7", nil},
		{"network-path reference dropped", http.StatusCreated, "//evil.example/mup/7", nil},
		{"backslash network-path dropped", http.StatusCreated, `/\evil.example/mup/7`, nil},
		{"scheme-relative dropped", http.StatusCreated, "mup/7", nil},
		{"empty", http.StatusCreated, "", nil},
		{"capped at 256 bytes", http.StatusCreated, long, long[:maxLoggedLocation]},
		{"non-2xx dropped", http.StatusBadRequest, "/mup/7", nil},
		{"3xx dropped", http.StatusFound, "/mup/7", nil},
		{"newline stays inside one JSON line", http.StatusCreated, "/a\nb", "/a\nb"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			raw, lines := observedLines(t, func(w http.ResponseWriter, r *http.Request) {
				if c.loc != "" && !strings.ContainsAny(c.loc, "\r\n") {
					w.Header().Set("Location", c.loc)
				}
				if strings.ContainsAny(c.loc, "\r\n") {
					// net/http rejects a newline in a header value at the
					// wire, so set the map directly as a buggy handler could.
					w.Header()["Location"] = []string{c.loc}
				}
				w.WriteHeader(c.status)
			})
			if len(lines) != 1 || strings.Count(raw, "\n") != 1 {
				t.Fatalf("got %d lines, want exactly one: %q", len(lines), raw)
			}
			got, present := lines[0]["location"]
			if c.want == nil {
				if present {
					t.Errorf("location = %v, want absent", got)
				}
				return
			}
			if got != c.want {
				t.Errorf("location = %v, want %v", got, c.want)
			}
		})
	}
}

func TestLogRequestMergesNoteFieldsBelow400Only(t *testing.T) {
	t.Parallel()
	setNote := func(w http.ResponseWriter, r *http.Request) {
		n := devicelog.NoteFrom(r.Context())
		if err := n.Set("mmr", 4); err != nil {
			t.Errorf("Set(mmr): %v", err)
		}
		if err := n.Set("kind", "forged"); err == nil {
			t.Error("Set(kind) accepted a reserved name")
		}
	}
	_, ok := observedLines(t, func(w http.ResponseWriter, r *http.Request) {
		setNote(w, r)
		w.WriteHeader(http.StatusCreated)
	})
	if ok[0]["mmr"] != float64(4) || ok[0]["kind"] != devicelog.KindRequest {
		t.Errorf("201 line mmr = %v kind = %v, want 4 and %q", ok[0]["mmr"], ok[0]["kind"], devicelog.KindRequest)
	}
	_, failed := observedLines(t, func(w http.ResponseWriter, r *http.Request) {
		setNote(w, r)
		w.WriteHeader(http.StatusInternalServerError)
	})
	if v, present := failed[0]["mmr"]; present {
		t.Errorf("500 line carries mmr = %v, want none", v)
	}
}

func TestLogRequestRefusalReasonFromNote(t *testing.T) {
	t.Parallel()
	_, lines := observedLines(t, func(w http.ResponseWriter, r *http.Request) {
		devicelog.NoteFrom(r.Context()).Refuse("not-owner-or-manager")
		http.Error(w, "forbidden", http.StatusForbidden)
	})
	if lines[0]["kind"] != devicelog.KindRefused || lines[0]["reason"] != "not-owner-or-manager" {
		t.Errorf("line kind = %v reason = %v, want refused and the reason", lines[0]["kind"], lines[0]["reason"])
	}
}
