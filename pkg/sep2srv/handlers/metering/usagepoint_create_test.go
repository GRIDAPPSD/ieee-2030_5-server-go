package metering_test

import (
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/handlers/metering"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

// POST /upt had the same shape of defect already fixed on POST /mup: the
// client-supplied mRID was used as the global store key and went verbatim into
// Resource.Href, MeterReadingListLink.Href, and the Location header, and the
// ErrAlreadyExists branch discarded the error from its follow-up Get.
//
// Annex A.4.4.1 marks POST /upt Optional, so the server is not obliged to
// offer it; it does offer it, so it has to be correct.

// derivedUsagePointHrefRE is the exact shape a minted UsagePoint href may
// take: the literal prefix plus exactly 32 uppercase hex characters. Anchored
// at both ends, so any client-controlled residue in the URI fails it.
var derivedUsagePointHrefRE = regexp.MustCompile(`^/upt/[0-9A-F]{32}$`)

// usagePointHrefLen is the constant length of every minted UsagePoint href:
// len("/upt/") + 32. It matches the 37 bytes MirrorHref is bounded to, which
// is the point: both are values this server hands to a client that parses
// Location into a fixed 127-byte buffer without a length guard.
const usagePointHrefLen = 37

func postUsagePoint(t *testing.T, mux *http.ServeMux, mrid string) *httptest.ResponseRecorder {
	t.Helper()
	body, err := xml.Marshal(&sep2.UsagePoint{MRID: mrid})
	if err != nil {
		t.Fatalf("marshal UsagePoint: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/upt", bytes.NewReader(body))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	return w
}

// TestHandleCreateUsagePoint_LocationIsBounded asserts that no client-supplied
// mRID, however long or however shaped, reaches any URI this handler mints.
//
// The two concrete failures being locked out: a 4000-character mRID produced a
// 4005-character Location, and an mRID of "../../etc/passwd" produced a
// Location carrying path traversal. Both went out to a client that parses
// Location into a 127-byte buffer with no length guard.
func TestHandleCreateUsagePoint_LocationIsBounded(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		mrid string
	}{
		{name: "ordinary hex mRID", mrid: "ABCDEF0123456789ABCDEF0123456789"},
		{name: "4000 characters", mrid: strings.Repeat("A", 4000)},
		{name: "path traversal", mrid: "../../etc/passwd"},
		{name: "absolute path", mrid: "/mup/VICTIM"},
		{name: "query and fragment", mrid: "X?a=b#frag"},
		{name: "percent encoded traversal", mrid: "%2e%2e%2fetc%2fpasswd"},
		{name: "spaces and tabs", mrid: "a b\tc"},
		{name: "absent", mrid: ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := memory.NewStore[sep2.UsagePoint]()
			mux := http.NewServeMux()
			mux.HandleFunc("POST /upt", metering.HandleCreateUsagePoint(s))

			w := postUsagePoint(t, mux, tc.mrid)
			if w.Code != http.StatusCreated {
				t.Fatalf("status = %d, want 201; body = %s", w.Code, w.Body.String())
			}

			loc := w.Header().Get("Location")
			if len(loc) != usagePointHrefLen {
				t.Errorf("Location length = %d, want %d; Location = %q", len(loc), usagePointHrefLen, loc)
			}
			if !derivedUsagePointHrefRE.MatchString(loc) {
				t.Errorf("Location = %q, want a server-derived %v", loc, derivedUsagePointHrefRE)
			}
			if tc.mrid != "" && strings.Contains(loc, tc.mrid) {
				t.Errorf("Location = %q carries the client mRID verbatim", loc)
			}

			// The same bound holds on every URI in the served body, not only
			// on the header.
			var served sep2.UsagePoint
			if err := xml.Unmarshal(w.Body.Bytes(), &served); err != nil {
				t.Fatalf("unmarshal served UsagePoint: %v", err)
			}
			if served.Href != loc {
				t.Errorf("served Href = %q, want %q matching Location", served.Href, loc)
			}
			if served.MeterReadingListLink == nil {
				t.Fatal("served MeterReadingListLink is nil, want set")
			}
			if want := loc + "/mr"; served.MeterReadingListLink.Href != want {
				t.Errorf("served MeterReadingListLink.Href = %q, want %q", served.MeterReadingListLink.Href, want)
			}

			// The mRID itself is preserved on the resource: it is bounded out
			// of the URI, not discarded from the record.
			if served.MRID != tc.mrid {
				t.Errorf("served MRID = %q, want %q preserved", served.MRID, tc.mrid)
			}

			// The derived id is the actual store key, so the URI the client is
			// handed resolves.
			id := strings.TrimPrefix(loc, "/upt/")
			stored, err := s.Get(context.Background(), id)
			if err != nil {
				t.Fatalf("get stored UsagePoint at the id Location names (%q): %v", id, err)
			}
			if stored.Href != loc {
				t.Errorf("stored Href = %q, want %q", stored.Href, loc)
			}
			if stored.MRID != tc.mrid {
				t.Errorf("stored MRID = %q, want %q preserved", stored.MRID, tc.mrid)
			}
		})
	}
}

