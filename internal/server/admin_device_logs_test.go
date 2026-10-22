package server_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/adminplane"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/auth"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/devicelog"
)

// #1066: GET /api/device-logs/{lfdi} through the real admin router, so the
// sensitive-prefix gate, the mux and the handler are exercised together.

const (
	logDevice  = "0123456789ABCDEF0123456789ABCDEF01234567"
	logManager = "FEDCBA9876543210FEDCBA9876543210FEDCBA98"
	logOther   = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
)

var logEpoch = time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)

// logLine is one stored line; sec is minutes apart so the reader's skew
// window never makes a page read a file the test did not expect.
func logLine(seq int, minute int, kind, actor, target, path string) string {
	s := fmt.Sprintf(`{"seq":%d,"t":%q,"kind":%q,"actor":%q`, seq, logEpoch.Add(time.Duration(minute)*time.Minute).Format(time.RFC3339Nano), kind, actor)
	if target != "" {
		s += fmt.Sprintf(`,"target":%q`, target)
	}
	return s + fmt.Sprintf(`,"path":%q}`, path) + "\n"
}

func writeLogs(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func logsRouter(t *testing.T, reader *devicelog.Reader, pairs ...[2]string) http.Handler {
	t.Helper()
	stores := newTestStores()
	for _, p := range pairs {
		if err := stores.EndDeviceManagers.Assign(context.Background(), p[0], p[1]); err != nil {
			t.Fatal(err)
		}
	}
	h, _, err := adminplane.Build(adminplane.Config{
		AdminKey:       "the-key",
		CertService:    newScopeTestCertService(t),
		Stores:         stores,
		TLSMode:        "GCM",
		Tickets:        auth.NewTicketStore(30 * time.Second),
		Sessions:       auth.NewSessionStore(30*time.Minute, 8*time.Hour),
		AllowedHosts:   adminplane.DefaultAdminAllowedHosts(),
		LoopbackBypass: true,
		DeviceLogs:     reader,
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	return h
}

func logsGet(t *testing.T, h http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, bearerFromLoopbackRequest(http.MethodGet, path, "", ""))
	return rec
}

type logPage struct {
	LFDI    string `json:"lfdi"`
	Entries []struct {
		Cursor string `json:"cursor"`
		Source string `json:"source"`
		Line   struct {
			Seq    int    `json:"seq"`
			Actor  string `json:"actor"`
			Target string `json:"target"`
			Path   string `json:"path"`
		} `json:"line"`
	} `json:"entries"`
	NextBefore  *string `json:"nextBefore"`
	NextAfter   *string `json:"nextAfter"`
	RotatedRead bool    `json:"rotatedRead"`
	Dropped     uint64  `json:"dropped"`
}

func decodeLogPage(t *testing.T, rec *httptest.ResponseRecorder) logPage {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body)
	}
	var p logPage
	if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
		t.Fatalf("body %q: %v", rec.Body, err)
	}
	return p
}

func (p logPage) keys() []string {
	var out []string
	for _, e := range p.Entries {
		src := e.Source
		if src == "" {
			src = "own"
		}
		out = append(out, fmt.Sprintf("%s:%d", src, e.Line.Seq))
	}
	return out
}

func newLogReader(t *testing.T) (*devicelog.Reader, string) {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	r, err := devicelog.NewReader(dir)
	if err != nil {
		t.Fatal(err)
	}
	return r.WithDropped(func() uint64 { return 7 }), dir
}

func TestDeviceLogsRequireARealCredentialAndHonorTheirStates(t *testing.T) {
	reader, dir := newLogReader(t)
	writeLogs(t, dir, map[string]string{logDevice + ".jsonl": logLine(1, 1, devicelog.KindRequest, logDevice, "", "/dcap")})
	on := logsRouter(t, reader)
	off := logsRouter(t, nil)

	for _, path := range []string{"/api/device-logs/" + logDevice, "/api/device-logs/" + logDevice + "/download", "/api/device-logs/anything"} {
		rec := httptest.NewRecorder()
		on.ServeHTTP(rec, bypassOnlyRequest(http.MethodGet, path))
		if rec.Code != http.StatusUnauthorized || rec.Body.String() != sensitiveRefusalBody {
			t.Errorf("GET %s from loopback with no credential = %d %q, want the 401 refusal", path, rec.Code, rec.Body)
		}
		rec = httptest.NewRecorder()
		off.ServeHTTP(rec, bypassOnlyRequest(http.MethodGet, path))
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("GET %s with logging off and no credential = %d, want 401 before the 503", path, rec.Code)
		}
	}

	if rec := logsGet(t, on, "/api/device-logs/"+logDevice); rec.Code != http.StatusOK {
		t.Errorf("credentialed GET = %d %s, want 200", rec.Code, rec.Body)
	}
	for _, path := range []string{"/api/device-logs/" + logDevice, "/api/device-logs/" + logDevice + "/download"} {
		rec := logsGet(t, off, path)
		if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "device log disabled") {
			t.Errorf("GET %s with logging off = %d %s, want 503 device log disabled", path, rec.Code, rec.Body)
		}
	}
}

