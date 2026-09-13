// Tests for the EndDevice handler family.
// Ported from the reference server's internal/handler/edev_test.go and
// edev_delete_test.go, adapted to the injected IdentityFunc/SFDIPrefixFunc
// seam: tests pass closures directly rather than injecting auth context values.
package enddevice_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/xml"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	coreedev "github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/handlers/enddevice"
	corelisthandler "github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/handlers/listhandler"
	coresub "github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/handlers/subscription"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

const (
	testSFDI = "AABBCCDD11223344"
	testLFDI = "AABBCCDDEEFF001122334455667788990011223344556677"
	testID   = "AABBCCDD" // sfdi[:8]
)

// identityOK returns an IdentityFunc that always provides the given pair.
func identityOK(lfdi, sfdi string) coreedev.IdentityFunc {
	return func(_ context.Context) (string, string, bool) { return lfdi, sfdi, true }
}

// identityNone always returns ok=false (no authenticated identity).
func identityNone() coreedev.IdentityFunc {
	return func(_ context.Context) (string, string, bool) { return "", "", false }
}

// sfdiFirst8 implements SFDIPrefixFunc as the first 8 characters of the SFDI
// (mirrors auth.ExtractSFDIPrefix). Sufficient for unit tests.
func sfdiFirst8(sfdi string) (string, error) {
	if len(sfdi) < 8 {
		return "", errors.New("SFDI too short")
	}
	return sfdi[:8], nil
}

// ----- GET /edev/{id} -----

func TestHandleEndDeviceGet(t *testing.T) {
	t.Parallel()

	s := memory.NewEndDeviceStore()
	dev := sep2.EndDevice{SFDI: testSFDI, LFDI: testLFDI}
	dev.Href = "/edev/1"
	_ = s.Create(context.Background(), "1", dev)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /edev/{id}", coreedev.HandleEndDevice(s))

	req := httptest.NewRequest(http.MethodGet, "/edev/1", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var got sep2.EndDevice
	if err := xml.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.SFDI != testSFDI {
		t.Errorf("SFDI = %q, want %q", got.SFDI, testSFDI)
	}
}

func TestHandleEndDeviceGetNotFound(t *testing.T) {
	t.Parallel()

	s := memory.NewEndDeviceStore()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /edev/{id}", coreedev.HandleEndDevice(s))

	req := httptest.NewRequest(http.MethodGet, "/edev/missing", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
}

// ----- POST /edev -----

func TestHandleCreateEndDevice(t *testing.T) {
	t.Parallel()

	s := memory.NewEndDeviceStore()
	h := coreedev.HandleCreateEndDevice(s, memory.NewEndDeviceIndex(), identityOK(testLFDI, testSFDI), sfdiFirst8)

	req := httptest.NewRequest(http.MethodPost, "/edev", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body=%s", w.Code, w.Body.String())
	}
	if loc := w.Header().Get("Location"); loc == "" {
		t.Error("missing Location header")
	}
	var got sep2.EndDevice
	if err := xml.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal body: %v", err)
	}
	if got.SFDI != testSFDI {
		t.Errorf("SFDI = %q, want %q", got.SFDI, testSFDI)
	}
	if got.LFDI != testLFDI {
		t.Errorf("LFDI = %q, want %q", got.LFDI, testLFDI)
	}
}

func TestHandleCreateEndDeviceDuplicate(t *testing.T) {
	t.Parallel()

	s := memory.NewEndDeviceStore()
	h := coreedev.HandleCreateEndDevice(s, memory.NewEndDeviceIndex(), identityOK(testLFDI, testSFDI), sfdiFirst8)

	// First POST: 201 Created.
	req1 := httptest.NewRequest(http.MethodPost, "/edev", nil)
	w1 := httptest.NewRecorder()
	h.ServeHTTP(w1, req1)
	if w1.Code != http.StatusCreated {
		t.Fatalf("first POST status = %d, want 201", w1.Code)
	}

	// Second POST with same identity: idempotent 200 with existing record.
	req2 := httptest.NewRequest(http.MethodPost, "/edev", nil)
	w2 := httptest.NewRecorder()
	h.ServeHTTP(w2, req2)
	if w2.Code != http.StatusOK {
		t.Errorf("duplicate POST status = %d, want 200", w2.Code)
	}
	if loc := w2.Header().Get("Location"); loc == "" {
		t.Error("duplicate POST missing Location header")
	}
}

// TestHandleCreateEndDeviceNoIdentity: CRITICAL security gate.
// Handler must return 403 when no identity is present, never 201.
func TestHandleCreateEndDeviceNoIdentity(t *testing.T) {
	t.Parallel()

	s := memory.NewEndDeviceStore()
	h := coreedev.HandleCreateEndDevice(s, memory.NewEndDeviceIndex(), identityNone(), sfdiFirst8)

	req := httptest.NewRequest(http.MethodPost, "/edev", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Errorf("no-identity status = %d, want 403", w.Code)
	}
}

// TestHandleEndDeviceMethodNotAllowed: any method other than GET/HEAD returns 405.
func TestHandleEndDeviceMethodNotAllowed(t *testing.T) {
	t.Parallel()
	s := memory.NewEndDeviceStore()
	h := coreedev.HandleEndDevice(s)
	req := httptest.NewRequest(http.MethodPost, "/edev/1", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", w.Code)
	}
}

// TestHandleEndDeviceEmptyID: defensive 400 on empty path value.
func TestHandleEndDeviceEmptyID(t *testing.T) {
	t.Parallel()
	s := memory.NewEndDeviceStore()
	h := coreedev.HandleEndDevice(s)
	req := httptest.NewRequest(http.MethodGet, "/edev/", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

// TestHandleEndDeviceStoreError: non-ErrNotFound store error returns 500.
func TestHandleEndDeviceStoreError(t *testing.T) {
	t.Parallel()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /edev/{id}", coreedev.HandleEndDevice(deleteFailEndDeviceStore{}))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/edev/1", nil))
	if w.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", w.Code)
	}
}

