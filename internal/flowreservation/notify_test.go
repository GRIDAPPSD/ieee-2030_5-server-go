package flowreservation_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/commitment"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/commitment/sources"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/flowreservation"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
)

// Tests for GRIDAPPSD/ieee-2030_5-server-go#669: every change to an
// EndDevice's response list notifies its subscribers.

type notification struct {
	href   string
	status uint8
	// seen is what onNotify observed at the instant of the call.
	seen string
}

type recordingNotifier struct {
	mu       sync.Mutex
	got      []notification
	onNotify func() string
	signal   chan struct{}
}

func newRecordingNotifier() *recordingNotifier {
	return &recordingNotifier{signal: make(chan struct{}, 16)}
}

func (r *recordingNotifier) Notify(_ context.Context, href string, status uint8) {
	n := notification{href: href, status: status}
	if r.onNotify != nil {
		n.seen = r.onNotify()
	}
	r.mu.Lock()
	r.got = append(r.got, n)
	r.mu.Unlock()
	r.signal <- struct{}{}
}

func (r *recordingNotifier) all() []notification {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]notification(nil), r.got...)
}

func (r *recordingNotifier) waitOne(t *testing.T) {
	t.Helper()
	select {
	case <-r.signal:
	case <-time.After(2 * time.Second):
		t.Fatal("no notification within 2s")
	}
}

func (r *recordingNotifier) requireOne(t *testing.T, wantHref string) notification {
	t.Helper()
	got := r.all()
	if len(got) != 1 {
		t.Fatalf("notifications = %+v, want exactly 1", got)
	}
	if got[0].href != wantHref || got[0].status != sep2.NotificationStatusChanged {
		t.Errorf("notification = %+v, want href %q status %d", got[0], wantHref, sep2.NotificationStatusChanged)
	}
	return got[0]
}

// notifyFixture is a cancel fixture whose queue and canceller notify rec.
func newNotifyFixture(t *testing.T, cfg flowreservation.Config, rec *recordingNotifier) (*cancelFixture, *flowreservation.Queue, *flowreservation.Canceller) {
	t.Helper()
	f := newCancelFixture(t, cfg)
	gate := flowreservation.NewLedgerGate(f.ledger, commitment.Resolver{Devices: f.devices, Managers: f.managers})
	q := flowreservation.NewQueue(f.frq, f.frp, gate, cfg, nil, flowreservation.WithNotifier(rec))
	t.Cleanup(q.Close)
	c := flowreservation.NewCanceller(f.frq, f.frp, q, f.ledger, sources.NewWriters(f.issuer, f.frpLifecycles), flowreservation.WithNotifier(rec))
	return f, q, c
}

func TestNotify_AnswerNotifiesAfterTheResponseIsStored(t *testing.T) {
	t.Parallel()
	rec := newRecordingNotifier()
	f, q, _ := newNotifyFixture(t, flowreservation.Config{Deadline: time.Hour}, rec)
	rec.onNotify = func() string {
		frp, err := f.frp.Get(context.Background(), aggID, "R1")
		if err != nil {
			return "missing: " + err.Error()
		}
		return frp.Href
	}
	storeRequest(t, f.frq, aggID, "R1", windowRequest("REQ-A", time.Now().Add(time.Hour).Unix(), 600, 10000))

	if _, err := q.Answer(context.Background(), aggID, "R1", flowreservation.Decision{}); err != nil {
		t.Fatal(err)
	}

	n := rec.requireOne(t, "/edev/"+aggID+"/frp")
	if n.seen != "/edev/"+aggID+"/frp/R1" {
		t.Errorf("response seen at notify time = %q, want it already stored at /edev/%s/frp/R1", n.seen, aggID)
	}
}

