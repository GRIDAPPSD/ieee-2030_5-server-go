package server_test

// #763: the flow reservation queue as server.Run wires it. Each test drives
// Run over a real data directory, because the properties (one queue, recovery
// before bind, no timer after Run, the deadline setting) are facts about the
// wiring that no assembly-level test can reach.

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"log"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/config"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/flowreservation"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/server"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

const frRunRequestMRID = "0102030405060708090A0B0C0D0E0F10"

// frRunEnv is one device, one fixture and one data directory shared by every
// Run a test starts.
type frRunEnv struct {
	c       *splitListenerCerts
	device  managementE2EDevice
	dataDir string
	fixture string
}

func newFRRunEnv(t *testing.T) *frRunEnv {
	t.Helper()
	c := newSplitListenerCerts(t)
	device := newManagementE2EDevice(t, c, "fr-run")
	fixture := filepath.Join(t.TempDir(), "fr-run.yaml")
	yaml := fmt.Sprintf(`end_devices:
  - id: "0"
    sfdi: %q
    lfdi: %q
    enabled: true
`, device.sfdi, device.lfdi)
	if err := os.WriteFile(fixture, []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	return &frRunEnv{c: c, device: device, dataDir: t.TempDir(), fixture: fixture}
}

func (e *frRunEnv) config(deadline time.Duration) *config.Config {
	pen := uint32(0xA0B1)
	return &config.Config{
		Addr:                      e.c.sep2Probe,
		CertFile:                  e.c.certFile,
		KeyFile:                   e.c.keyFile,
		CAFile:                    e.c.caFile,
		AdminListen:               e.c.adminProbe,
		AdminKey:                  adminTestKey,
		TZOffset:                  -28800,
		TimeQuality:               sep2.TimeQualityNTP,
		PEN:                       &pen,
		DataDir:                   e.dataDir,
		NotificationAllowLoopback: true,
		BootFixtureFile:           e.fixture,
		FlowReservationDeadline:   deadline,
	}
}

// start runs server.Run on cfg, waits for the protocol listener, and returns
// a stop that cancels Run and returns what Run returned.
func (e *frRunEnv) start(t *testing.T, cfg *config.Config) (stop func() error) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	runErr := make(chan error, 1)
	go func() { runErr <- server.Run(ctx, cfg, e.c.svc) }()
	var once sync.Once
	var result error
	stop = func() error {
		once.Do(func() {
			cancel()
			select {
			case result = <-runErr:
			case <-time.After(10 * time.Second):
				result = errors.New("server.Run did not return within 10s after cancel")
			}
		})
		return result
	}
	t.Cleanup(func() {
		if err := stop(); err != nil {
			t.Errorf("server.Run: %v", err)
		}
	})
	if !waitForServerReady(e.c.sep2Probe, 5*time.Second, e.device.tlsConfig) {
		t.Fatal("SEP2 listener never became ready")
	}
	return stop
}

func (e *frRunEnv) protocolClient() *http.Client {
	return ccmHTTPClient(e.device.tlsConfig, 3*time.Second)
}

func (e *frRunEnv) postRequest(t *testing.T) {
	t.Helper()
	duration := uint16(600)
	body, err := xml.Marshal(&sep2.FlowReservationRequest{MRID: frRunRequestMRID, Description: "run test", DurationRequested: &duration})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := e.protocolClient().Post("https://"+e.c.sep2Probe+"/edev/0/frq", "application/sep+xml", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusCreated {
		got, _ := io.ReadAll(resp.Body)
		t.Fatalf("POST /edev/0/frq: status %d body %s", resp.StatusCode, got)
	}
}

// storedResponses reads the persisted responses of device 0 the way a later
// boot would, from the data directory.
func (e *frRunEnv) storedResponses(t *testing.T) []sep2.FlowReservationResponse {
	t.Helper()
	s, err := memory.NewPersistentScopedStore[sep2.FlowReservationResponse](
		filepath.Join(e.dataDir, "flowreservation-responses.json"), "FlowReservationResponse")
	if err != nil {
		t.Fatalf("reopen response store: %v", err)
	}
	page, err := s.List(context.Background(), "0", store.ListOptions{Unbounded: true})
	if err != nil {
		t.Fatalf("list responses: %v", err)
	}
	return page.Items
}

func (e *frRunEnv) waitForStoredResponse(t *testing.T, within time.Duration) []sep2.FlowReservationResponse {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if got := e.storedResponses(t); len(got) > 0 {
			return got
		}
		time.Sleep(100 * time.Millisecond)
	}
	return nil
}