// TestHandleCreateUsagePoint_DerivationIsDeterministicAndDistinct asserts the
// two properties the derived id has to hold at once: the same mRID always
// lands on the same id (so a re-POST is idempotent rather than duplicating),
// and different mRIDs land on different ids (so bounding does not merge two
// clients' resources into one).
func TestHandleCreateUsagePoint_DerivationIsDeterministicAndDistinct(t *testing.T) {
	t.Parallel()

	if a, b := metering.UsagePointStoreID("UPT001"), metering.UsagePointStoreID("UPT001"); a != b {
		t.Errorf("UsagePointStoreID is not deterministic: %q != %q", a, b)
	}

	seen := make(map[string]string)
	for _, mrid := range []string{"UPT001", "UPT002", "", strings.Repeat("A", 4000), "../../etc/passwd"} {
		id := metering.UsagePointStoreID(mrid)
		if len(id) != 32 {
			t.Errorf("UsagePointStoreID(%.20q) length = %d, want 32", mrid, len(id))
		}
		if prev, ok := seen[id]; ok {
			t.Errorf("UsagePointStoreID collision: %.20q and %.20q both derive %q", mrid, prev, id)
		}
		seen[id] = mrid
	}
}

// TestHandleCreateUsagePoint_RepostServesExistingWithMintedLocation covers the
// ErrAlreadyExists branch on its normal path: a second POST of the same mRID
// resolves to the same derived id, is served the stored record, and gets a
// Location minted from that id rather than echoed out of storage.
//
// The Location is deliberately NOT read back from the stored record's Href.
// A consumer may seed this store directly with any Href it likes, and this
// handler will not hand an arbitrary stored string to a client as a URI.
func TestHandleCreateUsagePoint_RepostServesExistingWithMintedLocation(t *testing.T) {
	t.Parallel()
	s := memory.NewStore[sep2.UsagePoint]()
	mux := http.NewServeMux()
	mux.HandleFunc("POST /upt", metering.HandleCreateUsagePoint(s))

	const mrid = "UPT_REPOST"
	wantHref := metering.UsagePointHref(metering.UsagePointStoreID(mrid))

	first := postUsagePoint(t, mux, mrid)
	if first.Code != http.StatusCreated {
		t.Fatalf("first POST status = %d, want 201", first.Code)
	}

	// Plant a hostile Href on the stored record, standing in for a
	// directly-seeded consumer record.
	stored, err := s.Get(context.Background(), metering.UsagePointStoreID(mrid))
	if err != nil {
		t.Fatalf("get stored UsagePoint: %v", err)
	}
	stored.Href = "/upt/" + strings.Repeat("Z", 4000)
	if err := s.Update(context.Background(), metering.UsagePointStoreID(mrid), stored); err != nil {
		t.Fatalf("update stored UsagePoint: %v", err)
	}

	second := postUsagePoint(t, mux, mrid)
	if second.Code != http.StatusOK {
		t.Fatalf("re-POST status = %d, want 200; body = %s", second.Code, second.Body.String())
	}
	loc := second.Header().Get("Location")
	if loc != wantHref {
		t.Errorf("re-POST Location = %q, want %q minted from the derived id", loc, wantHref)
	}
	if len(loc) != usagePointHrefLen {
		t.Errorf("re-POST Location length = %d, want %d", len(loc), usagePointHrefLen)
	}

	// Exactly one record, not two: the derivation is what makes the re-POST
	// idempotent.
	count, err := s.Count(context.Background())
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 1 {
		t.Errorf("stored UsagePoint count = %d, want 1", count)
	}
}

// TestHandleCreateUsagePoint_NoMRIDPostsDoNotCollide asserts that bounding did
// not turn "no mRID" into a single shared key. Hashing the empty string would
// derive one id for every mRID-less POST, so the second such POST would be
// served the first one's record.
func TestHandleCreateUsagePoint_NoMRIDPostsDoNotCollide(t *testing.T) {
	t.Parallel()
	s := memory.NewStore[sep2.UsagePoint]()
	mux := http.NewServeMux()
	mux.HandleFunc("POST /upt", metering.HandleCreateUsagePoint(s))

	first := postUsagePoint(t, mux, "")
	second := postUsagePoint(t, mux, "")

	if first.Code != http.StatusCreated || second.Code != http.StatusCreated {
		t.Fatalf("statuses = %d, %d, want 201 and 201", first.Code, second.Code)
	}
	locA := first.Header().Get("Location")
	locB := second.Header().Get("Location")
	if locA == locB {
		t.Errorf("two mRID-less POSTs both got Location %q: they share one record", locA)
	}

	count, err := s.Count(context.Background())
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 2 {
		t.Errorf("stored UsagePoint count = %d, want 2", count)
	}

	// A synthesized id can never coincide with an id a client could reach by
	// choosing an mRID, because the two derivations use disjoint domain tags.
	for _, loc := range []string{locA, locB} {
		id := strings.TrimPrefix(loc, "/upt/")
		if metering.UsagePointStoreID("") == id {
			t.Errorf("synthesized id %q equals the id derived from an empty mRID", id)
		}
	}
}

