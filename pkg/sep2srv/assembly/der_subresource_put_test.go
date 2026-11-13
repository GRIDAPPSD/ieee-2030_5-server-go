package assembly_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
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

// The stored href comes from the request path, and the first write under an
// existing DER still creates the singleton.
func TestDERSubResourcePut_StoredHrefComesFromThePath(t *testing.T) {
	for _, sr := range subResourcePuts {
		t.Run(sr.suffix, func(t *testing.T) {
			srv, stores := derInstanceServer(t)
			seedDER(t, stores, "7", "1")

			path := "/edev/7/der/1/" + sr.suffix
			if got := putSub(t, srv.URL, path, sr.body); got != http.StatusNoContent {
				t.Fatalf("PUT status = %d, want 204", got)
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
				t.Errorf("served href = %q, want %q", got.Href, path)
			}
		})
	}
}
