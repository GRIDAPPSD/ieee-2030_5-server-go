package assembly_test

import (
	"context"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/assembly"
	coreder "github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/handlers/der"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
)

// The four DER lists are served in the order IEEE 2030.5-2018 Table 50
// specifies (section 4.6.1 makes it a SHALL), and paging addresses positions
// within that order. Every seed below is chosen so that the Table 50 order is
// neither the store's id order nor its reverse, so a list served in either
// fails.

const orderEdev = "e1"

// scrambledScoped is a backend that honours no ordering at all: it accepts any
// SortKey and returns its collection in reverse id order, which is a legal
// thing for a query planner to do and the case the contract has to survive.
type scrambledScoped[T store.Copier[T]] struct {
	store.ScopedStore[T]
}

func (s scrambledScoped[T]) List(ctx context.Context, parentID string, opts store.ListOptions) (store.ListResult[T], error) {
	opts.Sort = store.SortByIDAsc
	res, err := s.ScopedStore.List(ctx, parentID, opts)
	slices.Reverse(res.Items)
	return res, err
}

type scrambledFlat[T store.Copier[T]] struct {
	store.ResourceStore[T]
}

func (s scrambledFlat[T]) List(ctx context.Context, opts store.ListOptions) (store.ListResult[T], error) {
	opts.Sort = store.SortByIDAsc
	res, err := s.ResourceStore.List(ctx, opts)
	slices.Reverse(res.Items)
	return res, err
}

func seedOrderFixtures(t *testing.T, stores *assembly.Stores) {
	t.Helper()
	ctx := context.Background()
	seedOwnedDevices(t, stores.EndDevices, orderEdev)

	programs := []struct {
		id      string
		primacy uint8
	}{{"a", 2}, {"b", 0}, {"c", 1}, {"d", 0}, {"e", 2}}
	for _, p := range programs {
		prog := sep2.DERProgram{MRID: "0" + p.id, Primacy: p.primacy}
		prog.Href = coreder.DERProgramHref(orderEdev, "f1", p.id)
		if err := stores.DERPrograms.Create(ctx, orderEdev, p.id, prog); err != nil {
			t.Fatalf("seed DERProgram %s: %v", p.id, err)
		}
	}

	controls := []struct {
		id       string
		start    int64
		creation int64
	}{{"a", 300, 10}, {"b", 100, 10}, {"c", 100, 20}, {"d", 100, 20}, {"e", 200, 5}}
	for _, c := range controls {
		ctrl := sep2.DERControl{}
		ctrl.MRID = "0" + c.id
		ctrl.CreationTime = c.creation
		ctrl.Interval = &sep2.DateTimeInterval{Start: c.start, Duration: 60}
		ctrl.DERControlBase = &sep2.DERControlBase{}
		if err := stores.DERControls.Create(ctx, orderEdev+"/f1/p1", c.id, ctrl); err != nil {
			t.Fatalf("seed DERControl %s: %v", c.id, err)
		}
	}

	curves := []struct {
		id       string
		creation int64
	}{{"a", 100}, {"b", 300}, {"c", 200}, {"d", 300}, {"e", 100}}
	for _, c := range curves {
		curve := sep2.DERCurve{MRID: "0" + c.id, CreationTime: c.creation}
		if err := stores.DERCurves.Create(ctx, c.id, curve); err != nil {
			t.Fatalf("seed DERCurve %s: %v", c.id, err)
		}
	}

	ders := []struct{ id, leaf string }{{"a", "4"}, {"b", "2"}, {"c", "5"}, {"d", "1"}, {"e", "3"}}
	for _, d := range ders {
		der := sep2.DER{}
		der.Href = "/edev/" + orderEdev + "/der/" + d.leaf
		if err := stores.DERs.Create(ctx, orderEdev, d.id, der); err != nil {
			t.Fatalf("seed DER %s: %v", d.id, err)
		}
	}
}

func getList[L any](t *testing.T, srv *httptest.Server, path string) L {
	t.Helper()
	resp, err := srv.Client().Get(srv.URL + path)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	if resp.StatusCode != 200 {
		_ = resp.Body.Close()
		t.Fatalf("GET %s status = %d, want 200", path, resp.StatusCode)
	}
	var list L
	decodeXML(t, resp, &list)
	return list
}