// raceLossUsagePointStore reports ErrAlreadyExists from Create and then fails
// the follow-up Get, which is the exact interleaving the handler's
// ErrAlreadyExists branch has to survive: another writer created the record
// between the two calls and deleted it again, or the store failed outright.
type raceLossUsagePointStore struct {
	store.ResourceStore[sep2.UsagePoint]
}

func (raceLossUsagePointStore) Create(context.Context, string, sep2.UsagePoint) error {
	return store.ErrAlreadyExists
}

func (raceLossUsagePointStore) Get(context.Context, string) (sep2.UsagePoint, error) {
	return sep2.UsagePoint{}, store.ErrNotFound
}

// TestHandleCreateUsagePoint_RaceLossSurfacesError asserts the swallowed error
// is gone.
//
// The old code was `existing, _ := uptStore.Get(...)`, so this interleaving
// produced a 200 carrying a zero-value UsagePoint and Location: "". That is
// silent data loss dressed as success, and the empty Location specifically is
// what the EPRI reference client strlen()s with no guard before dereferencing
// the NULL its failed URI parse returns.
//
// The assertions are on the served bytes, not only the status: a 200 whose
// body is an empty UsagePoint is the exact failure being locked out, so the
// test checks that nothing resembling a resource was served at all.
func TestHandleCreateUsagePoint_RaceLossSurfacesError(t *testing.T) {
	t.Parallel()
	s := raceLossUsagePointStore{ResourceStore: memory.NewStore[sep2.UsagePoint]()}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /upt", metering.HandleCreateUsagePoint(s))

	w := postUsagePoint(t, mux, "UPT_RACE")

	if w.Code == http.StatusOK {
		t.Fatalf("status = 200 on a lost race: a zero-value UsagePoint was served as success; body = %q", w.Body.String())
	}
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", w.Code)
	}
	if loc, ok := w.Header()["Location"]; ok {
		t.Errorf("Location header present on the error response: %q", loc)
	}
	if body := w.Body.String(); strings.Contains(body, "<") {
		t.Errorf("error body carries XML markup, want a plain-text reason: %q", body)
	}
}

// TestUsagePointItemMethodsNotMounted is the Annex A.4.4.2 check: PUT and POST
// on /upt/{id1} are marked Error, so neither may be reachable. This asserts the
// current state rather than changing it.
//
// Both halves matter. The mux must not route those methods to any handler, and
// the handler behind GET /upt/{uptId} must refuse them on its own, so that a
// consumer mounting it at a bare pattern does not silently acquire a write
// path the standard forbids.
func TestUsagePointItemMethodsNotMounted(t *testing.T) {
	t.Parallel()
	s := memory.NewStore[sep2.UsagePoint]()
	if err := s.Create(context.Background(), "upt1", sep2.UsagePoint{
		SubscribableResource: sep2.SubscribableResource{Resource: sep2.Resource{Href: "/upt/upt1"}},
		MRID:                 "ORIGINAL",
	}); err != nil {
		t.Fatalf("seed UsagePoint: %v", err)
	}

	// Mounted exactly as assembly.go mounts it: method-qualified GET only.
	mux := http.NewServeMux()
	mux.HandleFunc("GET /upt/{uptId}", metering.HandleUsagePoint(s))

	// And mounted method-agnostically, to prove the handler itself refuses
	// rather than relying on the pattern to do it.
	bare := http.NewServeMux()
	bare.HandleFunc("/upt/{uptId}", metering.HandleUsagePoint(s))

	body, err := xml.Marshal(&sep2.UsagePoint{MRID: "OVERWRITTEN"})
	if err != nil {
		t.Fatalf("marshal UsagePoint: %v", err)
	}

	for _, method := range []string{http.MethodPut, http.MethodPost} {
		for name, m := range map[string]*http.ServeMux{"method-qualified mount": mux, "bare mount": bare} {
			t.Run(fmt.Sprintf("%s %s", method, name), func(t *testing.T) {
				req := httptest.NewRequest(method, "/upt/upt1", bytes.NewReader(body))
				w := httptest.NewRecorder()
				m.ServeHTTP(w, req)

				if w.Code != http.StatusMethodNotAllowed {
					t.Errorf("%s /upt/{id1} status = %d, want 405 (A.4.4.2 marks it Error); body = %s",
						method, w.Code, w.Body.String())
				}
			})
		}
	}

	// The seeded record is untouched by any of those attempts.
	stored, err := s.Get(context.Background(), "upt1")
	if err != nil {
		t.Fatalf("get UsagePoint: %v", err)
	}
	if stored.MRID != "ORIGINAL" {
		t.Errorf("stored MRID = %q, want ORIGINAL: a forbidden method mutated the resource", stored.MRID)
	}
}
