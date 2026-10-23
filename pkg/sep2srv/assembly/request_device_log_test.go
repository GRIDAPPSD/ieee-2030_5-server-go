package assembly_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/assembly"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/devicelog"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
)

// logServer serves the management fleet through a router that writes the
// device log into a fresh directory. closeLog drains the writer so the files
// can be read.
func logServer(t *testing.T, policy assembly.AuthPolicy) (srv *httptest.Server, dir string, closeLog func()) {
	t.Helper()
	srv, dir, closeLog, _ = logServerWith(t, policy, nil, nil)
	return srv, dir, closeLog
}

// logServerWith is logServer with wrapManagers applied to the fleet's
// management store before the router is built.
func logServerWith(t *testing.T, policy assembly.AuthPolicy, wrapManagers func(store.EndDeviceManagementStore) store.EndDeviceManagementStore, wrapDevices func(store.EndDeviceStore) store.EndDeviceStore) (srv *httptest.Server, dir string, closeLog func(), w *devicelog.Writer) {
	t.Helper()
	return logServerSized(t, 1_000_000, policy, wrapManagers, wrapDevices)
}

// logServerSized is logServerWith with the per-file size limit set to
// maxBytes.
func logServerSized(t *testing.T, maxBytes int64, policy assembly.AuthPolicy, wrapManagers func(store.EndDeviceManagementStore) store.EndDeviceManagementStore, wrapDevices func(store.EndDeviceStore) store.EndDeviceStore) (srv *httptest.Server, dir string, closeLog func(), w *devicelog.Writer) {
	t.Helper()
	stores := newManagementFleet(t).stores
	if wrapManagers != nil {
		stores.EndDeviceManagers = wrapManagers(stores.EndDeviceManagers)
	}
	if wrapDevices != nil {
		stores.EndDevices = wrapDevices(stores.EndDevices)
	}
	return logServerFor(t, maxBytes, policy, stores)
}

// logServerFor serves stores through a router that writes the device log into
// a fresh directory, so a test can seed the stores first.
func logServerFor(t *testing.T, maxBytes int64, policy assembly.AuthPolicy, stores *assembly.Stores) (srv *httptest.Server, dir string, closeLog func(), w *devicelog.Writer) {
	t.Helper()
	dir = t.TempDir()
	w, err := devicelog.New(devicelog.Config{Dir: dir, MaxBytes: maxBytes})
	if err != nil {
		t.Fatalf("devicelog.New: %v", err)
	}
	closed := false
	closeLog = func() {
		if closed {
			return
		}
		closed = true
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := w.Close(ctx); err != nil {
			t.Fatalf("Close: %v", err)
		}
	}
	t.Cleanup(closeLog)
	handler, _ := assembly.BuildProtocolRouter(assembly.RouterConfig{DeviceLog: w}, stores, policy, "serverSFDI", "serverLFDI", nil)
	srv = httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return srv, dir, closeLog, w
}

// ownerLogServer is logServer with the fleet's pairs removed, for a test whose
// device connects on its own certificate.
func ownerLogServer(t *testing.T, policy assembly.AuthPolicy) (srv *httptest.Server, dir string, closeLog func()) {
	t.Helper()
	srv, dir, closeLog, _ = logServerWith(t, policy, emptyManagers, nil)
	return srv, dir, closeLog
}

func readLogLines(t *testing.T, path string) (raw string, lines []map[string]any) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	for _, l := range strings.Split(strings.TrimSuffix(string(b), "\n"), "\n") {
		var m map[string]any
		if err := json.Unmarshal([]byte(l), &m); err != nil {
			t.Fatalf("line %q is not one JSON object: %v", l, err)
		}
		lines = append(lines, m)
	}
	return string(b), lines
}

// warmDeviceLog sends GET path as lfdi until the device's own file exists. A
// device's first lines go to the shared file while its registration lookup
// runs, so this is the point from which its lines reach its own file.
func warmDeviceLog(t *testing.T, srv *httptest.Server, dir, lfdi, path string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if status, _ := gateRequest(t, srv, http.MethodGet, path, lfdi, ""); status != http.StatusOK {
			t.Fatalf("warm-up GET %s status = %d, want 200", path, status)
		}
		time.Sleep(5 * time.Millisecond)
		if _, err := os.Stat(filepath.Join(dir, lfdi+".jsonl")); err == nil {
			// The target lookup started with the registration lookup; one more
			// request after a pause finds both cached.
			time.Sleep(50 * time.Millisecond)
			gateRequest(t, srv, http.MethodGet, path, lfdi, "")
			return
		}
	}
	t.Fatalf("%s never got its own file; the registration lookup did not complete", lfdi)
}

