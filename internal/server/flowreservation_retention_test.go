package server_test

// GRIDAPPSD/ieee-2030_5-server-go#672: flow reservation retention as the
// admin and protocol routes serve its result, and as Run starts and stops it.

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/auth"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/dercontrol"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/flowreservation"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/server"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/assembly"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

type frRetentionEnv struct {
	device *httptest.Server
	admin  http.Handler
	stores *server.Stores
}

// newFRRetentionEnv wires the stores the way Run does for the routes this
// test reads and writes: one queue and one DER control issuer, so the admin
// cancel route is mounted.
func newFRRetentionEnv(t *testing.T) *frRetentionEnv {
	t.Helper()
	ctx := context.Background()
	stores := newTestStores()
	stores.FlowReservationAnswers = memory.NewScopedStore[flowreservation.AnswerRecord]()
	if err := stores.EndDevices.Create(ctx, "dev", sep2.EndDevice{LFDI: frAgreeLFDI}); err != nil {
		t.Fatal(err)
	}
	pen := uint32(0x40732001)
	issuer, err := assembly.NewDERControlIssuer(stores.DERPrograms, stores.DERControls, stores.DERControlLifecycles, &pen)
	if err != nil {
		t.Fatal(err)
	}
	stores.DERControlIssuer = issuer
	stores.FlowReservationQueue = assembly.NewFlowReservationQueue(server.NewCoreStores(stores), &pen, 0, nil)
	t.Cleanup(stores.FlowReservationQueue.Close)

	policy := assembly.AuthPolicy{
		Wrap:       func(h http.Handler) http.Handler { return h },
		Identity:   func(context.Context) (string, string, bool) { return frAgreeLFDI, "AABBCCDD11223344", true },
		SFDIPrefix: func(sfdi string) (string, error) { return sfdi[:8], nil },
	}
	protocol, _ := assembly.BuildProtocolRouter(assembly.RouterConfig{FlowReservationDeadline: time.Hour},
		server.NewCoreStores(stores), policy, "serverSFDI", "serverLFDI", nil)
	device := httptest.NewServer(protocol)
	t.Cleanup(device.Close)
	admin, _ := server.BuildAdminRouter(
		"the-key", newScopeTestCertService(t), stores, "GCM",
		auth.NewTicketStore(30*time.Second), auth.NewSessionStore(30*time.Minute, 8*time.Hour),
		server.DefaultAdminAllowedHosts(), false, nil,
	)
	return &frRetentionEnv{device: device, admin: admin, stores: stores}
}

type frSeed struct {
	start, created int64
	dur            uint32
	cancelledAt    *int64
}

