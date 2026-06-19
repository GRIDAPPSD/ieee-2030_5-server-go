package handler_test

import (
	"context"
	"encoding/xml"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-go/internal/handler"
	"github.com/GRIDAPPSD/ieee-2030_5-go/internal/subscription"
	"gitlab.pnnl.gov/arista/ieee-2030_5/ieee-2030_5-core/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-go/pkg/store"
	"github.com/GRIDAPPSD/ieee-2030_5-go/pkg/store/memory"
)

// errBoom is a sentinel returned by deleteFailEndDeviceStore.Delete so the
// handler's 500 branch can be exercised.
var errBoom = errors.New("synthetic store failure")

// deleteFailEndDeviceStore satisfies store.EndDeviceStore. Every method
// returns either a zero value or errBoom; only Delete is interesting — it
// reports a non-ErrNotFound error so the handler's 500 branch fires.
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

func (deleteFailEndDeviceStore) Delete(context.Context, string) error {
	return errBoom
}

func (deleteFailEndDeviceStore) Count(context.Context) (uint32, error) { return 0, nil }

func (deleteFailEndDeviceStore) GetBySFDI(context.Context, string) (sep2.EndDevice, error) {
	return sep2.EndDevice{}, errBoom
}

func (deleteFailEndDeviceStore) GetByLFDI(context.Context, string) (sep2.EndDevice, error) {
	return sep2.EndDevice{}, errBoom
}

// IEEE-023 / CSIP V1.2 MAINT-002 — HTTP DELETE on /edev/{id}.
//
// Spec procedure (V1.2 §11, page 192-193):
//   step 4: [C] HTTP DELETE on the EDA1X EndDevice instance href
//   step 5: [S] Receive and process the HTTP DELETE; fire Notification on the
//           EndDeviceList subscription; return 404 on subsequent GET.

