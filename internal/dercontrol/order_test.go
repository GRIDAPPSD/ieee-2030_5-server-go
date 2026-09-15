package dercontrol

import (
	"context"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/handlers/sep2time"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
)

// Acceptance criterion 6: the stored id orders admin-issued controls in
// IEEE 2030.5-2018 Table 50 DERControl order (interval.start ascending,
// creationTime descending, mRID descending) under the store's ascending
// id sort.

// TestSortableID_OrdersByStartThenCreationTimeDescThenMRIDDesc is a direct,
// deterministic unit test of the ordering scheme, independent of the wall
// clock: it asserts ascending string order of the ids equals Table 50
// order of the (start, creationTime, mRID) triples that produced them.
func TestSortableID_OrdersByStartThenCreationTimeDescThenMRIDDesc(t *testing.T) {
	type triple struct {
		start, creationTime int64
		mrid                string
	}
	// Deliberately out of Table 50 order; the ids' string order must sort
	// them back into it: earlier start first; for equal start, higher
	// creationTime first; for equal start and creationTime, higher mRID
	// first.
	in := []triple{
		{100, 5, "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA1"},
		{50, 9, "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA1"},
		{50, 9, "BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB1"},
		{50, 3, "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA1"},
	}
	wantOrder := []int{2, 1, 3, 0} // indices into `in`, Table 50 order

	ids := make([]string, len(in))
	for i, tr := range in {
		ids[i] = sortableID(tr.start, tr.creationTime, tr.mrid)
	}

	// Sort a copy of the indices by their id string and compare, rather
	// than sorting ids directly, so a failure names which input triple
	// landed in the wrong place.
	got := append([]int(nil), 0, 1, 2, 3)
	for i := 0; i < len(got); i++ {
		for j := i + 1; j < len(got); j++ {
			if ids[got[j]] < ids[got[i]] {
				got[i], got[j] = got[j], got[i]
			}
		}
	}
	for i := range wantOrder {
		if got[i] != wantOrder[i] {
			t.Fatalf("order[%d] = input#%d %+v, want input#%d %+v", i, got[i], in[got[i]], wantOrder[i], in[wantOrder[i]])
		}
	}
}

// TestIssue_TableFiftyOrder_ThreeAdminControls issues three controls with
// the same start (so ordering falls to creationTime), then lists the scope
// and asserts newest-creationTime-first, exercising Issue's real id
// assignment rather than sortableID in isolation.
func TestIssue_TableFiftyOrder_ThreeAdminControls(t *testing.T) {
	h := newHarness(Config{PEN: testPEN(1)})
	h.seedProgram(t, "dev1", "p1", controlListHref("dev1", "0", "p1"))

	start := sep2time.Now().Unix() + 100
	var mrids []string
	for i := 0; i < 3; i++ {
		res, err := issueWith(t, h, func(r *CreateRequest) { r.Start = &start })
		if err != nil {
			t.Fatalf("Issue() #%d error = %v", i, err)
		}
		mrids = append(mrids, res.Control.MRID)
	}

	list, err := h.controls.List(context.Background(), "dev1/0/p1", store.ListOptions{Unbounded: true, Sort: store.SortByIDAsc})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(list.Items) != 3 {
		t.Fatalf("List() returned %d items, want 3", len(list.Items))
	}
	// Ascending id order must read newest-creationTime-first: the third
	// Issue call has the highest creationTime (the per-scope ordering rule
	// forces strictly increasing values), so its mRID (mrids[2]) must lead.
	want := []string{mrids[2], mrids[1], mrids[0]}
	for i, item := range list.Items {
		if item.MRID != want[i] {
			t.Fatalf("List()[%d].MRID = %q, want %q (Table 50 order)", i, item.MRID, want[i])
		}
	}
}
