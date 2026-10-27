package assembly_test

import (
	"context"
	"encoding/xml"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/assembly"
	coresub "github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/handlers/subscription"
)

// noticeSink is a notification receiver that records, per URL path, the
// notifications POSTed to it.
type noticeSink struct {
	mu   sync.Mutex
	seen map[string][]sep2.Notification
}

func newNoticeSink(t *testing.T) (*noticeSink, string) {
	t.Helper()
	s := &noticeSink{seen: map[string][]sep2.Notification{}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var n sep2.Notification
		if err := xml.Unmarshal(body, &n); err == nil {
			s.mu.Lock()
			s.seen[r.URL.Path] = append(s.seen[r.URL.Path], n)
			s.mu.Unlock()
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	return s, srv.URL
}

func (s *noticeSink) got(path string) []sep2.Notification {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]sep2.Notification(nil), s.seen[path]...)
}

func (s *noticeSink) waitFor(path string) []sep2.Notification {
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if g := s.got(path); len(g) > 0 {
			return g
		}
		time.Sleep(10 * time.Millisecond)
	}
	return nil
}

// seedDeletedNoticeSubs stores a subscription on subscribed, created under
// edevID, that posts to <base>/<name>.
func seedDeletedNoticeSubs(t *testing.T, stores *assembly.Stores, base string, subs map[string][2]string) {
	t.Helper()
	for name, v := range subs {
		sub := sep2.Subscription{
			SubscribableResource: sep2.SubscribableResource{Resource: sep2.Resource{Href: "/edev/" + v[0] + "/sub/" + name}},
			SubscribedResource:   v[1],
			NotificationURI:      base + "/" + name,
		}
		if err := stores.Subscriptions.Create(context.Background(), name, sub); err != nil {
			t.Fatalf("seed subscription %s: %v", name, err)
		}
	}
}

func deletedNoticeRouter(t *testing.T, fleet managementFleet) *httptest.Server {
	t.Helper()
	mgr := coresub.NewManager(fleet.stores.Subscriptions, 2, 16, coresub.WithDestinationPolicy(coresub.DestinationPolicy{AllowLoopback: true}))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { mgr.Start(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	h, _ := assembly.BuildProtocolRouter(assembly.RouterConfig{}, fleet.stores, gateTestPolicy(), "serverSFDI", "serverLFDI", mgr)
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv
}

// With the router's subscriber check installed (pairing withholds included), a
// manager deleting a managed device (#1101) still tells the manager's
// subscription, and nobody else: the managed device's own subscription stays
// withheld while it is paired, and an unrelated device gets nothing. The
// recipients are decided while the record is readable, because after the
// delete the check refuses everyone.
func TestEndDeviceDeleted_NoticeReachesWhoCouldReadTheRecord(t *testing.T) {
	t.Parallel()
	fleet := newManagementFleet(t)
	sink, base := newNoticeSink(t)
	seedDeletedNoticeSubs(t, fleet.stores, base, map[string][2]string{
		"paired":    {victimID, "/edev/" + victimID},
		"manager":   {managerID, "/edev/" + victimID},
		"unrelated": {unmanagedID, "/edev/" + victimID},
		"othermgr":  {callerID, "/edev/" + victimID},
	})
	srv := deletedNoticeRouter(t, fleet)

	if status, raw := gateRequest(t, srv, http.MethodDelete, "/edev/"+victimID, managerLFDI, ""); status != http.StatusNoContent {
		t.Fatalf("manager DELETE: status %d, want 204; body=%q", status, raw)
	}

	assertDeletedNotice(t, sink, "manager", victimID)
	time.Sleep(200 * time.Millisecond)
	for _, name := range []string{"paired", "unrelated", "othermgr"} {
		if got := sink.got("/" + name); len(got) != 0 {
			t.Errorf("subscription %q received %+v, want nothing", name, got)
		}
	}
}

// An unpaired device deleting itself is told on its own subscription.
func TestEndDeviceDeleted_UnpairedDeviceHearsItsOwnDeletion(t *testing.T) {
	t.Parallel()
	fleet := newManagementFleet(t)
	sink, base := newNoticeSink(t)
	seedDeletedNoticeSubs(t, fleet.stores, base, map[string][2]string{
		"own":       {unmanagedID, "/edev/" + unmanagedID},
		"unrelated": {callerID, "/edev/" + unmanagedID},
	})
	srv := deletedNoticeRouter(t, fleet)

	if status, raw := gateRequest(t, srv, http.MethodDelete, "/edev/"+unmanagedID, unmanagedLFDI, ""); status != http.StatusNoContent {
		t.Fatalf("self DELETE: status %d, want 204; body=%q", status, raw)
	}

	assertDeletedNotice(t, sink, "own", unmanagedID)
	time.Sleep(200 * time.Millisecond)
	if got := sink.got("/unrelated"); len(got) != 0 {
		t.Errorf("unrelated subscription received %+v, want nothing", got)
	}
}

func assertDeletedNotice(t *testing.T, sink *noticeSink, name, edevID string) {
	t.Helper()
	got := sink.waitFor("/" + name)
	if len(got) != 1 {
		t.Errorf("subscription %q received %d notifications, want 1", name, len(got))
		return
	}
	if got[0].Status != sep2.NotificationStatusCanceledDeleted {
		t.Errorf("subscription %q: Status = %d, want %d", name, got[0].Status, sep2.NotificationStatusCanceledDeleted)
	}
	if got[0].SubscribedResource != "/edev/"+edevID || got[0].Resource.Href != "/edev/"+edevID {
		t.Errorf("subscription %q: SubscribedResource %q, Resource.Href %q, want /edev/%s", name, got[0].SubscribedResource, got[0].Resource.Href, edevID)
	}
}

// A delete the gate refuses sends no notice to anyone.
func TestEndDeviceDeleted_RefusedDeleteSendsNothing(t *testing.T) {
	t.Parallel()
	fleet := newManagementFleet(t)
	sink, base := newNoticeSink(t)
	seedDeletedNoticeSubs(t, fleet.stores, base, map[string][2]string{
		"paired":  {victimID, "/edev/" + victimID},
		"manager": {managerID, "/edev/" + victimID},
	})
	srv := deletedNoticeRouter(t, fleet)

	if status, _ := gateRequest(t, srv, http.MethodDelete, "/edev/"+victimID, otherManagerLFDI, ""); status != http.StatusForbidden {
		t.Fatalf("other manager DELETE: status %d, want 403", status)
	}
	time.Sleep(300 * time.Millisecond)
	for _, name := range []string{"paired", "manager"} {
		if got := sink.got("/" + name); len(got) != 0 {
			t.Errorf("subscription %q received %+v after a refused delete, want nothing", name, got)
		}
	}
}