// seed stores request id and one response per member, oldest first, each
// with an answer record, and a cancel mark for each cancelled member.
func (e *frRetentionEnv) seed(t *testing.T, id string, members ...frSeed) {
	t.Helper()
	ctx := context.Background()
	frq := sep2.FlowReservationRequest{
		Resource: sep2.Resource{Href: "/edev/dev/frq/" + id}, MRID: "M-" + id, CreationTime: members[0].created,
		IntervalRequested: &sep2.DateTimeInterval{Start: members[0].start, Duration: members[0].dur},
		EnergyRequested:   &sep2.SignedRealEnergy{Value: 1000},
	}
	if err := e.stores.FlowReservationRequests.Create(ctx, "dev", id, frq); err != nil {
		t.Fatal(err)
	}
	rid := id
	for i, m := range members {
		if i > 0 {
			rid = flowreservation.RevisionID(rid)
		}
		frp := sep2.FlowReservationResponse{
			Event: sep2.Event{
				SubscribableResource: sep2.SubscribableResource{Resource: sep2.Resource{Href: "/edev/dev/frp/" + rid}},
				MRID:                 "R-" + rid, CreationTime: m.created, Interval: &sep2.DateTimeInterval{Start: m.start, Duration: m.dur},
			},
			EnergyAvailable: &sep2.SignedRealEnergy{Value: 1000}, PowerAvailable: &sep2.ActivePower{Value: 500}, Subject: "M-" + id,
		}
		if err := e.stores.FlowReservationResponses.Create(ctx, "dev", rid, frp); err != nil {
			t.Fatal(err)
		}
		rec := flowreservation.AnswerRecord{Action: flowreservation.ActionAnswer, By: flowreservation.Attribution{Kind: flowreservation.KindOperator, At: m.created}}
		if err := e.stores.FlowReservationAnswers.Create(ctx, "dev", rid, rec); err != nil {
			t.Fatal(err)
		}
		if m.cancelledAt != nil {
			if err := e.stores.FlowReservationResponseLifecycles.Create(ctx, "dev", rid, dercontrol.LifecycleRecord{CancelledAt: m.cancelledAt}); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func (e *frRetentionEnv) sweep(t *testing.T, now time.Time) int {
	t.Helper()
	n, err := server.NewFlowReservationRetention(e.stores, nil, slog.Default()).Sweep(context.Background(), now)
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	return n
}

func (e *frRetentionEnv) adminDo(t *testing.T, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	ct := ""
	if body != "" {
		ct = "application/json"
	}
	e.admin.ServeHTTP(w, bearerFromLoopbackRequest(method, path, ct, body))
	return w
}

// adminList returns the frqIds the admin list serves, each with its response
// count.
func (e *frRetentionEnv) adminList(t *testing.T) map[string]int {
	t.Helper()
	w := e.adminDo(t, http.MethodGet, "/api/derms/flow-reservations?aggregatorLFDI="+frAgreeLFDI, "")
	if w.Code != http.StatusOK {
		t.Fatalf("admin list = %d %s", w.Code, w.Body.String())
	}
	var list struct {
		Requests []struct {
			FrqID     string            `json:"frqId"`
			Responses []json.RawMessage `json:"responses"`
		} `json:"requests"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil {
		t.Fatalf("decode admin list: %v\n%s", err, w.Body.String())
	}
	out := map[string]int{}
	for _, r := range list.Requests {
		out[r.FrqID] = len(r.Responses)
	}
	return out
}

func (e *frRetentionEnv) protocolGet(t *testing.T, path string) (int, []byte) {
	t.Helper()
	resp, err := http.Get(e.device.URL + path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, body
}

const frGraceSeconds = int64(flowreservation.DefaultRetentionGrace / time.Second)

// #672 criterion 1: an expired request is gone from both lists and is a 404
// at its admin and protocol paths, never a 500.
func TestRetention_ExpiredRequestIsGoneFromTheListsAnd404AtItsPath(t *testing.T) {
	e := newFRRetentionEnv(t)
	now := time.Now().Unix()
	ended := now - frGraceSeconds - 1
	e.seed(t, "frq-expired", frSeed{start: ended - 3600, dur: 3600, created: ended - 7200})
	e.seed(t, "frq-active", frSeed{start: now - 100, dur: 3600, created: now - 200})

	if n := e.sweep(t, time.Unix(now, 0)); n != 1 {
		t.Fatalf("removed = %d, want 1", n)
	}

	if got := e.adminList(t); len(got) != 1 || got["frq-active"] != 1 {
		t.Errorf("admin list = %v, want frq-active alone, with its one response", got)
	}
	w := e.adminDo(t, http.MethodGet, "/api/derms/flow-reservations/dev/frq-expired", "")
	if w.Code != http.StatusNotFound || !strings.Contains(w.Body.String(), "request_not_found") {
		t.Errorf("admin GET of the removed request = %d %s, want 404 request_not_found", w.Code, w.Body.String())
	}
	for _, path := range []string{"/edev/dev/frq/frq-expired", "/edev/dev/frp/frq-expired"} {
		if code, body := e.protocolGet(t, path); code != http.StatusNotFound {
			t.Errorf("protocol GET %s = %d %s, want 404", path, code, body)
		}
	}
	code, body := e.protocolGet(t, "/edev/dev/frq")
	if code != http.StatusOK {
		t.Fatalf("protocol GET /edev/dev/frq = %d %s", code, body)
	}
	var list sep2.FlowReservationRequestList
	if err := xml.Unmarshal(body, &list); err != nil {
		t.Fatalf("decode request list: %v\n%s", err, body)
	}
	var hrefs []string
	for _, r := range list.FlowReservationRequest {
		hrefs = append(hrefs, r.Href)
	}
	if !slices.Equal(hrefs, []string{"/edev/dev/frq/frq-active"}) {
		t.Errorf("protocol request list hrefs = %v, want only /edev/dev/frq/frq-active", hrefs)
	}
}

// #672 criterion 2: a request whose grant is still active survives the sweep
// and the operator can still cancel it through the admin route.
func TestRetention_ActiveRequestIsRetainedAndCancellableThroughTheAdminRoute(t *testing.T) {
	e := newFRRetentionEnv(t)
	now := time.Now().Unix()
	// Created more than a grace ago, so only the window's end can keep it.
	e.seed(t, "frq-active", frSeed{start: now - 100, dur: 3600, created: now - 2*frGraceSeconds})

	if n := e.sweep(t, time.Unix(now, 0)); n != 0 {
		t.Fatalf("removed = %d, want 0", n)
	}
	w := e.adminDo(t, http.MethodPost, "/api/derms/flow-reservations/dev/frq-active/cancel", `{"reason":"retention test"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("admin cancel after the sweep = %d %s, want 200", w.Code, w.Body.String())
	}
	lc, err := e.stores.FlowReservationResponseLifecycles.Get(context.Background(), "dev", "frq-active")
	if err != nil || lc.CancelledAt == nil {
		t.Fatalf("cancel mark after the admin cancel = %+v, %v; want CancelledAt set", lc, err)
	}
	code, body := e.protocolGet(t, "/edev/dev/frp/frq-active")
	var frp sep2.FlowReservationResponse
	if code != http.StatusOK || xml.Unmarshal(body, &frp) != nil || frp.EventStatus == nil ||
		frp.EventStatus.CurrentStatus != sep2.EventStatusCancelled {
		t.Errorf("protocol GET after the cancel = %d %s, want 200 with EventStatus Cancelled", code, body)
	}
}

// #672 criterion 3: a cancelled, revised-away response long past its end does
// not remove its request while the revision is active.
func TestRetention_RevisedAwayResponseKeepsItsRequestWhileTheRevisionIsActive(t *testing.T) {
	e := newFRRetentionEnv(t)
	now := time.Now().Unix()
	long := now - 10*frGraceSeconds
	e.seed(t, "frq-revised",
		frSeed{start: long - 3600, dur: 600, created: long - 60, cancelledAt: &long},
		frSeed{start: now - 60, dur: 3600, created: long + 1},
	)

	if n := e.sweep(t, time.Unix(now, 0)); n != 0 {
		t.Fatalf("removed = %d, want 0", n)
	}
	if got := e.adminList(t); got["frq-revised"] != 2 {
		t.Errorf("admin list = %v, want frq-revised with both responses", got)
	}
	for _, path := range []string{"/edev/dev/frq/frq-revised", "/edev/dev/frp/frq-revised", "/edev/dev/frp/frq-revised-r1"} {
		if code, body := e.protocolGet(t, path); code != http.StatusOK {
			t.Errorf("protocol GET %s = %d %s, want 200", path, code, body)
		}
	}
}

// Run sweeps once at boot, after recovery and before serving, and stops the
// sweep before it closes the queue.
func TestRun_RetentionSweepsAtBootAfterRecoveryAndStopsBeforeTheQueueCloses(t *testing.T) {
	e := newFRRunEnv(t)
	ctx := context.Background()
	reqs, err := memory.NewPersistentScopedStore[sep2.FlowReservationRequest](
		filepath.Join(e.dataDir, "flowreservation-requests.json"), "FlowReservationRequest")
	if err != nil {
		t.Fatal(err)
	}
	resps, err := memory.NewPersistentScopedStore[sep2.FlowReservationResponse](
		filepath.Join(e.dataDir, "flowreservation-responses.json"), "FlowReservationResponse")
	if err != nil {
		t.Fatal(err)
	}
	ended := time.Now().Unix() - frGraceSeconds - 60
	frq := sep2.FlowReservationRequest{MRID: "OLD", CreationTime: ended - 600, IntervalRequested: &sep2.DateTimeInterval{Start: ended - 600, Duration: 600}}
	frq.Href = "/edev/0/frq/frq-old"
	if err := reqs.Create(ctx, "0", "frq-old", frq); err != nil {
		t.Fatal(err)
	}
	frp := sep2.FlowReservationResponse{Subject: "OLD"}
	frp.Href, frp.MRID, frp.CreationTime = "/edev/0/frp/frq-old", "R-OLD", ended-600
	frp.Interval = &sep2.DateTimeInterval{Start: ended - 600, Duration: 600}
	if err := resps.Create(ctx, "0", "frq-old", frp); err != nil {
		t.Fatal(err)
	}

	var (
		mu     sync.Mutex
		events []string
	)
	record := func(s string) {
		mu.Lock()
		defer mu.Unlock()
		events = append(events, s)
	}
	server.SetRecoverAtBoot(t, func(ctx context.Context, s *server.Stores, q *flowreservation.Queue, n flowreservation.Notifier, l *slog.Logger, now time.Time) (flowreservation.RecoverCounts, error) {
		record("recover")
		return server.RecoverFlowReservations(ctx, s, q, n, l, now)
	})
	server.SetStartRetentionAtBoot(t, func(ctx context.Context, s *server.Stores, n flowreservation.Notifier, l *slog.Logger, now func() time.Time) func() {
		record("retention start")
		stop := server.StartFlowReservationRetention(ctx, s, n, l, now)
		return func() {
			stop()
			// A queue still open re-arms; a closed one refuses. The probe
			// request does not exist, so its timer, stopped by Close, writes
			// nothing.
			probe := sep2.FlowReservationRequest{CreationTime: time.Now().Unix()}
			if s.FlowReservationQueue.Rearm("0", "frq-probe", probe, time.Now()) {
				record("retention stopped, queue open")
			} else {
				record("retention stopped, queue closed")
			}
		}
	})

	stop := e.start(t, e.config(0))
	if err := stop(); err != nil {
		t.Fatalf("run: %v", err)
	}

	want := []string{"recover", "retention start", "retention stopped, queue open"}
	mu.Lock()
	got := slices.Clone(events)
	mu.Unlock()
	if !slices.Equal(got, want) {
		t.Errorf("boot and shutdown order = %q, want %q", got, want)
	}
	reqs, err = memory.NewPersistentScopedStore[sep2.FlowReservationRequest](
		filepath.Join(e.dataDir, "flowreservation-requests.json"), "FlowReservationRequest")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reqs.Get(ctx, "0", "frq-old"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("stored request after the boot sweep: err = %v, want not found", err)
	}
	resps, err = memory.NewPersistentScopedStore[sep2.FlowReservationResponse](
		filepath.Join(e.dataDir, "flowreservation-responses.json"), "FlowReservationResponse")
	if err != nil {
		t.Fatal(err)
	}
	if n, err := resps.Count(ctx, "0"); err != nil || n != 0 {
		t.Errorf("stored responses after the boot sweep = %d (%v), want 0", n, err)
	}
}

// seedEndedRequest stores, in e's data directory, a request on device 0 and
// its response, whose window ended endedAgo before now.
func seedEndedRequest(t *testing.T, e *frRunEnv, endedAgo time.Duration) {
	t.Helper()
	ctx := context.Background()
	reqs, err := memory.NewPersistentScopedStore[sep2.FlowReservationRequest](
		filepath.Join(e.dataDir, "flowreservation-requests.json"), "FlowReservationRequest")
	if err != nil {
		t.Fatal(err)
	}
	resps, err := memory.NewPersistentScopedStore[sep2.FlowReservationResponse](
		filepath.Join(e.dataDir, "flowreservation-responses.json"), "FlowReservationResponse")
	if err != nil {
		t.Fatal(err)
	}
	ended := time.Now().Add(-endedAgo).Unix()
	frq := sep2.FlowReservationRequest{MRID: "OLD", CreationTime: ended - 600, IntervalRequested: &sep2.DateTimeInterval{Start: ended - 600, Duration: 600}}
	frq.Href = "/edev/0/frq/frq-old"
	if err := reqs.Create(ctx, "0", "frq-old", frq); err != nil {
		t.Fatal(err)
	}
	frp := sep2.FlowReservationResponse{Subject: "OLD"}
	frp.Href, frp.MRID, frp.CreationTime = "/edev/0/frp/frq-old", "R-OLD", ended-600
	frp.Interval = &sep2.DateTimeInterval{Start: ended - 600, Duration: 600}
	if err := resps.Create(ctx, "0", "frq-old", frp); err != nil {
		t.Fatal(err)
	}
}

func storedOldRequest(t *testing.T, e *frRunEnv) bool {
	t.Helper()
	reqs, err := memory.NewPersistentScopedStore[sep2.FlowReservationRequest](
		filepath.Join(e.dataDir, "flowreservation-requests.json"), "FlowReservationRequest")
	if err != nil {
		t.Fatal(err)
	}
	_, err = reqs.Get(context.Background(), "0", "frq-old")
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		t.Fatal(err)
	}
	return err == nil
}

// The configured grace is the one the running sweep applies: a request that
// ended 120 s ago is removed at boot under a 60 s grace, and kept under the
// 1800 s default.
func TestRun_RetentionGraceSettingReachesTheRunningSweep(t *testing.T) {
	for _, tc := range []struct {
		name     string
		grace    time.Duration
		wantKept bool
	}{
		{"60 s grace removes it", 60 * time.Second, false},
		{"default grace keeps it", 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newFRRunEnv(t)
			seedEndedRequest(t, e, 120*time.Second)
			cfg := e.config(0)
			cfg.FlowReservationRetentionGrace = tc.grace
			stop := e.start(t, cfg)
			if err := stop(); err != nil {
				t.Fatalf("run: %v", err)
			}
			if got := storedOldRequest(t, e); got != tc.wantKept {
				t.Errorf("request stored after the boot sweep = %v, want %v", got, tc.wantKept)
			}
		})
	}
}

// A grace the setting would have refused stops startup with an error naming
// it, before recovery, the sweep or either listener: never the default.
func TestRun_InvalidRetentionGraceStopsStartup(t *testing.T) {
	e := newFRRunEnv(t)
	seedEndedRequest(t, e, 120*time.Second)
	server.SetStartRetentionAtBoot(t, func(context.Context, *server.Stores, flowreservation.Notifier, *slog.Logger, func() time.Time) func() {
		t.Error("retention started with an invalid grace")
		return func() {}
	})
	cfg := e.config(0)
	cfg.FlowReservationRetentionGrace = 1500 * time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runErr := make(chan error, 1)
	go func() { runErr <- server.Run(ctx, cfg, e.c.svc) }()
	var err error
	select {
	case err = <-runErr:
	case <-time.After(10 * time.Second):
		cancel()
		<-runErr
		t.Fatal("Run was still serving after 10 s, want a startup error")
	}
	if err == nil || !strings.Contains(err.Error(), "retention grace") {
		t.Fatalf("Run = %v, want an error naming the retention grace", err)
	}
	if !storedOldRequest(t, e) {
		t.Error("the request was removed although startup failed")
	}
}
