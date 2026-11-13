package assembly_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/assembly"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
)

// subResourcePuts holds one body per DER sub-resource, each carrying a
// client-chosen href naming another device's DER.
var subResourcePuts = []struct {
	suffix string
	body   string
	count  func(*testing.T, *derStores, string) uint32
}{
	{"dercap", `<DERCapability xmlns="urn:ieee:std:2030.5:ns" href="/edev/9/der/1/dercap"><rtgMaxW><multiplier>0</multiplier><value>1000</value></rtgMaxW></DERCapability>`,
		func(t *testing.T, s *derStores, p string) uint32 { return countUnder(t, s.caps, p) }},
	{"derg", `<DERSettings xmlns="urn:ieee:std:2030.5:ns" href="/edev/9/der/1/derg"><updatedTime>1604963587</updatedTime></DERSettings>`,
		func(t *testing.T, s *derStores, p string) uint32 { return countUnder(t, s.settings, p) }},
	{"ders", `<DERStatus xmlns="urn:ieee:std:2030.5:ns" href="/edev/9/der/1/ders"><readingTime>1604963587</readingTime></DERStatus>`,
		func(t *testing.T, s *derStores, p string) uint32 { return countUnder(t, s.statuses, p) }},
	{"dera", `<DERAvailability xmlns="urn:ieee:std:2030.5:ns" href="/edev/9/der/1/dera"><readingTime>1604963587</readingTime></DERAvailability>`,
		func(t *testing.T, s *derStores, p string) uint32 { return countUnder(t, s.avails, p) }},
}

type derStores struct {
	caps     store.ScopedReader[sep2.DERCapability]
	settings store.ScopedReader[sep2.DERSettings]
	statuses store.ScopedReader[sep2.DERStatus]
	avails   store.ScopedReader[sep2.DERAvailability]
}

func countUnder[T store.Copier[T]](t *testing.T, s store.ScopedReader[T], parent string) uint32 {
	t.Helper()
	n, err := s.Count(context.Background(), parent)
	if err != nil {
		t.Fatalf("Count %q: %v", parent, err)
	}
	return n
}

func putSub(t *testing.T, base, path, body string) int {
	t.Helper()
	req, err := http.NewRequest(http.MethodPut, base+path, strings.NewReader(body))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PUT %s: %v", path, err)
	}
	_ = resp.Body.Close()
	return resp.StatusCode
}

// A PUT under a DER that is not stored answers 404 and leaves every sub-resource
// store empty, for the DER-missing, DER-never-seeded and traversal-shaped ids.
func TestDERSubResourcePut_MissingDERIs404AndStoresNothing(t *testing.T) {
	for _, sr := range subResourcePuts {
		for _, derID := range []string{"99", "..%2F..%2Fd9%2Fdstat"} {
			t.Run(sr.suffix+"/"+derID, func(t *testing.T) {
				srv, stores := derInstanceServer(t)
				seedDER(t, stores, "7", "1")

				got := putSub(t, srv.URL, "/edev/7/der/"+derID+"/"+sr.suffix, sr.body)
				if got != http.StatusNotFound {
					t.Fatalf("PUT status = %d, want 404", got)
				}

				ds := &derStores{stores.DERCapabilities, stores.DERSettings, stores.DERStatuses, stores.DERAvailabilities}
				for _, parent := range []string{"7/99", "7/../../d9/dstat"} {
					if n := sr.count(t, ds, parent); n != 0 {
						t.Errorf("%d record(s) stored under %q, want 0", n, parent)
					}
				}
			})
		}
	}
}

// The stored href comes from the request path on the create branch and on the
// update branch, and the first write under an existing DER still creates the
// singleton.
func TestDERSubResourcePut_StoredHrefComesFromThePath(t *testing.T) {
	for _, sr := range subResourcePuts {
		t.Run(sr.suffix, func(t *testing.T) {
			srv, stores := derInstanceServer(t)
			seedDER(t, stores, "7", "1")

			path := "/edev/7/der/1/" + sr.suffix
			foreign := strings.Replace(sr.body, "/edev/9/der/1/", "/edev/8/der/2/", 1)
			for i, body := range []string{sr.body, foreign} {
				if got := putSub(t, srv.URL, path, body); got != http.StatusNoContent {
					t.Fatalf("PUT %d status = %d, want 204", i+1, got)
				}

				resp, err := srv.Client().Get(srv.URL + path)
				if err != nil {
					t.Fatalf("GET %s: %v", path, err)
				}
				var got struct {
					Href string `xml:"href,attr"`
				}
				decodeXML(t, resp, &got)
				if got.Href != path {
					t.Errorf("after PUT %d served href = %q, want %q", i+1, got.Href, path)
				}
			}
		})
	}
}