// A request posted before a restart is answered by the fallback after it,
// over the same data directory: the first run stops before the hold ends and
// leaves no response, and the second run's Recover re-arms the hold.
func TestRun_FlowReservationRequestIsAnsweredByTheFallbackAfterARestart(t *testing.T) {
	e := newFRRunEnv(t)

	stop := e.start(t, e.config(3*time.Second))
	e.postRequest(t)
	if err := stop(); err != nil {
		t.Fatalf("first run: %v", err)
	}
	if got := e.storedResponses(t); len(got) != 0 {
		t.Fatalf("after the first run %d responses are stored, want none: the hold had not ended", len(got))
	}

	stop = e.start(t, e.config(3*time.Second))
	got := e.waitForStoredResponse(t, 10*time.Second)
	if len(got) != 1 {
		t.Fatalf("after the restart %d responses are stored, want exactly 1 from the fallback", len(got))
	}
	if got[0].Subject != frRunRequestMRID {
		t.Errorf("response Subject = %q, want the request's mRID %q", got[0].Subject, frRunRequestMRID)
	}
	if !strings.HasPrefix(got[0].Href, "/edev/0/frp/") || got[0].MRID == "" {
		t.Errorf("response Href = %q MRID = %q, want an href under /edev/0/frp/ and a minted mRID", got[0].Href, got[0].MRID)
	}
	// Let the rest of the hold pass and the run end: a second answer to the
	// same request would be stored by then.
	time.Sleep(time.Second)
	if err := stop(); err != nil {
		t.Fatalf("second run: %v", err)
	}
	if n := len(e.storedResponses(t)); n != 1 {
		t.Errorf("after the second run %d responses are stored, want exactly 1", n)
	}
}

// Run closes the queue on the way out: a timer left running would answer the
// request after Run returned, and the answer would reach the data directory.
func TestRun_NoQueueTimerSurvivesRun(t *testing.T) {
	e := newFRRunEnv(t)

	stop := e.start(t, e.config(time.Second))
	e.postRequest(t)
	if err := stop(); err != nil {
		t.Fatalf("run: %v", err)
	}
	if got := e.waitForStoredResponse(t, 2500*time.Millisecond); len(got) != 0 {
		t.Fatalf("%d responses stored after Run returned, want none: a deadline timer outlived Run", len(got))
	}
}

// One request that cannot be repaired is logged at error level, once, and the
// server still serves.
func TestRun_UnrepairableRequestIsLoggedAndBootContinues(t *testing.T) {
	e := newFRRunEnv(t)
	seed, err := memory.NewPersistentScopedStore[sep2.FlowReservationRequest](
		filepath.Join(e.dataDir, "flowreservation-requests.json"), "FlowReservationRequest")
	if err != nil {
		t.Fatal(err)
	}
	bad := sep2.FlowReservationRequest{MRID: "BAD", CreationTime: 100}
	bad.Href = "/not/an/frq/href"
	if err := seed.Create(context.Background(), "0", "bad", bad); err != nil {
		t.Fatal(err)
	}

	logs := &lockedBuffer{}
	prev := log.Writer()
	log.SetOutput(logs)
	t.Cleanup(func() { log.SetOutput(prev) })

	stop := e.start(t, e.config(0))
	if err := stop(); err != nil {
		t.Fatalf("run: %v", err)
	}
	line := ""
	for l := range strings.SplitSeq(logs.String(), "\n") {
		if strings.Contains(l, "request not repaired") {
			line = l
		}
	}
	if !strings.Contains(line, "ERROR") || !strings.Contains(line, "/not/an/frq/href") {
		t.Errorf("no error-level line naming the request's href; last matching line = %q", line)
	}
}