// checkTable50 asserts the full lists and a middle page of each against the
// Table 50 order. The page proves ordering happens before paging: sorting
// only the page a store returned would serve the right members of the wrong
// page.
func checkTable50(t *testing.T, srv *httptest.Server) {
	t.Helper()
	derp := "/edev/" + orderEdev + "/fsa/f1/derp"
	derc := derp + "/p1/derc"
	der := "/edev/" + orderEdev + "/der"

	cases := []struct {
		name string
		path string
		got  func(path string) (all, results uint32, keys []string)
		want []string
	}{
		{"DERProgram primacy asc then mRID desc", derp, func(p string) (uint32, uint32, []string) {
			l := getList[sep2.DERProgramList](t, srv, p)
			var k []string
			for _, m := range l.DERProgram {
				k = append(k, m.MRID)
			}
			return l.All, l.Results, k
		}, []string{"0d", "0b", "0c", "0e", "0a"}},
		{"DERControl interval.start asc, creationTime desc, mRID desc", derc, func(p string) (uint32, uint32, []string) {
			l := getList[sep2.DERControlList](t, srv, p)
			var k []string
			for _, m := range l.DERControl {
				k = append(k, m.MRID)
			}
			return l.All, l.Results, k
		}, []string{"0d", "0c", "0b", "0e", "0a"}},
		{"DERCurve creationTime desc then mRID desc", "/dc", func(p string) (uint32, uint32, []string) {
			l := getList[sep2.DERCurveList](t, srv, p)
			var k []string
			for _, m := range l.DERCurve {
				k = append(k, m.MRID)
			}
			return l.All, l.Results, k
		}, []string{"0d", "0b", "0c", "0e", "0a"}},
		{"DER href asc", der, func(p string) (uint32, uint32, []string) {
			l := getList[sep2.DERList](t, srv, p)
			var k []string
			for _, m := range l.DER {
				k = append(k, m.Href)
			}
			return l.All, l.Results, k
		}, []string{der + "/1", der + "/2", der + "/3", der + "/4", der + "/5"}},
	}
	for _, tc := range cases {
		all, results, got := tc.got(tc.path + "?l=255")
		if all != 5 || results != 5 || !slices.Equal(got, tc.want) {
			t.Errorf("%s: all=%d results=%d order=%v, want 5/5 %v", tc.name, all, results, got, tc.want)
		}
		all, results, got = tc.got(tc.path + "?s=1&l=2")
		if want := tc.want[1:3]; all != 5 || results != 2 || !slices.Equal(got, want) {
			t.Errorf("%s page s=1 l=2: all=%d results=%d order=%v, want 5/2 %v", tc.name, all, results, got, want)
		}
	}
}

func TestListOrder_DERListsServeTable50Order(t *testing.T) {
	t.Parallel()
	stores := testStores()
	seedOrderFixtures(t, stores)
	handler, _ := assembly.BuildProtocolRouter(assembly.RouterConfig{}, stores, testAuthPolicy(), "serverSFDI", "serverLFDI", nil)
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	checkTable50(t, srv)
}

// The served order must not depend on what order the backend hands rows back
// in, or a durable store would satisfy every method signature while breaking
// section 4.6.1 with nothing failing.
func TestListOrder_ScrambledBackendStillServesTable50Order(t *testing.T) {
	t.Parallel()
	stores := testStores()
	seedOrderFixtures(t, stores)
	stores.DERPrograms = scrambledScoped[sep2.DERProgram]{stores.DERPrograms}
	stores.DERControls = scrambledScoped[sep2.DERControl]{stores.DERControls}
	stores.DERs = scrambledScoped[sep2.DER]{stores.DERs}
	stores.DERCurves = scrambledFlat[sep2.DERCurve]{stores.DERCurves}
	handler, _ := assembly.BuildProtocolRouter(assembly.RouterConfig{}, stores, testAuthPolicy(), "serverSFDI", "serverLFDI", nil)
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	checkTable50(t, srv)
}

// Lists outside Table 50 keep the id order they were served in before.
func TestListOrder_OtherListsKeepIDOrder(t *testing.T) {
	t.Parallel()
	stores := testStores()
	ctx := context.Background()
	for _, id := range []string{"b", "c", "a"} {
		if err := stores.MessagingPrograms.Create(ctx, id, sep2.MessagingProgram{MRID: "0" + id}); err != nil {
			t.Fatalf("seed MessagingProgram %s: %v", id, err)
		}
	}
	handler, _ := assembly.BuildProtocolRouter(assembly.RouterConfig{}, stores, testAuthPolicy(), "serverSFDI", "serverLFDI", nil)
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	l := getList[sep2.MessagingProgramList](t, srv, "/msg?l=255")
	var got []string
	for _, m := range l.MessagingProgram {
		got = append(got, m.MRID)
	}
	if want := []string{"0a", "0b", "0c"}; !slices.Equal(got, want) {
		t.Errorf("/msg order = %v, want %v", got, want)
	}
}