// A GET under a DER that is not stored answers 404 like the PUT does, not an
// empty shell with an href to a DER that does not exist. Control: under a
// stored DER the same GET still serves the default shell.
func TestDERSubResourceGet_MissingDERIs404(t *testing.T) {
	for _, sr := range subResourcePuts {
		t.Run(sr.suffix, func(t *testing.T) {
			srv, stores := derInstanceServer(t)
			seedDER(t, stores, "7", "1")

			resp, err := srv.Client().Get(srv.URL + "/edev/7/der/1/" + sr.suffix)
			if err != nil {
				t.Fatalf("GET control: %v", err)
			}
			_ = resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("control GET under a stored DER = %d, want 200", resp.StatusCode)
			}

			resp, err = srv.Client().Get(srv.URL + "/edev/7/der/99/" + sr.suffix)
			if err != nil {
				t.Fatalf("GET: %v", err)
			}
			_ = resp.Body.Close()
			if resp.StatusCode != http.StatusNotFound {
				t.Errorf("GET under a missing DER = %d, want 404", resp.StatusCode)
			}
		})
	}
}

// holdingDERs pauses the first Get that succeeds after arm, once released it
// lets the caller continue, so a test can land a DELETE between a handler's
// parent check and its write.
type holdingDERs struct {
	store.ScopedStore[sep2.DER]
	mu      sync.Mutex
	armed   bool
	passed  chan struct{}
	release chan struct{}
}

func (h *holdingDERs) arm() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.armed = true
}

func (h *holdingDERs) Get(ctx context.Context, parentID, id string) (sep2.DER, error) {
	d, err := h.ScopedStore.Get(ctx, parentID, id)
	h.mu.Lock()
	hold := h.armed && err == nil
	if hold {
		h.armed = false
	}
	h.mu.Unlock()
	if hold {
		close(h.passed)
		<-h.release
	}
	return d, err
}

// DeleteParent keeps the cascade capability the wrapped store has, which the
// EndDevice delete requires of every family it removes.
func (h *holdingDERs) DeleteParent(ctx context.Context, parentID string) (uint32, error) {
	return store.CascadeParent(ctx, h.ScopedStore, parentID)
}

// A DELETE of the EndDevice landing between a PUT's parent check and its write
// must not leave the singleton behind for a device later registered at the same
// key: the PUT answers 404 and nothing is stored.
func TestDERSubResourcePut_DeleteBetweenCheckAndWriteStoresNothing(t *testing.T) {
	for _, sr := range subResourcePuts {
		t.Run(sr.suffix, func(t *testing.T) {
			stores := testStores()
			seedOwnedDevices(t, stores.EndDevices, "e1")
			hold := &holdingDERs{
				ScopedStore: stores.DERs,
				passed:      make(chan struct{}),
				release:     make(chan struct{}),
			}
			stores.DERs = hold
			seedDER(t, stores, "e1", "1")
			handler, _ := assembly.BuildProtocolRouter(assembly.RouterConfig{}, stores, testAuthPolicy(), "serverSFDI", "serverLFDI", nil)
			srv := httptest.NewServer(handler)
			t.Cleanup(srv.Close)

			hold.arm()
			status := make(chan int, 1)
			go func() { status <- putSub(t, srv.URL, "/edev/e1/der/1/"+sr.suffix, sr.body) }()
			<-hold.passed

			if got, _ := edevDelRequest(t, srv, http.MethodDelete, "/edev/e1"); got != http.StatusNoContent {
				close(hold.release)
				t.Fatalf("DELETE /edev/e1 = %d, want 204", got)
			}
			close(hold.release)

			if got := <-status; got != http.StatusNotFound {
				t.Errorf("PUT status = %d, want 404", got)
			}
			ds := &derStores{stores.DERCapabilities, stores.DERSettings, stores.DERStatuses, stores.DERAvailabilities}
			if n := sr.count(t, ds, "e1/1"); n != 0 {
				t.Errorf("%d record(s) stored under e1/1 after the device was deleted, want 0", n)
			}
		})
	}
}