// A recovery pass that fails stops the boot with that error, before the
// protocol listener is bound.
func TestRun_RecoveryPassErrorStopsBootBeforeBind(t *testing.T) {
	e := newFRRunEnv(t)
	boom := errors.New("recovery pass failed")
	server.SetRecoverAtBoot(t, func(context.Context, *server.Stores, *flowreservation.Queue, flowreservation.Notifier, *slog.Logger, time.Time) (flowreservation.RecoverCounts, error) {
		return flowreservation.RecoverCounts{}, boom
	})

	err := server.Run(context.Background(), e.config(0), e.c.svc)
	if !errors.Is(err, boom) || !strings.Contains(err.Error(), "flow reservation recovery") {
		t.Fatalf("Run = %v, want a flow reservation recovery error wrapping the pass error", err)
	}
	l, lerr := net.Listen("tcp", e.c.sep2Probe)
	if lerr != nil {
		t.Fatalf("protocol address is bound after a failed boot: %v", lerr)
	}
	_ = l.Close()
}

// A stop requested during recovery is a clean stop whether or not requests
// are stored: the pass sees the cancelled context only between requests, so
// without this the exit status would depend on the data directory.
func TestRun_StopDuringRecoveryIsACleanStop(t *testing.T) {
	e := newFRRunEnv(t)
	seed, err := memory.NewPersistentScopedStore[sep2.FlowReservationRequest](
		filepath.Join(e.dataDir, "flowreservation-requests.json"), "FlowReservationRequest")
	if err != nil {
		t.Fatal(err)
	}
	req := sep2.FlowReservationRequest{MRID: "REQ", CreationTime: 100}
	req.Href = "/edev/0/frq/r1"
	if err := seed.Create(context.Background(), "0", "r1", req); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	cfg := e.config(0)
	cfg.BootFixtureFile = ""
	if err := server.Run(ctx, cfg, e.c.svc); err != nil {
		t.Fatalf("Run = %v, want nil: a requested stop is not a failure", err)
	}
	l, lerr := net.Listen("tcp", e.c.sep2Probe)
	if lerr != nil {
		t.Fatalf("protocol address is bound after a stop during recovery: %v", lerr)
	}
	_ = l.Close()
}

// Recovery runs while neither listener is bound: a recovery that ran after
// the protocol listener started would find its address taken.
func TestRun_RecoveryRunsBeforeEitherListenerBinds(t *testing.T) {
	e := newFRRunEnv(t)
	var free map[string]bool
	server.SetRecoverAtBoot(t, func(ctx context.Context, s *server.Stores, q *flowreservation.Queue, n flowreservation.Notifier, l *slog.Logger, now time.Time) (flowreservation.RecoverCounts, error) {
		free = map[string]bool{}
		for name, addr := range map[string]string{"protocol": e.c.sep2Probe, "admin": e.c.adminProbe} {
			if ln, err := net.Listen("tcp", addr); err == nil {
				free[name] = true
				_ = ln.Close()
			}
		}
		return server.RecoverFlowReservations(ctx, s, q, n, l, now)
	})

	e.start(t, e.config(0))
	if free == nil {
		t.Fatal("the recovery hook never ran")
	}
	if !free["protocol"] || !free["admin"] {
		t.Errorf("addresses free at recovery = %v, want both: a listener was already bound", free)
	}
}