// linesFor returns the lines of file whose path is path.
func linesFor(t *testing.T, file, path string) (raw string, out []map[string]any) {
	t.Helper()
	raw, all := readLogLines(t, file)
	for _, l := range all {
		if l["path"] == path {
			out = append(out, l)
		}
	}
	return raw, out
}

func logDirNames(t *testing.T, dir string) []string {
	t.Helper()
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range ents {
		names = append(names, e.Name())
	}
	return names
}

func putDERStatus(t *testing.T, srv *httptest.Server, asLFDI, query string) int {
	t.Helper()
	body := sep2Doc("DERStatus", `<readingTime>1700000102</readingTime>`)
	req, err := http.NewRequest(http.MethodPut, srv.URL+"/edev/"+victimID+"/der/"+victimID+"/ders"+query, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set(gateIdentityHeader, asLFDI)
	req.Header.Set("Content-Type", "application/sep+xml")
	req.Header.Set("Authorization", "Bearer header-secret-value")
	req.Header.Set("Cookie", "session=cookie-secret-value")
	status, _ := sendGateRequest(t, req)
	return status
}

func TestDeviceLogRecordsOwnRequestWithoutQueryOrHeaders(t *testing.T) {
	t.Parallel()
	srv, dir, closeLog := ownerLogServer(t, gateTestPolicy())
	warmDeviceLog(t, srv, dir, victimLFDI, "/edev/"+victimID)

	if status := putDERStatus(t, srv, victimLFDI, "?token=query-secret-value"); status != http.StatusNoContent {
		t.Fatalf("PUT ders status = %d, want 204", status)
	}
	closeLog()

	raw, lines := linesFor(t, filepath.Join(dir, victimLFDI+".jsonl"), "/edev/"+victimID+"/der/"+victimID+"/ders")
	if len(lines) != 1 {
		t.Fatalf("got %d lines, want 1: %s", len(lines), raw)
	}
	l := lines[0]
	want := map[string]any{
		"kind":   "request",
		"actor":  victimLFDI,
		"edev":   victimID,
		"target": victimLFDI,
		"method": http.MethodPut,
		"path":   "/edev/" + victimID + "/der/" + victimID + "/ders",
		"status": float64(http.StatusNoContent),
	}
	for k, v := range want {
		if l[k] != v {
			t.Errorf("field %s = %v, want %v", k, l[k], v)
		}
	}
	if n, _ := l["req_bytes"].(float64); n <= 0 {
		t.Errorf("req_bytes = %v, want the PUT body length", l["req_bytes"])
	}
	if _, ok := l["dur_ms"].(float64); !ok {
		t.Errorf("dur_ms = %v, want a number", l["dur_ms"])
	}
	for _, leaked := range []string{"query-secret-value", "token", "header-secret-value", "cookie-secret-value", "Authorization", "Cookie", "readingTime"} {
		if strings.Contains(raw, leaked) {
			t.Errorf("line carries %q: %s", leaked, raw)
		}
	}
	for _, name := range logDirNames(t, dir) {
		if name != victimLFDI+".jsonl" && name != devicelog.UnregisteredFile+".jsonl" {
			t.Errorf("directory holds %s, want only the actor's file and the shared file", name)
		}
	}
}

func TestDeviceLogAggregatorRequestLandsInAggregatorFileNamingManagedDevice(t *testing.T) {
	t.Parallel()
	srv, dir, closeLog := logServer(t, gateTestPolicy())
	warmDeviceLog(t, srv, dir, managerLFDI, "/edev/"+victimID)

	if status := putDERStatus(t, srv, managerLFDI, ""); status != http.StatusNoContent {
		t.Fatalf("manager PUT ders status = %d, want 204", status)
	}
	closeLog()

	_, lines := linesFor(t, filepath.Join(dir, managerLFDI+".jsonl"), "/edev/"+victimID+"/der/"+victimID+"/ders")
	if len(lines) != 1 {
		t.Fatalf("aggregator file has %d lines, want 1", len(lines))
	}
	if lines[0]["actor"] != managerLFDI || lines[0]["target"] != victimLFDI || lines[0]["edev"] != victimID {
		t.Errorf("actor/target/edev = %v/%v/%v, want %s/%s/%s", lines[0]["actor"], lines[0]["target"], lines[0]["edev"], managerLFDI, victimLFDI, victimID)
	}
	if _, err := os.Stat(filepath.Join(dir, victimLFDI+".jsonl")); !os.IsNotExist(err) {
		t.Errorf("managed device's file exists (err=%v); the line belongs to the requester only", err)
	}
}

func TestDeviceLogUnregisteredCertificateGoesToSharedFile(t *testing.T) {
	t.Parallel()
	srv, dir, closeLog := logServer(t, gateTestPolicy())

	if status, _ := gateRequest(t, srv, http.MethodGet, "/dcap", recordlessLFDI, ""); status != http.StatusOK {
		t.Fatalf("GET /dcap status = %d, want 200", status)
	}
	closeLog()

	_, lines := readLogLines(t, filepath.Join(dir, devicelog.UnregisteredFile+".jsonl"))
	if len(lines) != 1 || lines[0]["actor"] != recordlessLFDI || lines[0]["path"] != "/dcap" {
		t.Errorf("unregistered lines = %v, want one /dcap line with actor %s", lines, recordlessLFDI)
	}
	if _, present := lines[0]["edev"]; present {
		t.Errorf("edev present on /dcap: %v", lines[0])
	}
	if names := logDirNames(t, dir); len(names) != 1 {
		t.Errorf("directory holds %v, want only the shared file", names)
	}
}

func TestDeviceLogSkipsRefusedAndAnonymousRequests(t *testing.T) {
	t.Parallel()
	srv, dir, closeLog := ownerLogServer(t, activityPolicy(victimLFDI))

	req, err := http.NewRequest(http.MethodGet, srv.URL+"/dcap", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-Test-Deny", "1")
	if status, _ := sendGateRequest(t, req); status != http.StatusForbidden {
		t.Fatalf("control: refused request status = %d, want 403", status)
	}
	if status, _ := gateRequest(t, srv, http.MethodGet, "/dcap", "", ""); status != http.StatusOK {
		t.Fatalf("control: served request status = %d, want 200", status)
	}
	closeLog()

	total := 0
	for _, name := range logDirNames(t, dir) {
		_, lines := readLogLines(t, filepath.Join(dir, name))
		total += len(lines)
	}
	if total != 1 {
		t.Errorf("got %d lines across %v, want only the served request's", total, logDirNames(t, dir))
	}
}

func TestDeviceLogFailureNeverFailsTheRequest(t *testing.T) {
	t.Parallel()
	srv, dir, closeLog := ownerLogServer(t, gateTestPolicy())
	closeLog()

	if status := putDERStatus(t, srv, victimLFDI, ""); status != http.StatusNoContent {
		t.Errorf("PUT ders with the log closed: status = %d, want 204", status)
	}
	if names := logDirNames(t, dir); len(names) != 0 {
		t.Errorf("closed writer wrote %v", names)
	}
}

// stalledManagers blocks the lookups the device log writer makes until release
// closes, as a management store stuck on a lock or a slow backend would.
// ManagerOf is not stalled: the exclusivity check reads it on the request
// path, so a stall there holds the request by design.
type stalledManagers struct {
	store.EndDeviceManagementStore
	entered chan struct{}
	release chan struct{}
}

func (s stalledManagers) stall() {
	select {
	case s.entered <- struct{}{}:
	default:
	}
	<-s.release
}

func (s stalledManagers) ManagedBy(ctx context.Context, lfdi string) ([]string, error) {
	s.stall()
	return s.EndDeviceManagementStore.ManagedBy(ctx, lfdi)
}

func TestDeviceLogStalledManagementStoreNeverHoldsARequest(t *testing.T) {
	t.Parallel()
	stalled := stalledManagers{entered: make(chan struct{}, 1), release: make(chan struct{})}
	srv, dir, closeLog, _ := logServerWith(t, gateTestPolicy(), func(real store.EndDeviceManagementStore) store.EndDeviceManagementStore {
		stalled.EndDeviceManagementStore = real
		return stalled
	}, nil)
	released := false
	release := func() {
		if !released {
			released = true
			close(stalled.release)
		}
	}
	t.Cleanup(release)

	start := time.Now()
	status, _ := gateRequest(t, srv, http.MethodGet, "/dcap", recordlessLFDI, "")
	elapsed := time.Since(start)
	if status != http.StatusOK {
		t.Fatalf("GET /dcap status = %d, want 200", status)
	}
	// The control: the store really is stalled with the request already done,
	// so a request that waited on it could not have returned this fast.
	select {
	case <-stalled.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the writer never reached the management store; the stall was not exercised")
	}
	if elapsed > time.Second {
		t.Errorf("request took %v while the management store was stalled; want it unaffected", elapsed)
	}

	// The lookup is still running, so the line is filed without waiting:
	// the actor has no file yet, which makes it the shared file's line.
	closeLog()
	_, lines := readLogLines(t, filepath.Join(dir, devicelog.UnregisteredFile+".jsonl"))
	if len(lines) != 1 || lines[0]["actor"] != recordlessLFDI {
		t.Errorf("lines = %v, want one shared-file line for %s", lines, recordlessLFDI)
	}
	if names := logDirNames(t, dir); len(names) != 1 {
		t.Errorf("directory holds %v, want only the shared file", names)
	}
	release()
}

// erroringDevices fails GetByLFDI with a real error, not ErrNotFound.
type erroringDevices struct{ store.EndDeviceStore }

func (erroringDevices) GetByLFDI(context.Context, string) (sep2.EndDevice, error) {
	return sep2.EndDevice{}, errors.New("backend down")
}

func TestDeviceLogStoreErrorNeverCreatesADeviceFile(t *testing.T) {
	t.Parallel()
	srv, dir, closeLog, _ := logServerWith(t, gateTestPolicy(), emptyManagers, func(real store.EndDeviceStore) store.EndDeviceStore {
		return erroringDevices{real}
	})
	if status, _ := gateRequest(t, srv, http.MethodGet, "/dcap", victimLFDI, ""); status != http.StatusOK {
		t.Fatalf("GET /dcap status = %d, want 200", status)
	}
	closeLog()

	if names := logDirNames(t, dir); len(names) != 1 || names[0] != devicelog.UnregisteredFile+".jsonl" {
		t.Fatalf("directory holds %v, want only the shared file while the store cannot say who is registered", names)
	}
}

func TestDeviceLogStoreErrorKeepsAnExistingDeviceFile(t *testing.T) {
	t.Parallel()
	srv, dir, closeLog, _ := logServerWith(t, gateTestPolicy(), emptyManagers, func(real store.EndDeviceStore) store.EndDeviceStore {
		return erroringDevices{real}
	})
	if err := os.WriteFile(filepath.Join(dir, victimLFDI+".jsonl"), nil, 0o640); err != nil {
		t.Fatal(err)
	}
	if status, _ := gateRequest(t, srv, http.MethodGet, "/dcap", victimLFDI, ""); status != http.StatusOK {
		t.Fatalf("GET /dcap status = %d, want 200", status)
	}
	closeLog()

	_, lines := readLogLines(t, filepath.Join(dir, victimLFDI+".jsonl"))
	if len(lines) != 1 || lines[0]["actor"] != victimLFDI {
		t.Errorf("lines = %v, want the line in the device's existing file", lines)
	}
}

func TestDeviceLogHandlerPanicWritesA500LineAndStillPanics(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	w, err := devicelog.New(devicelog.Config{Dir: dir, MaxBytes: 1_000_000})
	if err != nil {
		t.Fatal(err)
	}
	policy := gateTestPolicy()
	policy.Wrap = func(next http.Handler) http.Handler { return next }
	policy.Identity = func(context.Context) (string, string, bool) { return victimLFDI, "", true }
	handler, _ := assembly.BuildProtocolRouter(assembly.RouterConfig{DeviceLog: w}, panicStores(), policy, "serverSFDI", "serverLFDI", nil)

	func() {
		defer func() {
			if recover() == nil {
				t.Error("the handler's panic did not reach the caller; recovery would behave differently")
			}
		}()
		handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/edev/1", nil))
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := w.Close(ctx); err != nil {
		t.Fatal(err)
	}
	_, lines := readLogLines(t, filepath.Join(dir, devicelog.UnregisteredFile+".jsonl"))
	if len(lines) != 1 || lines[0]["status"] != float64(http.StatusInternalServerError) || lines[0]["path"] != "/edev/1" {
		t.Errorf("lines = %v; want one /edev/1 line with status 500", lines)
	}
}

// panicDevices panics on Get, which the ownership gate calls on every
// /edev/{id} request.
type panicDevices struct{ store.EndDeviceStore }

func (panicDevices) Get(context.Context, string) (sep2.EndDevice, error) { panic("handler boom") }

func panicStores() *assembly.Stores {
	stores := testStores()
	stores.EndDevices = panicDevices{stores.EndDevices}
	return stores
}
