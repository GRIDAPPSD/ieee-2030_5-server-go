package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/config"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/dercontrol"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/assembly"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/handlers/sep2time"
)

const frpDevice = "dev"

// frpFixture is what the two-boot tests write and expect back.
type frpFixture struct {
	pending       sep2.FlowReservationRequest
	future        sep2.FlowReservationResponse
	cancelled     sep2.FlowReservationResponse
	cancelledMark dercontrol.LifecycleRecord
}

func newFRPFixture(start int64) frpFixture {
	var f frpFixture
	f.pending = sep2.FlowReservationRequest{MRID: "REQ-PENDING", Description: "pending", CreationTime: 100}
	f.pending.Href = "/edev/dev/frq/frq-1"
	f.pending.IntervalRequested = &sep2.DateTimeInterval{Start: start, Duration: 600}
	f.pending.RequestStatus = sep2.RequestStatus{DateTime: 100, RequestStatus: sep2.RequestStatusRequested}

	f.future = sep2.FlowReservationResponse{Subject: "REQ-PENDING"}
	f.future.Href = "/edev/dev/frp/frq-2"
	f.future.MRID = "GRANT-FUTURE"
	f.future.CreationTime = start - 100
	f.future.Interval = &sep2.DateTimeInterval{Start: start, Duration: 600}
	f.future.EnergyAvailable = &sep2.SignedRealEnergy{Value: 1500}

	f.cancelled = sep2.FlowReservationResponse{Subject: "REQ-CANCELLED"}
	f.cancelled.Href = "/edev/dev/frp/frq-3"
	f.cancelled.MRID = "GRANT-CANCELLED"
	f.cancelled.CreationTime = start - 100
	f.cancelled.Interval = &sep2.DateTimeInterval{Start: start, Duration: 600}

	at := int64(777)
	f.cancelledMark = dercontrol.LifecycleRecord{CancelledAt: &at, CancelReason: "operator cancel"}
	return f
}