// Close runs on an error exit after recovery as well as on shutdown: with the
// certificate missing, Run fails after it has re-armed the stored request,
// and the re-armed hold must not outlive Run.
func TestRun_NoQueueTimerSurvivesAnErrorExitAfterRecovery(t *testing.T) {
	e := newFRRunEnv(t)
	seed, err := memory.NewPersistentScopedStore[sep2.FlowReservationRequest](
		filepath.Join(e.dataDir, "flowreservation-requests.json"), "FlowReservationRequest")
	if err != nil {
		t.Fatal(err)
	}
	req := sep2.FlowReservationRequest{MRID: "REQ", CreationTime: time.Now().Unix()}
	req.Href = "/edev/0/frq/r1"
	if err := seed.Create(context.Background(), "0", "r1", req); err != nil {
		t.Fatal(err)
	}

	cfg := e.config(time.Second)
	cfg.BootFixtureFile = ""
	cfg.CertFile = filepath.Join(t.TempDir(), "missing.pem")
	if err := server.Run(context.Background(), cfg, e.c.svc); err == nil {
		t.Fatal("Run with a missing server certificate returned nil, want the error that ends this boot after recovery")
	}
	if got := e.waitForStoredResponse(t, 2500*time.Millisecond); len(got) != 0 {
		t.Fatalf("%d responses stored after Run failed, want none: the re-armed hold outlived Run", len(got))
	}
}

// The queue Run builds carries the notifier: when the fallback answers, the
// device's subscription to its response list is told.
func TestRun_FallbackAnswerNotifiesTheDevicesResponseList(t *testing.T) {
	e := newFRRunEnv(t)
	e.start(t, e.config(2*time.Second))

	rcv := newLoopbackReceiver(t)
	sub := `<Subscription xmlns="urn:ieee:std:2030.5:ns"><subscribedResource>/edev/0/frp</subscribedResource>` +
		`<notificationURI>` + rcv.uri + `</notificationURI><encoding>0</encoding></Subscription>`
	resp, err := e.protocolClient().Post("https://"+e.c.sep2Probe+"/edev/0/sub", "application/sep+xml", strings.NewReader(sub))
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("subscribe: status %d body %s", resp.StatusCode, got)
	}

	e.postRequest(t)
	time.Sleep(500 * time.Millisecond)
	before := rcv.hits.Load()
	deadline := time.Now().Add(6 * time.Second)
	for rcv.hits.Load() == before && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if rcv.hits.Load() == before {
		t.Fatal("no notification reached the response-list subscriber after the fallback answered")
	}
	if n := len(e.storedResponses(t)); n != 1 {
		t.Errorf("responses stored = %d, want 1", n)
	}
}

// The setting reaches the admin read API through the same value the queue
// runs under, and the stores report that they persist.
func TestRun_DeadlineSettingReachesTheAdminReadAPI(t *testing.T) {
	e := newFRRunEnv(t)
	e.start(t, e.config(120*time.Second))

	admin := &http.Client{Timeout: 3 * time.Second}
	url := "http://" + e.c.adminProbe + "/api/derms/flow-reservations?aggregatorLFDI=" + e.device.lfdi
	var list struct {
		DeadlineSeconds int  `json:"deadlineSeconds"`
		Persisted       bool `json:"persisted"`
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		req, _ := http.NewRequest(http.MethodGet, url, nil)
		req.Header.Set("Authorization", "Bearer "+adminTestKey)
		resp, err := admin.Do(req)
		if err == nil {
			body, _ := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("GET flow-reservations: status %d body %s", resp.StatusCode, body)
			}
			if err := json.Unmarshal(body, &list); err != nil {
				t.Fatal(err)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("admin listener never answered: %v", err)
		}
		time.Sleep(25 * time.Millisecond)
	}
	if list.DeadlineSeconds != 120 {
		t.Errorf("deadlineSeconds = %d, want 120 from the setting", list.DeadlineSeconds)
	}
	if !list.Persisted {
		t.Error("persisted = false with a data directory, want true")
	}
}

// lockedBuffer is a log sink the server's goroutines and the test can share.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}