// TestHandleDeleteEndDevice_HappyPathThenGetReturns404 exercises step 5: a
// DELETE on an existing href returns 204, and a subsequent GET against the
// same href returns 404.
func TestHandleDeleteEndDevice_HappyPathThenGetReturns404(t *testing.T) {
	t.Parallel()

	s := memory.NewEndDeviceStore()
	dev := sep2.EndDevice{SFDI: "123456789012"}
	dev.Href = "/edev/abc"
	if err := s.Create(context.Background(), "abc", dev); err != nil {
		t.Fatalf("seed: %v", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("DELETE /edev/{id}", handler.HandleDeleteEndDevice(s, nil))
	mux.HandleFunc("GET /edev/{id}", handler.HandleEndDevice(s))

	delReq := httptest.NewRequest(http.MethodDelete, "/edev/abc", nil)
	delRec := httptest.NewRecorder()
	mux.ServeHTTP(delRec, delReq)
	if delRec.Code != http.StatusNoContent {
		t.Fatalf("DELETE status = %d, want 204; body=%s", delRec.Code, delRec.Body.String())
	}

	getReq := httptest.NewRequest(http.MethodGet, "/edev/abc", nil)
	getRec := httptest.NewRecorder()
	mux.ServeHTTP(getRec, getReq)
	if getRec.Code != http.StatusNotFound {
		t.Errorf("GET after DELETE status = %d, want 404", getRec.Code)
	}
}

// TestHandleDeleteEndDevice_NotFound exercises the 404 path when the id was
// never registered.
func TestHandleDeleteEndDevice_NotFound(t *testing.T) {
	t.Parallel()

	s := memory.NewEndDeviceStore()
	mux := http.NewServeMux()
	mux.HandleFunc("DELETE /edev/{id}", handler.HandleDeleteEndDevice(s, nil))

	req := httptest.NewRequest(http.MethodDelete, "/edev/missing", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

// TestHandleDeleteEndDevice_RemovedFromList verifies the deleted EndDevice
// no longer appears in GET /edev list output.
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
	mux.HandleFunc("DELETE /edev/{id}", handler.HandleDeleteEndDevice(s, nil))
	mux.HandleFunc("GET /edev", handler.ListHandler[sep2.EndDevice, sep2.EndDeviceList](
		s, handler.BuildEndDeviceList, 900,
	))

	// Delete "b".
	delReq := httptest.NewRequest(http.MethodDelete, "/edev/b", nil)
	delRec := httptest.NewRecorder()
	mux.ServeHTTP(delRec, delReq)
	if delRec.Code != http.StatusNoContent {
		t.Fatalf("DELETE status = %d", delRec.Code)
	}

	// GET /edev — list must omit "b".
	listReq := httptest.NewRequest(http.MethodGet, "/edev", nil)
	listRec := httptest.NewRecorder()
	mux.ServeHTTP(listRec, listReq)
	if listRec.Code != http.StatusOK {
		t.Fatalf("list status = %d", listRec.Code)
	}

	var list sep2.EndDeviceList
	if err := xml.Unmarshal(listRec.Body.Bytes(), &list); err != nil {
		t.Fatalf("unmarshal list: %v", err)
	}

	for _, d := range list.EndDevice {
		if d.SFDI == "sfdi-b" {
			t.Errorf("deleted device sfdi-b still present in list (%d entries)", len(list.EndDevice))
		}
	}
	if list.All != 2 {
		t.Errorf("list.All = %d, want 2", list.All)
	}
}

// TestHandleDeleteEndDevice_TriggersNotification exercises MAINT-002 step 5
// notification fan-out: a DELETE against /edev/{id} causes the Manager to
// POST a Notification XML payload to the subscriber's NotificationURI with
// Status = NotificationStatusRemoved.
func TestHandleDeleteEndDevice_TriggersNotification(t *testing.T) {
	t.Parallel()

	// Stub subscriber callback.
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

	// Stores: EndDevice + subscription.
	edevStore := memory.NewEndDeviceStore()
	if err := edevStore.Create(context.Background(), "1", sep2.EndDevice{SFDI: "123"}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	subStore := memory.NewSubscriptionStore()
	sub := sep2.Subscription{
		SubscribableResource: sep2.SubscribableResource{
			Resource: sep2.Resource{Href: "/edev/1/sub/s1"},
		},
		SubscribedResource: handler.EndDeviceListHref, // "/edev"
		NotificationURI:    callbackSrv.URL,
	}
	if err := subStore.Create(context.Background(), "s1", sub); err != nil {
		t.Fatalf("seed sub: %v", err)
	}

	// Notification dispatcher.
	mgr := subscription.NewManager(subStore, 2, 16)
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

	// Hit DELETE.
	mux := http.NewServeMux()
	mux.HandleFunc("DELETE /edev/{id}", handler.HandleDeleteEndDevice(edevStore, mgr))

	req := httptest.NewRequest(http.MethodDelete, "/edev/1", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("DELETE status = %d", rec.Code)
	}

	// Wait for the notification POST to land on the stub.
	deadline := time.After(2 * time.Second)
	for received.Load() == 0 {
		select {
		case <-deadline:
			t.Fatalf("timed out waiting for notification POST")
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
	if n.SubscribedResource != handler.EndDeviceListHref {
		t.Errorf("notification SubscribedResource = %q, want %q",
			n.SubscribedResource, handler.EndDeviceListHref)
	}
}

// TestHandleDeleteEndDevice_NoNotificationOnNotFound verifies that a 404
// DELETE does not fan out a spurious "removed" notification. The Manager
// must only fire on a successful delete (MAINT-002 step 5 — server "deletes
// the EndDevice instance" is the precondition).
func TestHandleDeleteEndDevice_NoNotificationOnNotFound(t *testing.T) {
	t.Parallel()

	var received atomic.Int32
	callbackSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer callbackSrv.Close()

	edevStore := memory.NewEndDeviceStore()

	subStore := memory.NewSubscriptionStore()
	sub := sep2.Subscription{
		SubscribableResource: sep2.SubscribableResource{
			Resource: sep2.Resource{Href: "/edev/1/sub/s1"},
		},
		SubscribedResource: handler.EndDeviceListHref,
		NotificationURI:    callbackSrv.URL,
	}
	if err := subStore.Create(context.Background(), "s1", sub); err != nil {
		t.Fatalf("seed sub: %v", err)
	}

	mgr := subscription.NewManager(subStore, 1, 8)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { mgr.Start(ctx); close(done) }()
	defer func() { cancel(); <-done }()

	mux := http.NewServeMux()
	mux.HandleFunc("DELETE /edev/{id}", handler.HandleDeleteEndDevice(edevStore, mgr))

	req := httptest.NewRequest(http.MethodDelete, "/edev/ghost", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("DELETE status = %d, want 404", rec.Code)
	}

	// Give the dispatcher a moment; any callback hit is a bug.
	time.Sleep(50 * time.Millisecond)
	if got := received.Load(); got != 0 {
		t.Errorf("received %d notification(s); want 0 on 404-DELETE", got)
	}
}

// TestHandleDeleteEndDevice_StoreError exercises the 500 branch: when the
// underlying store returns a non-ErrNotFound error, the handler logs and
// returns 500. No notification is fired.
func TestHandleDeleteEndDevice_StoreError(t *testing.T) {
	t.Parallel()

	var fired atomic.Int32
	n := notifierFunc(func(_ context.Context, _ string, _ uint8) {
		fired.Add(1)
	})

	h := handler.HandleDeleteEndDevice(deleteFailEndDeviceStore{}, n)
	mux := http.NewServeMux()
	mux.HandleFunc("DELETE /edev/{id}", h)

	req := httptest.NewRequest(http.MethodDelete, "/edev/1", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.Code)
	}
	if got := fired.Load(); got != 0 {
		t.Errorf("notifier fired %d time(s); want 0 on store error", got)
	}
}

// TestHandleDeleteEndDevice_EmptyID exercises the bad-request branch when
// the {id} path value is empty. In practice the router pattern
// `DELETE /edev/{id}` never matches /edev (no trailing segment), so this
// branch is defensive. We exercise it by invoking the handler without a
// matching ServeMux pattern.
func TestHandleDeleteEndDevice_EmptyID(t *testing.T) {
	t.Parallel()

	s := memory.NewEndDeviceStore()
	h := handler.HandleDeleteEndDevice(s, nil)

	req := httptest.NewRequest(http.MethodDelete, "/edev/", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req) // no mux — PathValue("id") is "".
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

// TestHandleDeleteEndDevice_WrongMethod verifies method-guard: anything
// other than DELETE returns 405 (so the route doesn't accidentally accept
// GET/POST/etc. through the same handler).
func TestHandleDeleteEndDevice_WrongMethod(t *testing.T) {
	t.Parallel()

	s := memory.NewEndDeviceStore()
	// Route only DELETE; everything else falls through to the mux's 405 via
	// the handler's own method check (defense in depth).
	h := handler.HandleDeleteEndDevice(s, nil)

	req := httptest.NewRequest(http.MethodGet, "/edev/1", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", rec.Code)
	}
}

// notifierFunc is a function-adapter that satisfies handler.ResourceNotifier
// for tests that need to observe (or count) Notify calls without standing up
// a real *subscription.Manager.
type notifierFunc func(ctx context.Context, resourceHref string, status uint8)

func (f notifierFunc) Notify(ctx context.Context, resourceHref string, status uint8) {
	f(ctx, resourceHref, status)
}