func TestDeviceLogsRefuseBadInputWith400(t *testing.T) {
	reader, dir := newLogReader(t)
	writeLogs(t, dir, map[string]string{logDevice + ".jsonl": logLine(1, 1, devicelog.KindRequest, logDevice, "", "/dcap")})
	h := logsRouter(t, reader)
	base := "/api/device-logs/" + logDevice
	cursor := decodeLogPage(t, logsGet(t, h, base)).Entries[0].Cursor

	for _, c := range []struct{ name, path string }{
		{"short lfdi", "/api/device-logs/ABC"},
		{"non-hex lfdi", "/api/device-logs/" + strings.Repeat("Z", 40)},
		{"traversal lfdi", "/api/device-logs/..%2F..%2Fetc%2Fpasswd"},
		{"traversal lfdi download", "/api/device-logs/..%2F..%2Fetc%2Fpasswd/download"},
		{"limit 0", base + "?limit=0"},
		{"limit 1001", base + "?limit=1001"},
		{"limit negative", base + "?limit=-5"},
		{"limit text", base + "?limit=abc"},
		{"before and after", base + "?before=" + cursor + "&after=" + cursor},
		{"garbage cursor", base + "?before=%00%00"},
		{"unknown scope", base + "?scope=all"},
		{"bad kind", base + "?kind=Request"},
		{"kind injection", base + "?kind=request%2C..%2Fx"},
		{"too many kinds", base + "?kind=a,b,c,d,e,f,g,h,i"},
	} {
		rec := logsGet(t, h, c.path)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: GET %s = %d %s, want 400", c.name, c.path, rec.Code, rec.Body)
		}
	}
	if rec := logsGet(t, h, base+"?limit=1000"); rec.Code != http.StatusOK {
		t.Errorf("limit 1000 = %d, want 200", rec.Code)
	}
	if rec := logsGet(t, h, "/api/device-logs/"+logOther); rec.Code != http.StatusNotFound {
		t.Errorf("a device with no file = %d %s, want 404", rec.Code, rec.Body)
	}
}