// TestHandleCreateEndDeviceInvalidXML: malformed body returns 400.
func TestHandleCreateEndDeviceInvalidXML(t *testing.T) {
	t.Parallel()
	s := memory.NewEndDeviceStore()
	h := coreedev.HandleCreateEndDevice(s, memory.NewEndDeviceIndex(), identityOK(testLFDI, testSFDI), sfdiFirst8)
	req := httptest.NewRequest(http.MethodPost, "/edev", bytes.NewBufferString("<not-xml"))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

// TestHandleCreateEndDeviceMethodNotAllowed: non-POST returns 405.
func TestHandleCreateEndDeviceMethodNotAllowed(t *testing.T) {
	t.Parallel()
	s := memory.NewEndDeviceStore()
	h := coreedev.HandleCreateEndDevice(s, memory.NewEndDeviceIndex(), identityOK(testLFDI, testSFDI), sfdiFirst8)
	req := httptest.NewRequest(http.MethodGet, "/edev", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", w.Code)
	}
}

// TestHandleCreateEndDevicePanicsOnNilIndexer: idx is a required
// collaborator. A mis-wired caller that passes nil must fail loudly at
// construction (once, at router-assembly time) rather than on the first
// live POST /edev, where a nil-pointer panic would be swallowed by
// net/http's per-request recover and surfaced as a bare connection reset
// or a silent 500 to every subsequent caller.
func TestHandleCreateEndDevicePanicsOnNilIndexer(t *testing.T) {
	t.Parallel()

	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("HandleCreateEndDevice(nil idx) did not panic")
		}
	}()

	s := memory.NewEndDeviceStore()
	_ = coreedev.HandleCreateEndDevice(s, nil, identityOK(testLFDI, testSFDI), sfdiFirst8)
}

// TestHandleCreateEndDevicePanicsOnTypedNilIndexer covers the same guard for
// a different shape: a nil *memory.EndDeviceIndex passed as the
// EndDeviceIndexer interface parameter is NOT equal to the untyped nil
// literal the previous test covers, because the interface value's type half
// is set. A plain `idx == nil` comparison let exactly this shape through,
// which is the caller the constructor's own doc comment says the guard
// exists for: "any other caller of this exported constructor that passes
// nil directly." store.IsAbsent is what closes that gap.
func TestHandleCreateEndDevicePanicsOnTypedNilIndexer(t *testing.T) {
	t.Parallel()

	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("HandleCreateEndDevice(typed-nil idx) did not panic")
		}
	}()

	s := memory.NewEndDeviceStore()
	var idx *memory.EndDeviceIndex // nil concrete pointer, non-nil interface
	_ = coreedev.HandleCreateEndDevice(s, idx, identityOK(testLFDI, testSFDI), sfdiFirst8)
}

