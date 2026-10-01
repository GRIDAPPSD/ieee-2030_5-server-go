package flowreservation_test

import (
	"context"
	"encoding/xml"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/commitment"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/commitment/sources"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/flowreservation"
	coresub "github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/handlers/subscription"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

// Tests for GRIDAPPSD/ieee-2030_5-server-go#669: every change to an
// EndDevice's response list notifies its subscribers.

type notification struct {
	href   string
	status uint8
	// seen is what onNotify observed at the instant of the call.
	seen string
	// ctxErr is the context's error at the instant of the call.
	ctxErr error
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

func (r *recordingNotifier) Notify(ctx context.Context, href string, status uint8) {
	n := notification{href: href, status: status, ctxErr: ctx.Err()}
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
	if got[0].href != wantHref || got[0].status != sep2.NotificationStatusDefault {
		t.Errorf("notification = %+v, want href %q status %d", got[0], wantHref, sep2.NotificationStatusDefault)
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
	if len(got) != before+1 || got[before].href != "/edev/"+aggID+"/frp" || got[before].status != sep2.NotificationStatusDefault {
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

// blockingNotifier parks inside Notify once armed, standing in for a slow
// subscriber lookup.
type blockingNotifier struct {
	armed   atomic.Bool
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func newBlockingNotifier() *blockingNotifier {
	return &blockingNotifier{entered: make(chan struct{}, 4), release: make(chan struct{})}
}

func (b *blockingNotifier) Notify(context.Context, string, uint8) {
	if !b.armed.Load() {
		return
	}
	b.entered <- struct{}{}
	<-b.release
}

func (b *blockingNotifier) unblock() { b.once.Do(func() { close(b.release) }) }

func (b *blockingNotifier) waitEntered(t *testing.T) {
	t.Helper()
	select {
	case <-b.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("the notifier was never called")
	}
}

// within runs fn and fails the test if it does not return in 2s.
func within(t *testing.T, what string, fn func()) {
	t.Helper()
	done := make(chan struct{})
	go func() { defer close(done); fn() }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatalf("%s was blocked while a notification was in flight", what)
	}
}

func TestNotify_SlowNotifierDuringACancelDoesNotHoldTheFleetLock(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	blk := newBlockingNotifier()
	t.Cleanup(blk.unblock)
	f := newCancelFixture(t, flowreservation.Config{Deadline: time.Hour})
	c := flowreservation.NewCanceller(f.frq, f.frp, f.queue, f.ledger, sources.NewWriters(f.issuer, f.frpLifecycles), flowreservation.WithNotifier(blk))
	base := time.Now().Add(time.Hour).Unix()
	storeRequest(t, f.frq, aggID, "R1", windowRequest("REQ-1", base, 3600, 10000))
	if _, err := f.queue.Answer(ctx, aggID, "R1", flowreservation.Decision{}); err != nil {
		t.Fatal(err)
	}
	blk.armed.Store(true)
	cancelDone := make(chan error, 1)
	go func() { cancelDone <- c.Cancel(ctx, aggID, "R1", cancelledStatus(time.Now().Unix())) }()
	blk.waitEntered(t)

	storeRequest(t, f.frq, aggID, "R2", windowRequest("REQ-2", base+7200, 3600, 10000))
	within(t, "a grant for another request on the same fleet", func() {
		if _, err := f.queue.Answer(ctx, aggID, "R2", flowreservation.Decision{}); err != nil {
			t.Errorf("answer R2: %v", err)
		}
	})
	blk.unblock()
	if err := <-cancelDone; err != nil {
		t.Fatalf("cancel: %v", err)
	}
}

func TestNotify_SlowNotifierDuringAnAnswerDoesNotHoldTheRequestLock(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	blk := newBlockingNotifier()
	t.Cleanup(blk.unblock)
	f := newCancelFixture(t, flowreservation.Config{Deadline: time.Hour})
	gate := flowreservation.NewLedgerGate(f.ledger, commitment.Resolver{Devices: f.devices, Managers: f.managers})
	q := flowreservation.NewQueue(f.frq, f.frp, gate, flowreservation.Config{Deadline: time.Hour}, nil, flowreservation.WithNotifier(blk))
	t.Cleanup(q.Close)
	storeRequest(t, f.frq, aggID, "R1", windowRequest("REQ-1", time.Now().Add(time.Hour).Unix(), 600, 10000))
	blk.armed.Store(true)
	answered := make(chan error, 1)
	go func() { _, err := q.Answer(ctx, aggID, "R1", flowreservation.Decision{}); answered <- err }()
	blk.waitEntered(t)

	within(t, "a second answer for the same request", func() {
		if _, err := q.Answer(ctx, aggID, "R1", flowreservation.Decision{}); !errors.Is(err, flowreservation.ErrAlreadyAnswered) {
			t.Errorf("second answer = %v, want ErrAlreadyAnswered", err)
		}
	})
	blk.unblock()
	if err := <-answered; err != nil {
		t.Fatal(err)
	}
}

type stubGrants struct{ err error }

func (s stubGrants) MarkCancelled(context.Context, commitment.Grant, string, int64) error {
	return s.err
}

func TestNotifyingWriters_FailedMarkCancelledDoesNotNotify(t *testing.T) {
	t.Parallel()
	rec := newRecordingNotifier()
	boom := errors.New("lifecycle store down")
	w := flowreservation.NotifyingWriters(commitment.Writers{Grants: stubGrants{err: boom}}, rec)

	err := w.Grants.MarkCancelled(context.Background(), commitment.Grant{EndDeviceID: aggID}, "r", 1)

	if !errors.Is(err, boom) {
		t.Errorf("error = %v, want the inner failure", err)
	}
	if got := rec.all(); len(got) != 0 {
		t.Errorf("notifications = %+v, want none after a failed write", got)
	}
}

func TestNotify_ContextIsDetachedFromTheCaller(t *testing.T) {
	t.Parallel()
	cctx, cancel := context.WithCancel(context.Background())
	cancel()

	direct := newRecordingNotifier()
	w := flowreservation.NotifyingWriters(commitment.Writers{Grants: stubGrants{}}, direct)
	if err := w.Grants.MarkCancelled(cctx, commitment.Grant{EndDeviceID: aggID}, "r", 1); err != nil {
		t.Fatal(err)
	}
	deferred := newRecordingNotifier()
	dw := flowreservation.NotifyingWriters(commitment.Writers{Grants: stubGrants{}}, deferred)
	dctx, flush := flowreservation.DeferNotifications(cctx, deferred)
	if err := dw.Grants.MarkCancelled(dctx, commitment.Grant{EndDeviceID: aggID}, "r", 1); err != nil {
		t.Fatal(err)
	}
	if n := len(deferred.all()); n != 0 {
		t.Fatalf("a deferred notification fired before the flush: %d", n)
	}
	flush()

	for name, rec := range map[string]*recordingNotifier{"direct": direct, "deferred": deferred} {
		got := rec.all()
		if len(got) != 1 {
			t.Errorf("%s: notifications = %+v, want 1", name, got)
			continue
		}
		if got[0].ctxErr != nil {
			t.Errorf("%s: Notify saw a cancelled context (%v); the lookup of subscribers must outlive the request", name, got[0].ctxErr)
		}
	}
}

func TestNotify_SubmitHoldsWithoutNotifyingAndEachDeviceNotifiesItsOwnList(t *testing.T) {
	t.Parallel()
	rec := newRecordingNotifier()
	f, q, _ := newNotifyFixture(t, flowreservation.Config{Deadline: time.Hour}, rec)
	req := windowRequest("REQ-H", time.Now().Add(time.Hour).Unix(), 600, 10000)
	storeRequest(t, f.frq, aggID, "R1", req)
	q.Submit(aggID, "R1", req, time.Now().Unix())
	if got := rec.all(); len(got) != 0 {
		t.Fatalf("a held request notified: %+v", got)
	}

	storeRequest(t, f.frq, standaloneID, "R2", windowRequest("REQ-S", time.Now().Add(time.Hour).Unix(), 600, 10000))
	for _, id := range []struct{ edev, frq string }{{aggID, "R1"}, {standaloneID, "R2"}} {
		if _, err := q.Answer(context.Background(), id.edev, id.frq, flowreservation.Decision{}); err != nil {
			t.Fatal(err)
		}
	}
	got := rec.all()
	if len(got) != 2 || got[0].href != "/edev/"+aggID+"/frp" || got[1].href != "/edev/"+standaloneID+"/frp" {
		t.Errorf("notifications = %+v, want one per device, each naming its own list", got)
	}
}

type errGetter struct{ err error }

func (e errGetter) Get(context.Context, string, string) (sep2.FlowReservationResponse, error) {
	return sep2.FlowReservationResponse{}, e.err
}

func TestNewPendingCheck_ResponseReadFailureAndMalformedHrefs(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	mk := func(href string) pendingFRQ {
		var r sep2.FlowReservationRequest
		r.Href = href
		return pendingFRQ{items: []sep2.FlowReservationRequest{r}}
	}
	boom := errors.New("response store down")

	got, err := flowreservation.NewPendingCheck(mk("/edev/"+aggID+"/frq/R1"), errGetter{err: boom})(ctx, aggID)
	if err == nil || !errors.Is(err, boom) || got {
		t.Errorf("failed response Get: pending=%v err=%v, want an error and not pending", got, err)
	}
	for _, href := range []string{"/edev/" + aggID + "/frq/", "/edev/" + aggID + "/frq/a/b", "/edev/" + aggID + "/frq"} {
		got, err := flowreservation.NewPendingCheck(mk(href), errGetter{err: store.ErrNotFound})(ctx, aggID)
		if err == nil || got {
			t.Errorf("href %q: pending=%v err=%v, want an error", href, got, err)
		}
	}
}

type fixedLister struct{ recs []memory.SubscriptionRecord }

func (l fixedLister) ListByResource(_ context.Context, href string) ([]memory.SubscriptionRecord, error) {
	var out []memory.SubscriptionRecord
	for _, r := range l.recs {
		if r.Subscription.SubscribedResource == href {
			out = append(out, r)
		}
	}
	return out, nil
}

// A subscriber to the response list hears a supersede, through a real
// subscription Manager, after the revision is stored and the old grant marked.
func TestNotify_SubscriberHearsASupersede(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newCancelFixture(t, flowreservation.Config{Deadline: time.Hour})
	start := time.Now().Add(time.Hour).Unix()
	storeRequest(t, f.frq, aggID, "R1", windowRequest("REQ-SUP", start, 3600, 10000))
	grant, err := f.queue.Answer(ctx, aggID, "R1", flowreservation.Decision{})
	if err != nil {
		t.Fatal(err)
	}
	revisedID := flowreservation.RevisionID("R1")

	type seen struct {
		note     sep2.Notification
		raw      string
		revision bool
		oldMark  bool
	}
	got := make(chan seen, 4)
	rcv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var n sep2.Notification
		raw, _ := io.ReadAll(r.Body)
		if err := xml.Unmarshal(raw, &n); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		_, revErr := f.frp.Get(ctx, aggID, revisedID)
		lc, lcErr := f.frpLifecycles.Get(ctx, aggID, "R1")
		got <- seen{note: n, raw: string(raw), revision: revErr == nil, oldMark: lcErr == nil && lc.CancelledAt != nil}
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(rcv.Close)

	var sub sep2.Subscription
	sub.Href = "/edev/" + aggID + "/sub/1"
	sub.SubscribedResource = flowreservation.ListHref(aggID)
	sub.NotificationURI = rcv.URL
	mgr := coresub.NewManager(fixedLister{recs: []memory.SubscriptionRecord{{ID: "s1", Subscription: sub}}}, 1, 8,
		coresub.WithDestinationPolicy(coresub.DestinationPolicy{AllowLoopback: true}))
	mctx, stop := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { defer close(done); mgr.Start(mctx) }()
	t.Cleanup(func() { stop(); <-done })

	rctx, flush := flowreservation.DeferNotifications(ctx, mgr)
	err = f.ledger.Revise(rctx, flowreservation.NotifyingWriters(sources.NewWriters(f.issuer, f.frpLifecycles), mgr),
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
	flush()

	select {
	case s := <-got:
		if s.note.Href != flowreservation.ListHref(aggID) || s.note.SubscribedResource != flowreservation.ListHref(aggID) || s.note.Status != sep2.NotificationStatusDefault {
			t.Errorf("notification = %+v, want a change notification (status 0) on %s", s.note, flowreservation.ListHref(aggID))
		}
		if !strings.Contains(s.raw, "<status>0</status>") {
			t.Errorf("wire body lacks <status>0</status>: %s", s.raw)
		}
		if !s.revision || !s.oldMark {
			t.Errorf("at delivery: revision stored=%v, old grant marked cancelled=%v; want both", s.revision, s.oldMark)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the subscriber heard nothing of the supersede")
	}
}

// One ledger call can mark more than one grant of the same device; the list
// changed once, so it is notified once. Another device is notified on its own.
func TestDeferNotifications_NotifiesEachDeviceOnce(t *testing.T) {
	t.Parallel()
	rec := newRecordingNotifier()
	w := flowreservation.NotifyingWriters(commitment.Writers{Grants: stubGrants{}}, rec)
	ctx, flush := flowreservation.DeferNotifications(context.Background(), rec)

	for _, edev := range []string{aggID, aggID, standaloneID, aggID} {
		if err := w.Grants.MarkCancelled(ctx, commitment.Grant{EndDeviceID: edev}, "r", 1); err != nil {
			t.Fatal(err)
		}
	}
	flush()

	got := rec.all()
	if len(got) != 2 || got[0].href != "/edev/"+aggID+"/frp" || got[1].href != "/edev/"+standaloneID+"/frp" {
		t.Errorf("notifications = %+v, want one for %s then one for %s", got, aggID, standaloneID)
	}
}