func TestDeviceLogPageSpansRotationAndPagesWithNextBefore(t *testing.T) {
	reader, dir := newLogReader(t)
	var old, cur strings.Builder
	for s := 1; s <= 3; s++ {
		old.WriteString(logLine(s, s, devicelog.KindRequest, logDevice, "", fmt.Sprintf("/old%d", s)))
	}
	for s := 4; s <= 6; s++ {
		cur.WriteString(logLine(s, s, devicelog.KindRequest, logDevice, "", fmt.Sprintf("/cur%d", s)))
	}
	writeLogs(t, dir, map[string]string{logDevice + ".1.jsonl": old.String(), logDevice + ".jsonl": cur.String()})
	h := logsRouter(t, reader)

	p := decodeLogPage(t, logsGet(t, h, "/api/device-logs/"+logDevice+"?limit=4"))
	if got, want := strings.Join(p.keys(), ","), "own:6,own:5,own:4,own:3"; got != want {
		t.Fatalf("page 1 = %s, want %s", got, want)
	}
	if !p.RotatedRead || p.NextBefore == nil || p.LFDI != logDevice || p.Dropped != 7 {
		t.Fatalf("page 1 metadata: rotatedRead=%v nextBefore=%v lfdi=%q dropped=%d", p.RotatedRead, p.NextBefore, p.LFDI, p.Dropped)
	}
	if p.Entries[3].Line.Path != "/old3" {
		t.Errorf("the line from the rotated file reads %q, want /old3", p.Entries[3].Line.Path)
	}
	p2 := decodeLogPage(t, logsGet(t, h, "/api/device-logs/"+logDevice+"?limit=4&before="+*p.NextBefore))
	if got, want := strings.Join(p2.keys(), ","), "own:2,own:1"; got != want {
		t.Fatalf("page 2 = %s, want %s", got, want)
	}
	if p2.NextBefore != nil {
		t.Errorf("last page nextBefore = %q, want null", *p2.NextBefore)
	}

	// Refresh: after the newest cursor of page 1 there is nothing; add a
	// line and it is the only one returned.
	if p.NextAfter == nil {
		t.Fatal("nextAfter is null on a page with entries")
	}
	if got := decodeLogPage(t, logsGet(t, h, "/api/device-logs/"+logDevice+"?after="+*p.NextAfter)); len(got.Entries) != 0 {
		t.Errorf("after the newest cursor: %v, want none", got.keys())
	}
	writeLogs(t, dir, map[string]string{logDevice + ".jsonl": cur.String() + logLine(7, 7, devicelog.KindRequest, logDevice, "", "/cur7")})
	if got := decodeLogPage(t, logsGet(t, h, "/api/device-logs/"+logDevice+"?after="+*p.NextAfter)); strings.Join(got.keys(), ",") != "own:7" {
		t.Errorf("after the newest cursor with one new line: %v, want own:7", got.keys())
	}
}

func TestDeviceLogViewMergesManagerAndUnregisteredLines(t *testing.T) {
	reader, dir := newLogReader(t)
	writeLogs(t, dir, map[string]string{
		logDevice + ".jsonl": logLine(1, 10, devicelog.KindRequest, logDevice, "", "/own"),
		// The manager's request naming the device is the device's line; its
		// request naming another device is not.
		logManager + ".jsonl": logLine(1, 20, devicelog.KindRequest, logManager, logDevice, "/edev/1/der") +
			logLine(2, 30, devicelog.KindRequest, logManager, logOther, "/edev/9/der") +
			logLine(3, 40, devicelog.KindRequest, logManager, "", "/dcap"),
		// The shared file holds the device's pre-registration line and
		// another device's.
		"unregistered.jsonl": logLine(1, 5, devicelog.KindRequest, logDevice, "", "/before-registering") +
			logLine(2, 6, devicelog.KindRequest, logOther, "", "/someone-else"),
	})
	h := logsRouter(t, reader, [2]string{logManager, logDevice})

	p := decodeLogPage(t, logsGet(t, h, "/api/device-logs/"+logDevice))
	if got, want := strings.Join(p.keys(), ","), "manager:1,own:1,unregistered:1"; got != want {
		t.Fatalf("device view = %s, want %s", got, want)
	}
	if p.Entries[0].Line.Target != logDevice || p.Entries[2].Line.Path != "/before-registering" {
		t.Errorf("merged lines carry the wrong content: %+v", p.Entries)
	}

	// The aggregator's own view holds its own file and, with scope=managed,
	// the managed device's file and its shared-file lines.
	own := decodeLogPage(t, logsGet(t, h, "/api/device-logs/"+logManager))
	if got, want := strings.Join(own.keys(), ","), "own:3,own:2,own:1"; got != want {
		t.Fatalf("aggregator own view = %s, want %s", got, want)
	}
	all := decodeLogPage(t, logsGet(t, h, "/api/device-logs/"+logManager+"?scope=managed"))
	if got, want := strings.Join(all.keys(), ","), "own:3,own:2,own:1,managed:1,unregistered:1"; got != want {
		t.Fatalf("aggregator managed view = %s, want %s", got, want)
	}

	none := decodeLogPage(t, logsGet(t, h, "/api/device-logs/"+logDevice+"?kind="+devicelog.KindLogEvent))
	if none.Entries == nil || len(none.Entries) != 0 {
		t.Errorf("a page with no match has entries %v, want an empty list", none.Entries)
	}
}