// ----- PUT /edev/{id} -----

func TestHandleUpdateEndDevice(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	s := memory.NewEndDeviceStore()
	enabled := true
	dev := sep2.EndDevice{SFDI: "old-sfdi", LFDI: "old-lfdi", Enabled: &enabled}
	dev.Href = "/edev/1"
	_ = s.Create(ctx, "1", dev)

	mux := http.NewServeMux()
	mux.HandleFunc("PUT /edev/{id}", coreedev.HandleUpdateEndDevice(s))

	disabled := false
	updated := sep2.EndDevice{SFDI: "new-sfdi", LFDI: "new-lfdi", Enabled: &disabled}
	body, _ := xml.Marshal(&updated)

	req := httptest.NewRequest(http.MethodPut, "/edev/1", bytes.NewReader(body))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusNoContent {
		t.Errorf("status = %d, want 204", w.Code)
	}
	got, err := s.Get(context.Background(), "1")
	if err != nil {
		t.Fatalf("post-update Get: %v", err)
	}
	if got.LFDI != "old-lfdi" || got.SFDI != "old-sfdi" {
		t.Errorf("identity after update = LFDI %q SFDI %q, want old-lfdi and old-sfdi: a body never changes identity", got.LFDI, got.SFDI)
	}
	if got.Enabled == nil || *got.Enabled {
		t.Errorf("enabled after update = %v, want false", got.Enabled)
	}
	if byLFDI, err := s.GetByLFDI(ctx, "old-lfdi"); err != nil || byLFDI.Href != "/edev/1" {
		t.Errorf("GetByLFDI(old-lfdi) = href %q, err %v; want /edev/1", byLFDI.Href, err)
	}
	for _, lookup := range []func() (sep2.EndDevice, error){
		func() (sep2.EndDevice, error) { return s.GetByLFDI(ctx, "new-lfdi") },
		func() (sep2.EndDevice, error) { return s.GetBySFDI(ctx, "new-sfdi") },
	} {
		if dev, err := lookup(); err == nil {
			t.Errorf("the index maps the body's identity to %q", dev.Href)
		}
	}
}

func TestHandleUpdateEndDeviceNotFound(t *testing.T) {
	t.Parallel()

	s := memory.NewEndDeviceStore()
	mux := http.NewServeMux()
	mux.HandleFunc("PUT /edev/{id}", coreedev.HandleUpdateEndDevice(s))

	body, _ := xml.Marshal(&sep2.EndDevice{})
	req := httptest.NewRequest(http.MethodPut, "/edev/missing", bytes.NewReader(body))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
}

// ----- DELETE /edev/{id} -----

