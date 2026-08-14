package response_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	coreresponse "github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/handlers/response"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

// TestHrefsAgreeOnTheirShape pins the three href builders against each other.
// The Location header and the resource's own Href are minted from MemberHref
// and must nest inside the list ListHref returns, which must nest inside the
// set SetHref returns. A client walks exactly that chain: ResponseSet ->
// ResponseListLink -> POST -> Location.
func TestHrefsAgreeOnTheirShape(t *testing.T) {
	t.Parallel()

	if got, want := coreresponse.SetHref("7"), "/rsps/7"; got != want {
		t.Errorf("SetHref = %q, want %q", got, want)
	}
	if got, want := coreresponse.ListHref("7"), "/rsps/7/rsp"; got != want {
		t.Errorf("ListHref = %q, want %q", got, want)
	}
	if got, want := coreresponse.MemberHref("7", "rsp-1"), "/rsps/7/rsp/rsp-1"; got != want {
		t.Errorf("MemberHref = %q, want %q", got, want)
	}
}

// TestDefaultSetIsSelfConsistent asserts the seeded set carries the identity
// and the link a client needs. A ResponseSet with no ResponseListLink is a
// dead end: the client can read the set and still not know where to post.
func TestDefaultSetIsSelfConsistent(t *testing.T) {
	t.Parallel()

	set := coreresponse.DefaultSet()
	if set.Href != coreresponse.SetHref(coreresponse.DefaultSetID) {
		t.Errorf("Href = %q, want %q", set.Href, coreresponse.SetHref(coreresponse.DefaultSetID))
	}
	if set.MRID != coreresponse.DefaultSetMRID {
		t.Errorf("MRID = %q, want %q", set.MRID, coreresponse.DefaultSetMRID)
	}
	if len(set.MRID) != 32 {
		t.Errorf("MRID %q is %d characters; mRIDType is hexBinary128, which is 32", set.MRID, len(set.MRID))
	}
	if set.ResponseListLink == nil {
		t.Fatal("ResponseListLink is nil")
	}
	if set.ResponseListLink.Href != coreresponse.ListHref(coreresponse.DefaultSetID) {
		t.Errorf("ResponseListLink.Href = %q, want %q",
			set.ResponseListLink.Href, coreresponse.ListHref(coreresponse.DefaultSetID))
	}
}

// TestSeedDefaultSetIsIdempotent asserts a second seed is a no-op rather than
// a duplicate or an error. BuildProtocolRouter seeds on every call, and a
// consumer that builds two routers over one store must not end up with two
// sets, because the ResponseSetList's all count is what a client pages by.
func TestSeedDefaultSetIsIdempotent(t *testing.T) {
	t.Parallel()

	sets := memory.NewStore[sep2.ResponseSet]()
	ctx := context.Background()

	for i := range 3 {
		if err := coreresponse.SeedDefaultSet(ctx, sets); err != nil {
			t.Fatalf("seed %d: %v", i, err)
		}
	}

	count, err := sets.Count(ctx)
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 1 {
		t.Errorf("store holds %d ResponseSet(s) after three seeds, want 1", count)
	}
}