func (f frpFixture) seed(t *testing.T, s *Stores) {
	t.Helper()
	ctx := context.Background()
	for _, err := range []error{
		s.FlowReservationRequests.Create(ctx, frpDevice, "frq-1", f.pending),
		s.FlowReservationResponses.Create(ctx, frpDevice, "frq-2", f.future),
		s.FlowReservationResponses.Create(ctx, frpDevice, "frq-3", f.cancelled),
		s.FlowReservationResponseLifecycles.Create(ctx, frpDevice, "frq-3", f.cancelledMark),
	} {
		if err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
}

// requireStored asserts the three collections hold exactly the fixture,
// field for field, read from the stores as stored.
func (f frpFixture) requireStored(t *testing.T, s *Stores) {
	t.Helper()
	ctx := context.Background()
	if got, err := s.FlowReservationRequests.Get(ctx, frpDevice, "frq-1"); err != nil || !reflect.DeepEqual(got, f.pending) {
		t.Errorf("request frq-1 = %+v, %v, want %+v", got, err, f.pending)
	}
	if got, err := s.FlowReservationResponses.Get(ctx, frpDevice, "frq-2"); err != nil || !reflect.DeepEqual(got, f.future) {
		t.Errorf("response frq-2 = %+v, %v, want %+v", got, err, f.future)
	}
	if got, err := s.FlowReservationResponses.Get(ctx, frpDevice, "frq-3"); err != nil || !reflect.DeepEqual(got, f.cancelled) {
		t.Errorf("response frq-3 = %+v, %v, want %+v", got, err, f.cancelled)
	}
	if got, err := s.FlowReservationResponseLifecycles.Get(ctx, frpDevice, "frq-3"); err != nil || !reflect.DeepEqual(got, f.cancelledMark) {
		t.Errorf("lifecycle frq-3 = %+v, %v, want %+v", got, err, f.cancelledMark)
	}
	if _, err := s.FlowReservationResponseLifecycles.Get(ctx, frpDevice, "frq-2"); err == nil {
		t.Error("lifecycle frq-2 exists, want none: the future grant was never cancelled")
	}
}

func bootFRP(t *testing.T, dir string) (*Stores, *assembly.Stores) {
	t.Helper()
	s, _, err := newRunStores(&config.Config{DataDir: dir})
	if err != nil {
		t.Fatalf("newRunStores: %v", err)
	}
	return s, NewCoreStores(s)
}

// TestFlowReservationStores_SurviveARestart: a pending request, a future
// grant and a cancelled grant read back field for field after a second boot
// on the same data directory, the cancelled grant is served Cancelled at its
// mark, and the future grant is served Scheduled and then Active once its
// start passes.
func TestFlowReservationStores_SurviveARestart(t *testing.T) {
	dir := t.TempDir()
	start := sep2time.Now().Unix() + 2
	fx := newFRPFixture(start)

	boot1, _ := bootFRP(t, dir)
	fx.seed(t, boot1)
	for _, name := range []string{"flowreservation-requests", "flowreservation-responses", "flowreservation-response-lifecycles"} {
		if _, err := os.Stat(filepath.Join(dir, name+".json")); err != nil {
			t.Fatalf("snapshot %s after the writes: %v", name, err)
		}
	}

	boot2, core2 := bootFRP(t, dir)
	fx.requireStored(t, boot2)

	served := assembly.NewReaderStores(core2).FlowReservationResponses
	ctx := context.Background()
	cancelled, err := served.Get(ctx, frpDevice, "frq-3")
	if err != nil || cancelled.EventStatus == nil ||
		cancelled.EventStatus.CurrentStatus != sep2.EventStatusCancelled || cancelled.EventStatus.DateTime != 777 {
		t.Errorf("cancelled grant served %+v, %v, want Cancelled at 777", cancelled.EventStatus, err)
	}
	future, err := served.Get(ctx, frpDevice, "frq-2")
	if err != nil || future.EventStatus == nil || future.EventStatus.CurrentStatus != sep2.EventStatusScheduled {
		t.Fatalf("future grant served %+v, %v, want Scheduled", future.EventStatus, err)
	}
	deadline := time.Now().Add(6 * time.Second)
	for {
		future, err = served.Get(ctx, frpDevice, "frq-2")
		if err != nil {
			t.Fatal(err)
		}
		if future.EventStatus.CurrentStatus == sep2.EventStatusActive || time.Now().After(deadline) {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if future.EventStatus.CurrentStatus != sep2.EventStatusActive {
		t.Errorf("future grant after its start = %+v, want Active", future.EventStatus)
	}
}

// deleteDevice serves DELETE /edev/dev through the assembled protocol
// router over stores, as the device would, and returns the status.
func deleteDevice(t *testing.T, core *assembly.Stores) int {
	t.Helper()
	const lfdi = "AABBCCDDEEFF0011223344556677889900112233"
	policy := assembly.AuthPolicy{
		Wrap:       func(h http.Handler) http.Handler { return h },
		Identity:   func(context.Context) (string, string, bool) { return lfdi, "AABBCCDD11223344", true },
		SFDIPrefix: func(sfdi string) (string, error) { return sfdi[:8], nil },
	}
	protocol, _ := assembly.BuildProtocolRouter(assembly.RouterConfig{}, core, policy, "serverSFDI", "serverLFDI", nil)
	srv := httptest.NewServer(protocol)
	defer srv.Close()
	req, err := http.NewRequest(http.MethodDelete, srv.URL+"/edev/"+frpDevice, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close() // only the status is read
	return resp.StatusCode
}

func seedFRPDevice(t *testing.T, s *Stores) {
	t.Helper()
	dev := sep2.EndDevice{LFDI: "AABBCCDDEEFF0011223344556677889900112233", SFDI: "AABBCCDD11223344"}
	if err := s.EndDevices.Create(context.Background(), frpDevice, dev); err != nil {
		t.Fatal(err)
	}
}

// TestFlowReservationStores_EndDeviceDeleteLeavesNothingUnderItsKey: deleting
// the EndDevice leaves no request, response or response lifecycle under its
// key, in memory or after a restart.
func TestFlowReservationStores_EndDeviceDeleteLeavesNothingUnderItsKey(t *testing.T) {
	dir := t.TempDir()
	fx := newFRPFixture(sep2time.Now().Unix() + 3600)
	boot1, core1 := bootFRP(t, dir)
	seedFRPDevice(t, boot1)
	fx.seed(t, boot1)
	ctx := context.Background()
	// Control: the lifecycle is present before the delete, so a zero after
	// it is the cascade's doing.
	if n, err := boot1.FlowReservationResponseLifecycles.Count(ctx, frpDevice); err != nil || n != 1 {
		t.Fatalf("control: lifecycles under %s = %d, %v, want 1", frpDevice, n, err)
	}

	if code := deleteDevice(t, core1); code/100 != 2 {
		t.Fatalf("DELETE /edev/%s = %d, want 2xx", frpDevice, code)
	}
	assertFRPEmpty := func(t *testing.T, s *Stores, when string) {
		t.Helper()
		for name, count := range map[string]func() (uint32, error){
			"requests":   func() (uint32, error) { return s.FlowReservationRequests.Count(ctx, frpDevice) },
			"responses":  func() (uint32, error) { return s.FlowReservationResponses.Count(ctx, frpDevice) },
			"lifecycles": func() (uint32, error) { return s.FlowReservationResponseLifecycles.Count(ctx, frpDevice) },
		} {
			if n, err := count(); err != nil || n != 0 {
				t.Errorf("%s under %s %s = %d, %v, want 0", name, frpDevice, when, n, err)
			}
		}
	}
	assertFRPEmpty(t, boot1, "after the delete")
	boot2, _ := bootFRP(t, dir)
	assertFRPEmpty(t, boot2, "after a restart")
}

// TestFlowReservationStores_FailedLifecycleSnapshotLeavesTheDeviceAndEverythingUnderIt:
// when only the lifecycle snapshot cannot be written, the delete fails and
// the device, requests, responses and the cancel mark are all still there,
// in memory and after a restart.
func TestFlowReservationStores_FailedLifecycleSnapshotLeavesTheDeviceAndEverythingUnderIt(t *testing.T) {
	dir := t.TempDir()
	fx := newFRPFixture(sep2time.Now().Unix() + 3600)
	boot1, core1 := bootFRP(t, dir)
	seedFRPDevice(t, boot1)
	fx.seed(t, boot1)

	// An atomic write renames a temp file over the snapshot, which cannot
	// replace a non-empty directory.
	lifecyclePath := filepath.Join(dir, "flowreservation-response-lifecycles.json")
	if err := os.Remove(lifecyclePath); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(lifecyclePath, "keep"), 0o755); err != nil {
		t.Fatal(err)
	}

	if code := deleteDevice(t, core1); code/100 == 2 {
		t.Fatalf("DELETE /edev/%s = %d with an unwritable lifecycle snapshot, want a failure", frpDevice, code)
	}
	if _, err := boot1.EndDevices.Get(context.Background(), frpDevice); err != nil {
		t.Errorf("device after the failed delete: %v, want present", err)
	}
	fx.requireStored(t, boot1)

	if err := os.RemoveAll(lifecyclePath); err != nil {
		t.Fatal(err)
	}
	// The lifecycle file was replaced by the sabotage, so only the two
	// collections the restart can still read are compared from disk.
	boot2, _ := bootFRP(t, dir)
	ctx := context.Background()
	if got, err := boot2.FlowReservationRequests.Get(ctx, frpDevice, "frq-1"); err != nil || !reflect.DeepEqual(got, fx.pending) {
		t.Errorf("request after restart = %+v, %v, want %+v", got, err, fx.pending)
	}
	if got, err := boot2.FlowReservationResponses.Get(ctx, frpDevice, "frq-3"); err != nil || !reflect.DeepEqual(got, fx.cancelled) {
		t.Errorf("response after restart = %+v, %v, want %+v", got, err, fx.cancelled)
	}
}