// TestHandleDeleteEndDevice_HappyPathThenGetReturns404 exercises MAINT-002
// step 5: DELETE returns 204 and a subsequent GET returns 404.
func TestHandleDeleteEndDevice_HappyPathThenGetReturns404(t *testing.T) {
	t.Parallel()

	s := memory.NewEndDeviceStore()
	dev := sep2.EndDevice{SFDI: testSFDI}
	dev.Href = "/edev/abc"
	if err := s.Create(context.Background(), "abc", dev); err != nil {
		t.Fatalf("seed: %v", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("DELETE /edev/{id}", coreedev.HandleDeleteEndDevice(s, nil))
	mux.HandleFunc("GET /edev/{id}", coreedev.HandleEndDevice(s))

	delRec := httptest.NewRecorder()
	mux.ServeHTTP(delRec, httptest.NewRequest(http.MethodDelete, "/edev/abc", nil))
	if delRec.Code != http.StatusNoContent {
		t.Fatalf("DELETE status = %d, want 204; body=%s", delRec.Code, delRec.Body.String())
	}

	getRec := httptest.NewRecorder()
	mux.ServeHTTP(getRec, httptest.NewRequest(http.MethodGet, "/edev/abc", nil))
	if getRec.Code != http.StatusNotFound {
		t.Errorf("GET after DELETE status = %d, want 404", getRec.Code)
	}
}

func TestHandleDeleteEndDevice_NotFound(t *testing.T) {
	t.Parallel()

	s := memory.NewEndDeviceStore()
	mux := http.NewServeMux()
	mux.HandleFunc("DELETE /edev/{id}", coreedev.HandleDeleteEndDevice(s, nil))

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/edev/missing", nil))

	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

// TestHandleDeleteEndDevice_RemovedFromList: deleted device must not appear
// in the EndDeviceList (data-invariants Rule 1: assert field values).
func TestHandleDeleteEndDevice_RemovedFromList(t *testing.T) {
	t.Parallel()

	s := memory.NewEndDeviceStore()
	for _, id := range []string{"a", "b", "c"} {
		dev := sep2.EndDevice{SFDI: "sfdi-" + id}
		dev.Href = "/edev/" + id
		if err := s.Create(context.Background(), id, dev); err != nil {
			t.Fatalf("seed %q: %v", id, err)
		}
	}

	mux := http.NewServeMux()
	mux.HandleFunc("DELETE /edev/{id}", coreedev.HandleDeleteEndDevice(s, nil))
	mux.HandleFunc("GET /edev", corelisthandler.ListHandler[sep2.EndDevice, sep2.EndDeviceList](
		s, coreedev.BuildEndDeviceList, 900,
	))

	delRec := httptest.NewRecorder()
	mux.ServeHTTP(delRec, httptest.NewRequest(http.MethodDelete, "/edev/b", nil))
	if delRec.Code != http.StatusNoContent {
		t.Fatalf("DELETE status = %d", delRec.Code)
	}

	listRec := httptest.NewRecorder()
	mux.ServeHTTP(listRec, httptest.NewRequest(http.MethodGet, "/edev", nil))
	if listRec.Code != http.StatusOK {
		t.Fatalf("list status = %d", listRec.Code)
	}

	var list sep2.EndDeviceList
	if err := xml.Unmarshal(listRec.Body.Bytes(), &list); err != nil {
		t.Fatalf("unmarshal list: %v", err)
	}
	if list.All != 2 {
		t.Errorf("list.All = %d, want 2 after delete", list.All)
	}
	for _, d := range list.EndDevice {
		if d.SFDI == "sfdi-b" {
			t.Errorf("deleted device sfdi-b still present in list (%d entries)", len(list.EndDevice))
		}
	}
}

// TestHandleDeleteEndDevice_TriggersNotification verifies MAINT-002 step 5
// notification fan-out. Field-value asserts on Status and SubscribedResource
// (data-invariants Rule 1: wire-level correctness).
func TestHandleDeleteEndDevice_TriggersNotification(t *testing.T) {
	t.Parallel()

	var got atomic.Pointer[sep2.Notification]
	var received atomic.Int32
	callbackSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var n sep2.Notification
		_ = xml.Unmarshal(body, &n)
		got.Store(&n)
		received.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer callbackSrv.Close()

	edevStore := memory.NewEndDeviceStore()
	if err := edevStore.Create(context.Background(), "1", sep2.EndDevice{SFDI: "123"}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	subStore := memory.NewSubscriptionStore()
	sub := sep2.Subscription{
		SubscribableResource: sep2.SubscribableResource{
			Resource: sep2.Resource{Href: "/edev/1/sub/s1"},
		},
		SubscribedResource: coreedev.EndDeviceListHref, // "/edev"
		NotificationURI:    callbackSrv.URL,
	}
	if err := subStore.Create(context.Background(), "s1", sub); err != nil {
		t.Fatalf("seed sub: %v", err)
	}

	mgr := coresub.NewManager(subStore, 2, 16, coresub.WithDestinationPolicy(coresub.DestinationPolicy{AllowLoopback: true}))
	mgrCtx, mgrCancel := context.WithCancel(context.Background())
	mgrDone := make(chan struct{})
	go func() {
		mgr.Start(mgrCtx)
		close(mgrDone)
	}()
	defer func() {
		mgrCancel()
		<-mgrDone
	}()

	mux := http.NewServeMux()
	mux.HandleFunc("DELETE /edev/{id}", coreedev.HandleDeleteEndDevice(edevStore, mgr))

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/edev/1", nil))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("DELETE status = %d, want 204", rec.Code)
	}

	deadline := time.After(2 * time.Second)
	for received.Load() == 0 {
		select {
		case <-deadline:
			t.Fatal("timed out waiting for notification POST")
		default:
			time.Sleep(10 * time.Millisecond)
		}
	}

	n := got.Load()
	if n == nil {
		t.Fatal("got nil notification")
	}
	if n.Status != sep2.NotificationStatusRemoved {
		t.Errorf("notification Status = %d, want NotificationStatusRemoved (%d)",
			n.Status, sep2.NotificationStatusRemoved)
	}
	if n.SubscribedResource != coreedev.EndDeviceListHref {
		t.Errorf("SubscribedResource = %q, want %q",
			n.SubscribedResource, coreedev.EndDeviceListHref)
	}
}

// TestHandleDeleteEndDevice_NoNotificationOnNotFound: a 404 DELETE must NOT
// fan out a spurious notification.
func TestHandleDeleteEndDevice_NoNotificationOnNotFound(t *testing.T) {
	t.Parallel()

	// Count only requests to this test's random path: other traffic reaching
	// the loopback port must not fail the zero-notification check.
	notifyPath := "/notify/" + rand.Text()
	var received atomic.Int32
	callbackSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == notifyPath {
			received.Add(1)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer callbackSrv.Close()

	edevStore := memory.NewEndDeviceStore()
	subStore := memory.NewSubscriptionStore()
	sub := sep2.Subscription{
		SubscribableResource: sep2.SubscribableResource{
			Resource: sep2.Resource{Href: "/edev/1/sub/s1"},
		},
		SubscribedResource: coreedev.EndDeviceListHref,
		NotificationURI:    callbackSrv.URL + notifyPath,
	}
	if err := subStore.Create(context.Background(), "s1", sub); err != nil {
		t.Fatalf("seed sub: %v", err)
	}

	mgr := coresub.NewManager(subStore, 1, 8, coresub.WithDestinationPolicy(coresub.DestinationPolicy{AllowLoopback: true}))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { mgr.Start(ctx); close(done) }()
	defer func() { cancel(); <-done }()

	mux := http.NewServeMux()
	mux.HandleFunc("DELETE /edev/{id}", coreedev.HandleDeleteEndDevice(edevStore, mgr))

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/edev/ghost", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("DELETE status = %d, want 404", rec.Code)
	}

	time.Sleep(50 * time.Millisecond)
	if got := received.Load(); got != 0 {
		t.Errorf("received %d notification(s); want 0 on 404-DELETE", got)
	}
}

// TestHandleDeleteEndDevice_StoreError: non-ErrNotFound store error returns 500.
func TestHandleDeleteEndDevice_StoreError(t *testing.T) {
	t.Parallel()

	var fired atomic.Int32
	n := notifierFunc(func(_ context.Context, _ string, _ uint8) {
		fired.Add(1)
	})

	mux := http.NewServeMux()
	mux.HandleFunc("DELETE /edev/{id}", coreedev.HandleDeleteEndDevice(deleteFailEndDeviceStore{}, n))

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/edev/1", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.Code)
	}
	if got := fired.Load(); got != 0 {
		t.Errorf("notifier fired %d time(s); want 0 on store error", got)
	}
}

// TestHandleDeleteEndDevice_EmptyID: defensive guard on empty {id} path value.
func TestHandleDeleteEndDevice_EmptyID(t *testing.T) {
	t.Parallel()

	s := memory.NewEndDeviceStore()
	h := coreedev.HandleDeleteEndDevice(s, nil)

	req := httptest.NewRequest(http.MethodDelete, "/edev/", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req) // no mux: PathValue("id") is "".
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

// TestHandleDeleteEndDevice_WrongMethod: method guard returns 405.
func TestHandleDeleteEndDevice_WrongMethod(t *testing.T) {
	t.Parallel()

	s := memory.NewEndDeviceStore()
	h := coreedev.HandleDeleteEndDevice(s, nil)

	req := httptest.NewRequest(http.MethodGet, "/edev/1", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", rec.Code)
	}
}

// TestHandleUpdateEndDeviceMethodNotAllowed: non-PUT returns 405.
func TestHandleUpdateEndDeviceMethodNotAllowed(t *testing.T) {
	t.Parallel()
	s := memory.NewEndDeviceStore()
	h := coreedev.HandleUpdateEndDevice(s)
	req := httptest.NewRequest(http.MethodGet, "/edev/1", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", w.Code)
	}
}

// TestHandleUpdateEndDeviceEmptyID: defensive 400 when path value is empty.
func TestHandleUpdateEndDeviceEmptyID(t *testing.T) {
	t.Parallel()
	s := memory.NewEndDeviceStore()
	h := coreedev.HandleUpdateEndDevice(s)
	body, _ := xml.Marshal(&sep2.EndDevice{})
	req := httptest.NewRequest(http.MethodPut, "/edev/", bytes.NewReader(body))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

// TestHandleUpdateEndDeviceInvalidXML: malformed body returns 400.
func TestHandleUpdateEndDeviceInvalidXML(t *testing.T) {
	t.Parallel()
	s := memory.NewEndDeviceStore()
	mux := http.NewServeMux()
	mux.HandleFunc("PUT /edev/{id}", coreedev.HandleUpdateEndDevice(s))
	req := httptest.NewRequest(http.MethodPut, "/edev/1", bytes.NewBufferString("<bad"))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

// ----- BuildEndDeviceList -----

func TestBuildEndDeviceList(t *testing.T) {
	t.Parallel()

	result := store.ListResult[sep2.EndDevice]{All: 5, Results: 2, Items: []sep2.EndDevice{{}, {}}}
	list := coreedev.BuildEndDeviceList("/edev", result, 900)
	if list.All != 5 {
		t.Errorf("All = %d, want 5", list.All)
	}
	if list.Results != 2 {
		t.Errorf("Results = %d, want 2", list.Results)
	}
	if len(list.EndDevice) != 2 {
		t.Errorf("len(EndDevice) = %d, want 2", len(list.EndDevice))
	}
}

// ----- test doubles -----

var errBoom = errors.New("synthetic store failure")

// deleteFailEndDeviceStore satisfies store.EndDeviceStore: Delete returns a
// non-ErrNotFound error, exercising the handler's 500 branch.
type deleteFailEndDeviceStore struct{}

func (deleteFailEndDeviceStore) Get(context.Context, string) (sep2.EndDevice, error) {
	return sep2.EndDevice{}, errBoom
}
func (deleteFailEndDeviceStore) List(context.Context, store.ListOptions) (store.ListResult[sep2.EndDevice], error) {
	return store.ListResult[sep2.EndDevice]{}, nil
}
func (deleteFailEndDeviceStore) Create(context.Context, string, sep2.EndDevice) error {
	return errBoom
}
func (deleteFailEndDeviceStore) Update(context.Context, string, sep2.EndDevice) error {
	return errBoom
}
func (deleteFailEndDeviceStore) Delete(context.Context, string) error { return errBoom }
func (deleteFailEndDeviceStore) Count(context.Context) (uint32, error) {
	return 0, nil
}
func (deleteFailEndDeviceStore) GetBySFDI(context.Context, string) (sep2.EndDevice, error) {
	return sep2.EndDevice{}, errBoom
}
func (deleteFailEndDeviceStore) GetByLFDI(context.Context, string) (sep2.EndDevice, error) {
	return sep2.EndDevice{}, errBoom
}

// notifierFunc adapts a plain function to the ResourceNotifier interface.
type notifierFunc func(ctx context.Context, resourceHref string, status uint8)

func (f notifierFunc) Notify(ctx context.Context, resourceHref string, status uint8) {
	f(ctx, resourceHref, status)
}

// ----- POST /edev when the identity lookup cannot complete -----

// lookupFailingEndDevices is an EndDeviceStore whose identity lookups fail
// while every other operation succeeds.
//
// The asymmetry is the whole point. A store that failed everything would make
// the create path fail at its next call for reasons of its own, and the status
// code would look right while the branch under test was never established. This
// models the case that actually matters on a durable backend: a read that times
// out against a connection pool while the write that follows it succeeds.
type lookupFailingEndDevices struct {
	store.EndDeviceStore
	err error
}

func (s *lookupFailingEndDevices) GetBySFDI(_ context.Context, _ string) (sep2.EndDevice, error) {
	return sep2.EndDevice{}, s.err
}

func (s *lookupFailingEndDevices) GetByLFDI(_ context.Context, _ string) (sep2.EndDevice, error) {
	return sep2.EndDevice{}, s.err
}

// TestCreateEndDeviceRefusesWhenTheIdentityLookupFails pins that a failed
// "is this device already registered" lookup is refused rather than read as
// "no".
//
// The status code is the smaller half of this. The consequential half is the
// COUNT: treating an unanswered lookup as absence sends the handler down the
// provisioning branch, so a device that is already registered is registered a
// second time, under a second URL index allocated to the same LFDI. On a
// durable backend that is duplicate fleet state written during a blip, and
// nothing downstream can tell the duplicate from a genuine second device. The
// index allocation compounds it: the device is re-addressed, so a client
// holding /edev/3 finds its resources under a path it was never told about.
//
// A 500 asks the client to retry, which costs one request. The write costs a
// corrupted fleet that no later read can detect.
func TestCreateEndDeviceRefusesWhenTheIdentityLookupFails(t *testing.T) {
	t.Parallel()

	backend := memory.NewEndDeviceStore()
	errBackendDown := errors.New("backend unavailable")
	s := &lookupFailingEndDevices{EndDeviceStore: backend, err: errBackendDown}

	h := coreedev.HandleCreateEndDevice(s, memory.NewEndDeviceIndex(), identityOK(testLFDI, testSFDI), sfdiFirst8)

	req := httptest.NewRequest(http.MethodPost, "/edev", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500: a lookup that did not complete is not evidence the device is unregistered", w.Code)
	}

	// Nothing may have been written. This is the assertion that distinguishes
	// "reported the right status" from "did not provision a duplicate".
	count, err := backend.Count(context.Background())
	if err != nil {
		t.Fatalf("count devices: %v", err)
	}
	if count != 0 {
		t.Errorf("the store holds %d device(s) after a refused create; want 0, because the handler provisioned one on a lookup it could not complete", count)
	}
}

// TestCreateEndDeviceStillReturnsTheExistingDeviceOnACleanMiss is the other
// side of the same branch: an ordinary ErrNotFound still means "not
// registered", so the provisioning path is unchanged for every case that is
// not a backend failure.
func TestCreateEndDeviceStillReturnsTheExistingDeviceOnACleanMiss(t *testing.T) {
	t.Parallel()

	s := memory.NewEndDeviceStore()
	h := coreedev.HandleCreateEndDevice(s, memory.NewEndDeviceIndex(), identityOK(testLFDI, testSFDI), sfdiFirst8)

	first := httptest.NewRecorder()
	h.ServeHTTP(first, httptest.NewRequest(http.MethodPost, "/edev", nil))
	if first.Code != http.StatusCreated {
		t.Fatalf("first POST status = %d, want 201; body=%s", first.Code, first.Body.String())
	}
	firstLocation := first.Header().Get("Location")

	second := httptest.NewRecorder()
	h.ServeHTTP(second, httptest.NewRequest(http.MethodPost, "/edev", nil))
	if second.Code != http.StatusOK {
		t.Fatalf("second POST status = %d, want 200 for an already-registered device", second.Code)
	}
	if got := second.Header().Get("Location"); got != firstLocation {
		t.Errorf("second POST Location = %q, want the first device's %q", got, firstLocation)
	}

	count, err := s.Count(context.Background())
	if err != nil {
		t.Fatalf("count devices: %v", err)
	}
	if count != 1 {
		t.Errorf("the store holds %d device(s) after two POSTs for one identity, want 1", count)
	}
}