func TestNotify_DeadlineFallbackNotifies(t *testing.T) {
	t.Parallel()
	rec := newRecordingNotifier()
	f, q, _ := newNotifyFixture(t, flowreservation.Config{Deadline: shortDeadline}, rec)
	req := windowRequest("REQ-F", time.Now().Add(time.Hour).Unix(), 600, 10000)
	storeRequest(t, f.frq, aggID, "R1", req)
	q.Submit(aggID, "R1", req, time.Now().Unix())

	rec.waitOne(t)

	rec.requireOne(t, "/edev/"+aggID+"/frp")
	if _, err := f.frp.Get(context.Background(), aggID, "R1"); err != nil {
		t.Errorf("the fallback notified but stored no response: %v", err)
	}
}

func TestNotify_CancellingAPendingRequestNotifiesItsDenial(t *testing.T) {
	t.Parallel()
	rec := newRecordingNotifier()
	f, _, c := newNotifyFixture(t, flowreservation.Config{Deadline: time.Hour}, rec)
	storeRequest(t, f.frq, aggID, "R1", windowRequest("REQ-C", time.Now().Add(time.Hour).Unix(), 600, 10000))

	if err := c.Cancel(context.Background(), aggID, "R1", cancelledStatus(time.Now().Unix())); err != nil {
		t.Fatal(err)
	}

	rec.requireOne(t, "/edev/"+aggID+"/frp")
	frp, err := f.frp.Get(context.Background(), aggID, "R1")
	if err != nil || frp.Interval == nil || frp.Interval.Duration != 0 {
		t.Errorf("stored response = %+v, %v; want the zero-duration denial", frp.Interval, err)
	}
}

func TestNotify_CancellingAGrantNotifiesOnceTheCancelMarkIsWritten(t *testing.T) {
	t.Parallel()
	rec := newRecordingNotifier()
	f, q, c := newNotifyFixture(t, flowreservation.Config{Deadline: time.Hour}, rec)
	ctx := context.Background()
	storeRequest(t, f.frq, aggID, "R1", windowRequest("REQ-G", time.Now().Add(time.Hour).Unix(), 3600, 10000))
	if _, err := q.Answer(ctx, aggID, "R1", flowreservation.Decision{}); err != nil {
		t.Fatal(err)
	}
	if got := len(rec.all()); got != 1 {
		t.Fatalf("setup: notifications after the grant = %d, want 1", got)
	}
	rec.onNotify = func() string {
		lc, err := f.frpLifecycles.Get(ctx, aggID, "R1")
		if err != nil || lc.CancelledAt == nil {
			return "not cancelled"
		}
		return lc.CancelReason
	}

	if err := c.Cancel(ctx, aggID, "R1", cancelledStatus(time.Now().Unix())); err != nil {
		t.Fatal(err)
	}

	got := rec.all()
	if len(got) != 2 {
		t.Fatalf("notifications = %+v, want the grant's and the cancel's", got)
	}
	if got[1].href != "/edev/"+aggID+"/frp" || got[1].seen != "client cancel" {
		t.Errorf("cancel notification = %+v, want the list href after the cancel mark (reason client cancel)", got[1])
	}

	// A repeated cancel finds the grant not live and notifies no one.
	if err := c.Cancel(ctx, aggID, "R1", cancelledStatus(time.Now().Unix())); err != nil {
		t.Fatal(err)
	}
	if n := len(rec.all()); n != 2 {
		t.Errorf("a repeated cancel notified again: %d notifications, want 2", n)
	}
}

func TestNotify_ReviseNotifiesThroughTheGrantWriter(t *testing.T) {
	t.Parallel()
	rec := newRecordingNotifier()
	f, q, _ := newNotifyFixture(t, flowreservation.Config{Deadline: time.Hour}, rec)
	ctx := context.Background()
	start := time.Now().Add(time.Hour).Unix()
	storeRequest(t, f.frq, aggID, "R1", windowRequest("REQ-R", start, 3600, 10000))
	grant, err := q.Answer(ctx, aggID, "R1", flowreservation.Decision{})
	if err != nil {
		t.Fatal(err)
	}
	before := len(rec.all())

	revisedID := flowreservation.RevisionID("R1")
	err = f.ledger.Revise(ctx, flowreservation.NotifyingWriters(sources.NewWriters(f.issuer, f.frpLifecycles), rec),
		grant.MRID, "revised", time.Now().Unix(),
		func(commitment.Grant) (commitment.Replacement, error) {
			rev := grant
			rev.MRID = "REVISED-MRID"
			rev.Href = "/edev/" + aggID + "/frp/" + revisedID
			rev.CreationTime = grant.CreationTime + 1
			rev.Interval = &sep2.DateTimeInterval{Start: start, Duration: 1800}
			return sources.NewReplacement(f.frp, aggID, rev)
		})
	if err != nil {
		t.Fatal(err)
	}

	got := rec.all()
	if len(got) != before+1 || got[before].href != "/edev/"+aggID+"/frp" || got[before].status != sep2.NotificationStatusChanged {
		t.Fatalf("notifications = %+v, want one more list notification after the revision", got)
	}
	if _, err := f.frp.Get(ctx, aggID, revisedID); err != nil {
		t.Errorf("revision not stored: %v", err)
	}
}