func TestDeviceLogBodyNeverCarriesMarkupOrNull(t *testing.T) {
	reader, dir := newLogReader(t)
	writeLogs(t, dir, map[string]string{logDevice + ".jsonl": logLine(1, 1, devicelog.KindRequest, logDevice, "", `/<script>alert(1)</script>`)})
	h := logsRouter(t, reader)

	rec := logsGet(t, h, "/api/device-logs/"+logDevice)
	if strings.Contains(rec.Body.String(), "<script>") {
		t.Errorf("response carries raw markup: %s", rec.Body)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	if rec.Header().Get("X-Content-Type-Options") != "nosniff" || rec.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("headers = %v, want nosniff and no-store", rec.Header())
	}
	empty := logsGet(t, h, "/api/device-logs/"+logDevice+"?kind="+devicelog.KindLogEvent).Body.String()
	if !strings.Contains(empty, `"entries":[]`) || !strings.Contains(empty, `"nextBefore":null`) {
		t.Errorf("empty page serialises as %s, want entries [] and nextBefore null", empty)
	}
	p := decodeLogPage(t, rec)
	if p.Entries[0].Line.Path != `/<script>alert(1)</script>` {
		t.Errorf("decoded path = %q, markup must survive as data", p.Entries[0].Line.Path)
	}
}

func TestDeviceLogDownloadIsTheTwoFilesOldestFirst(t *testing.T) {
	reader, dir := newLogReader(t)
	oldBody := logLine(1, 1, devicelog.KindRequest, logDevice, "", "/a") + logLine(2, 2, devicelog.KindRequest, logDevice, "", "/b")
	curBody := logLine(3, 3, devicelog.KindRequest, logDevice, "", "/c")
	writeLogs(t, dir, map[string]string{logDevice + ".1.jsonl": oldBody, logDevice + ".jsonl": curBody})
	h := logsRouter(t, reader)

	rec := logsGet(t, h, "/api/device-logs/"+logDevice+"/download")
	if rec.Code != http.StatusOK || rec.Body.String() != oldBody+curBody {
		t.Fatalf("download = %d %q, want 200 %q", rec.Code, rec.Body, oldBody+curBody)
	}
	if got := rec.Header().Get("Content-Type"); got != "application/x-ndjson" {
		t.Errorf("Content-Type = %q", got)
	}
	if got, want := rec.Header().Get("Content-Disposition"), `attachment; filename="`+logDevice+`.jsonl"`; got != want {
		t.Errorf("Content-Disposition = %q, want %q", got, want)
	}
	if rec := logsGet(t, h, "/api/device-logs/"+logOther+"/download"); rec.Code != http.StatusNotFound {
		t.Errorf("download for a device with no file = %d, want 404", rec.Code)
	}
}

func TestDeviceLogReadRefusesASymlinkedLogFile(t *testing.T) {
	reader, dir := newLogReader(t)
	target := filepath.Join(t.TempDir(), "secret.jsonl")
	if err := os.WriteFile(target, []byte(logLine(1, 1, devicelog.KindRequest, logDevice, "", "/secret")), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(dir, logDevice+".jsonl")); err != nil {
		t.Skipf("no symlinks here: %v", err)
	}
	h := logsRouter(t, reader)
	for _, path := range []string{"/api/device-logs/" + logDevice, "/api/device-logs/" + logDevice + "/download"} {
		rec := logsGet(t, h, path)
		if rec.Code != http.StatusInternalServerError || strings.Contains(rec.Body.String(), "secret") {
			t.Errorf("GET %s through a symlink = %d %s, want a 500 that discloses nothing", path, rec.Code, rec.Body)
		}
	}
}

// The shared file existing is not a log for every device: a device with no
// file of its own and no line in the shared one is 404, and one with a line
// only in the shared file is 200 with it.
func TestDeviceLogOfADeviceKnownOnlyFromTheSharedFile(t *testing.T) {
	reader, dir := newLogReader(t)
	writeLogs(t, dir, map[string]string{"unregistered.jsonl": logLine(1, 1, devicelog.KindRequest, logOther, "", "/before")})
	h := logsRouter(t, reader)

	if rec := logsGet(t, h, "/api/device-logs/"+logDevice); rec.Code != http.StatusNotFound {
		t.Errorf("device with nothing anywhere = %d %s, want 404", rec.Code, rec.Body)
	}
	p := decodeLogPage(t, logsGet(t, h, "/api/device-logs/"+logOther))
	if got := strings.Join(p.keys(), ","); got != "unregistered:1" {
		t.Errorf("device with a line only in the shared file: %s, want unregistered:1", got)
	}
}