// TestSeedDefaultSetYieldsToAConsumersOwnSet asserts seeding does not overwrite
// a set already stored under the same id.
//
// The mRID is the client-visible identity of the channel, so replacing an
// operator's set with ours would silently change what a client believes it is
// posting into, which is worse than the seeding not happening at all.
func TestSeedDefaultSetYieldsToAConsumersOwnSet(t *testing.T) {
	t.Parallel()

	sets := memory.NewStore[sep2.ResponseSet]()
	ctx := context.Background()

	const consumerMRID = "FFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFF"
	own := sep2.ResponseSet{MRID: consumerMRID, Description: "operator set"}
	own.Href = coreresponse.SetHref(coreresponse.DefaultSetID)
	if err := sets.Create(ctx, coreresponse.DefaultSetID, own); err != nil {
		t.Fatalf("create consumer set: %v", err)
	}

	if err := coreresponse.SeedDefaultSet(ctx, sets); err != nil {
		t.Fatalf("seed: %v", err)
	}

	got, err := sets.Get(ctx, coreresponse.DefaultSetID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.MRID != consumerMRID {
		t.Errorf("seeding replaced the consumer's set: MRID = %q, want %q", got.MRID, consumerMRID)
	}
}

// TestSeedDefaultSetToleratesANilStore covers the branch a consumer that wired
// no ResponseSets store takes. Seeding is called before the nil check that
// guards the routes in some orderings, so it must not panic.
func TestSeedDefaultSetToleratesANilStore(t *testing.T) {
	t.Parallel()

	if err := coreresponse.SeedDefaultSet(context.Background(), nil); err != nil {
		t.Errorf("SeedDefaultSet(nil) = %v, want nil", err)
	}
}

// TestHandleResponseSet covers the read route's three outcomes. A miss is a
// 404 and never a zero-valued ResponseSet: a client that parsed one would read
// an empty ResponseListLink and post its acknowledgements nowhere.
func TestHandleResponseSet(t *testing.T) {
	t.Parallel()

	sets := memory.NewStore[sep2.ResponseSet]()
	if err := coreresponse.SeedDefaultSet(context.Background(), sets); err != nil {
		t.Fatalf("seed: %v", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/rsps/{rspsId}", coreresponse.HandleResponseSet(sets))

	cases := []struct {
		name   string
		method string
		path   string
		want   int
	}{
		{"seeded set", http.MethodGet, "/rsps/" + coreresponse.DefaultSetID, http.StatusOK},
		{"absent set", http.MethodGet, "/rsps/nope", http.StatusNotFound},
		{"write refused", http.MethodPut, "/rsps/" + coreresponse.DefaultSetID, http.StatusMethodNotAllowed},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, httptest.NewRequest(tc.method, tc.path, nil))
			if w.Code != tc.want {
				t.Errorf("%s %s = %d, want %d (body: %s)", tc.method, tc.path, w.Code, tc.want, w.Body.String())
			}
		})
	}
}

// TestHandleResponse covers the member route, including the scope boundary: a
// response stored under one set must not be readable through another set's
// path, or the ResponseSet stops being a channel at all.
func TestHandleResponse(t *testing.T) {
	t.Parallel()

	responses := memory.NewScopedStore[sep2.Response]()
	stored := sep2.Response{Subject: "0123456789ABCDEF0123456789ABCDEF"}
	stored.Href = coreresponse.MemberHref("setA", "rsp-1")
	if err := responses.Create(context.Background(), "setA", "rsp-1", stored); err != nil {
		t.Fatalf("create: %v", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/rsps/{rspsId}/rsp/{rspId}", coreresponse.HandleResponse(responses))

	cases := []struct {
		name   string
		method string
		path   string
		want   int
	}{
		{"stored response", http.MethodGet, "/rsps/setA/rsp/rsp-1", http.StatusOK},
		{"absent response", http.MethodGet, "/rsps/setA/rsp/rsp-9", http.StatusNotFound},
		{"other set's path", http.MethodGet, "/rsps/setB/rsp/rsp-1", http.StatusNotFound},
		{"write refused", http.MethodDelete, "/rsps/setA/rsp/rsp-1", http.StatusMethodNotAllowed},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, httptest.NewRequest(tc.method, tc.path, nil))
			if w.Code != tc.want {
				t.Errorf("%s %s = %d, want %d (body: %s)", tc.method, tc.path, w.Code, tc.want, w.Body.String())
			}
		})
	}

	// The served document has to carry the subject: a Response the client
	// cannot match back to its event is not usable evidence of anything.
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/rsps/setA/rsp/rsp-1", nil))
	if body := w.Body.String(); !strings.Contains(body, "<subject>"+stored.Subject+"</subject>") {
		t.Errorf("served Response omits the subject:\n%s", body)
	}
	if _, err := responses.Get(context.Background(), "setA", "rsp-1"); err != nil {
		t.Errorf("serving mutated or removed the stored response: %v", err)
	}
}

// compile-time assertion that the new subtypes satisfy the store's Copier
// contract, the same way every other stored resource does.
var (
	_ store.Copier[sep2.PriceResponse]                   = sep2.PriceResponse{}
	_ store.Copier[sep2.TextResponse]                    = sep2.TextResponse{}
	_ store.Copier[sep2.FlowReservationResponseResponse] = sep2.FlowReservationResponseResponse{}
)