func TestNotify_NilNotifierAndMissingWritersChangeNothing(t *testing.T) {
	t.Parallel()
	f := newCancelFixture(t, flowreservation.Config{Deadline: time.Hour})
	storeRequest(t, f.frq, aggID, "R1", windowRequest("REQ-N", time.Now().Add(time.Hour).Unix(), 600, 10000))
	if _, err := f.queue.Answer(context.Background(), aggID, "R1", flowreservation.Decision{}); err != nil {
		t.Fatalf("a queue with no notifier failed to answer: %v", err)
	}
	rec := newRecordingNotifier()
	if w := flowreservation.NotifyingWriters(commitment.Writers{}, rec); w.Grants != nil {
		t.Error("NotifyingWriters gave empty writers a grant writer, so the ledger would no longer refuse them")
	}
}

type pendingFRQ struct {
	items []sep2.FlowReservationRequest
	err   error
}

func (p pendingFRQ) List(context.Context, string, store.ListOptions) (store.ListResult[sep2.FlowReservationRequest], error) {
	return store.ListResult[sep2.FlowReservationRequest]{Items: p.items}, p.err
}

func TestNewPendingCheck(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newCancelFixture(t, flowreservation.Config{Deadline: time.Hour})
	req := func(id string, status uint8) sep2.FlowReservationRequest {
		r := windowRequest("REQ-"+id, time.Now().Add(time.Hour).Unix(), 600, 10000)
		r.Href = "/edev/" + aggID + "/frq/" + id
		r.RequestStatus = sep2.RequestStatus{RequestStatus: status}
		return r
	}
	storeRequest(t, f.frq, aggID, "ANSWERED", req("ANSWERED", sep2.RequestStatusRequested))
	if _, err := f.queue.Answer(ctx, aggID, "ANSWERED", flowreservation.Decision{}); err != nil {
		t.Fatal(err)
	}
	boom := errors.New("backend down")
	var outsider sep2.FlowReservationRequest
	outsider.Href = "/edev/other/frq/x"

	cases := []struct {
		name    string
		frq     flowreservation.FRQLister
		want    bool
		wantErr bool
	}{
		{"answered request is not pending", pendingFRQ{items: []sep2.FlowReservationRequest{req("ANSWERED", 0)}}, false, false},
		{"unanswered request is pending", pendingFRQ{items: []sep2.FlowReservationRequest{req("ANSWERED", 0), req("WAITING", 0)}}, true, false},
		{"withdrawn request without a response is not pending", pendingFRQ{items: []sep2.FlowReservationRequest{req("WITHDRAWN", sep2.RequestStatusCancelled)}}, false, false},
		{"no requests", pendingFRQ{}, false, false},
		{"list failure is an error, not an answer", pendingFRQ{err: boom}, false, true},
		{"href outside the device is an error", pendingFRQ{items: []sep2.FlowReservationRequest{outsider}}, false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := flowreservation.NewPendingCheck(tc.frq, f.frp)(ctx, aggID)
			if (err != nil) != tc.wantErr || got != tc.want {
				t.Errorf("pending = %v, err = %v; want %v, error %v", got, err, tc.want, tc.wantErr)
			}
		})
	}
}
