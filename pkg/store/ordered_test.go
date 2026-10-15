package store_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/storetest"
)

// byNameDesc is an ordering that is neither id order nor its reverse once
// the seeds below are stored.
func byNameDesc(a, b storetest.Resource) int { return strings.Compare(b.Body, a.Body) }

// seed stores ids a..e with names chosen so that name-descending order is
// c, a, e, b, d.
var seedNames = map[string]string{"a": "4", "b": "2", "c": "5", "d": "1", "e": "3"}

func names(items []storetest.Resource) []string {
	var out []string
	for _, r := range items {
		out = append(out, r.Body)
	}
	return out
}

func TestOrdered_ListOrderingPagesAfterSorting(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	flat := memory.NewStore[storetest.Resource]()
	scoped := memory.NewScopedStore[storetest.Resource]()
	for id, n := range seedNames {
		if err := flat.Create(ctx, id, storetest.Resource{Body: n}); err != nil {
			t.Fatal(err)
		}
		if err := scoped.Create(ctx, "p", id, storetest.Resource{Body: n}); err != nil {
			t.Fatal(err)
		}
	}
	lists := map[string]func(store.ListOptions) (store.ListResult[storetest.Resource], error){
		"flat": func(o store.ListOptions) (store.ListResult[storetest.Resource], error) {
			return store.Ordered[storetest.Resource](flat, byNameDesc).List(ctx, o)
		},
		"scoped": func(o store.ListOptions) (store.ListResult[storetest.Resource], error) {
			return store.OrderedScoped[storetest.Resource](scoped, byNameDesc).List(ctx, "p", o)
		},
	}
	cases := []struct {
		name string
		opts store.ListOptions
		want []string
	}{
		{"whole list", store.ListOptions{Limit: 10}, []string{"5", "4", "3", "2", "1"}},
		{"middle page", store.ListOptions{Start: 1, Limit: 2}, []string{"4", "3"}},
		{"unbounded from an offset", store.ListOptions{Start: 3, Unbounded: true}, []string{"2", "1"}},
		{"start past the end", store.ListOptions{Start: 9, Limit: 2}, nil},
		{"limit zero", store.ListOptions{Limit: 0}, nil},
		{"after is ignored", store.ListOptions{After: "c", Limit: 2}, []string{"5", "4"}},
	}
	for shape, list := range lists {
		for _, tc := range cases {
			opts := tc.opts
			opts.Sort = store.SortByListOrdering
			res, err := list(opts)
			if err != nil {
				t.Fatalf("%s %s: %v", shape, tc.name, err)
			}
			got := names(res.Items)
			if res.All != 5 || res.Results != uint32(len(tc.want)) || !slices.Equal(got, tc.want) {
				t.Errorf("%s %s: all=%d results=%d names=%v, want 5/%d %v", shape, tc.name, res.All, res.Results, got, len(tc.want), tc.want)
			}
		}
		// Id orders still come from the store underneath.
		res, err := list(store.ListOptions{Limit: 10, Sort: store.SortByIDDesc})
		if err != nil {
			t.Fatalf("%s id-desc: %v", shape, err)
		}
		if got, want := names(res.Items), []string{"3", "1", "5", "2", "4"}; !slices.Equal(got, want) {
			t.Errorf("%s id-desc names = %v, want %v", shape, got, want)
		}
	}
}

func TestOrdered_GenericStoreRefusesListOrdering(t *testing.T) {
	t.Parallel()
	_, err := memory.NewStore[storetest.Resource]().List(context.Background(), store.ListOptions{Limit: 1, Sort: store.SortByListOrdering})
	if !errors.Is(err, store.ErrUnsupportedSort) {
		t.Fatalf("memory List with SortByListOrdering err = %v, want ErrUnsupportedSort", err)
	}
}

func TestOrdered_RefusesInvalidOptionsAndPropagatesFailure(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	inner := storetest.NewFake[storetest.Resource]()
	o := store.Ordered[storetest.Resource](inner, byNameDesc)
	_, err := o.List(ctx, store.ListOptions{Limit: 1, Unbounded: true, Sort: store.SortByListOrdering})
	if !errors.Is(err, store.ErrInvalidListOptions) {
		t.Errorf("Limit with Unbounded err = %v, want ErrInvalidListOptions", err)
	}

	fault := &storetest.Fault{}
	failing := store.Ordered(storetest.NewFaultyResourceStore[storetest.Resource](inner, fault), byNameDesc)
	fault.Arm(errors.New("backend down"))
	res, err := failing.List(ctx, store.ListOptions{Limit: 1, Sort: store.SortByListOrdering})
	if err == nil {
		t.Fatalf("failing backend: err = nil, result %+v; a failure must not read as an empty list", res)
	}
}

// partialStore answers an unbounded read with fewer items than its own All.
type partialStore struct {
	store.ResourceStore[storetest.Resource]
}

func (partialStore) List(context.Context, store.ListOptions) (store.ListResult[storetest.Resource], error) {
	return store.ListResult[storetest.Resource]{All: 3, Results: 1, Items: []storetest.Resource{{Body: "x"}}}, nil
}

func TestOrdered_RefusesToOrderAPartialCollection(t *testing.T) {
	t.Parallel()
	o := store.Ordered[storetest.Resource](partialStore{}, byNameDesc)
	res, err := o.List(context.Background(), store.ListOptions{Limit: 5, Sort: store.SortByListOrdering})
	if err == nil {
		t.Fatalf("partial unbounded read: err = nil, result %+v", res)
	}
}

func TestOrdered_ForwardingKeepsTheContract(t *testing.T) {
	t.Parallel()
	storetest.RunResourceStoreSuite(t, func(*testing.T) store.ResourceStore[storetest.Resource] {
		return store.Ordered[storetest.Resource](memory.NewStore[storetest.Resource](), byNameDesc)
	})
	storetest.RunScopedStoreSuite(t, func(*testing.T) store.ScopedStore[storetest.Resource] {
		return store.OrderedScoped[storetest.Resource](memory.NewScopedStore[storetest.Resource](), byNameDesc)
	})
}

func TestSortKeyString(t *testing.T) {
	t.Parallel()
	if got := store.SortByListOrdering.String(); got != "list-ordering" {
		t.Errorf("SortByListOrdering.String() = %q", got)
	}
}
