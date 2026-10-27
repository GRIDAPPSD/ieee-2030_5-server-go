package handler_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/handler"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/handlers/sep2time"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

// #1157: GET /api/derms/grid-requests and the block cancel routes.

func (g *gridFixture) get() handler.GridRequestList {
	g.t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/derms/grid-requests", nil)
	w := httptest.NewRecorder()
	g.mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		g.t.Fatalf("list = %d %s", w.Code, w.Body.String())
	}
	var got handler.GridRequestList
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		g.t.Fatal(err)
	}
	return got
}

func (g *gridFixture) cancel(path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	g.mux.ServeHTTP(w, req)
	return w
}

// soon is a start far enough ahead that every share of a row reads
// scheduled, whatever second its creationTime was bumped to.
func soon() int64 { return sep2time.Now().Unix() + 3600 }

// block is row n of the only batch, read through the list route.
func (g *gridFixture) block(n int) handler.GridBlockView {
	g.t.Helper()
	l := g.get()
	if len(l.Batches) != 1 || n < 1 || n > len(l.Batches[0].Rows) {
		g.t.Fatalf("list = %+v, want one batch holding row %d", l, n)
	}
	return l.Batches[0].Rows[n-1]
}

// servedStatus is the eventStatus the Controls tab's device list serves for
// the control with mrid on dev.
func (g *gridFixture) servedStatus(dev, mrid string) string {
	g.t.Helper()
	w := g.dc.do(g.t, http.MethodGet, "/api/der/controls?device="+dev, "")
	var l handler.DERControlList
	if err := json.Unmarshal(w.Body.Bytes(), &l); err != nil || w.Code != http.StatusOK {
		g.t.Fatalf("device list %s = %d %s", dev, w.Code, w.Body.String())
	}
	for _, c := range l.Controls {
		if c.MRID == mrid {
			return c.EventStatus.Status
		}
	}
	g.t.Fatalf("device %s lists no control %s: %s", dev, mrid, w.Body.String())
	return ""
}

// Each share shows its devices, weight, test-default count, encoded value and
// status; the basis holds, so nothing is flagged and no delivery is invented.
func TestGridBlocks_ListShowsEachShare(t *testing.T) {
	g := newGridFixture(t)
	created := decodeGridCreated(t, g.post(gridBody(gridRow("discharge", 40, 3, soon(), 600), `{"direction":"charge","x":20,"y":3,"durationSeconds":300,"followsPrevious":true}`)))

	l := g.get()
	if len(l.Batches) != 1 || l.Batches[0].Batch != created.Batch || len(l.Batches[0].Rows) != 2 {
		t.Fatalf("list = %+v, want one batch %s with two rows", l, created.Batch)
	}
	r1, r2 := l.Batches[0].Rows[0], l.Batches[0].Rows[1]
	if r1.Row != 1 || r1.Direction != "discharge" || r1.RequestedW != 40000 || r1.EncodedW != 40000 || r1.DifferenceW != 0 ||
		r1.Start != created.Rows[0].Start || r1.End != created.Rows[0].Start+600 || r1.DurationSeconds != 600 {
		t.Errorf("row 1 = %+v, want discharge 40000 W encoded exactly over 600 s", r1)
	}
	if r2.Row != 2 || r2.Direction != "charge" || r2.RequestedW != -20000 || r2.EncodedW != -20000 || r2.Start != r1.End || r2.DurationSeconds != 300 {
		t.Errorf("row 2 = %+v, want charge -20000 W from row 1's end for 300 s", r2)
	}
	if r1.Status != "scheduled" || r2.Status != "scheduled" {
		t.Errorf("block statuses %q and %q, want scheduled", r1.Status, r2.Status)
	}
	if len(r1.Shares) != 2 {
		t.Fatalf("row 1 shares = %+v, want two", r1.Shares)
	}
	a, b := r1.Shares[0], r1.Shares[1]
	if a.AggregatorLFDI != gridAggA || a.Devices != 2 || a.WeightW != 15000 || a.TestDefaultCount != 1 || len(a.DefaultedDevices) != 1 || a.DefaultedDevices[0] != "a2" ||
		a.TargetW.Value != 30 || a.TargetW.Multiplier != 3 || a.Status != "scheduled" || a.AggregatorGone || a.Copies != 2 {
		t.Errorf("share A = %+v, want 2 devices weighing 15000 with a2 at the test default, 30 x 10^3, scheduled", a)
	}
	if b.AggregatorLFDI != gridAggB || b.Devices != 1 || b.WeightW != 5000 || b.TestDefaultCount != 0 || len(b.DefaultedDevices) != 0 ||
		b.TargetW.Value != 10 || b.TargetW.Multiplier != 3 || b.AggregatorGone || b.Copies != 1 {
		t.Errorf("share B = %+v, want 1 device weighing 5000 and no default, 10 x 10^3", b)
	}
	if a.MRID != created.Rows[0].Shares[0].Control.MRID || a.FSA != created.Rows[0].Shares[0].FSA {
		t.Errorf("share A mRID %s fsa %s, want the created share's", a.MRID, a.FSA)
	}
	if r2.Shares[0].TargetW.Value != -15 || r2.Shares[1].TargetW.Value != -5 {
		t.Errorf("row 2 targets %+v %+v, want -15 and -5 x 10^3", r2.Shares[0].TargetW, r2.Shares[1].TargetW)
	}
	if r1.BasisChanged || len(r1.CurrentSplit) != 0 {
		t.Errorf("row 1 basis changed %v with split %+v, want the basis to hold", r1.BasisChanged, r1.CurrentSplit)
	}
	// No reading anywhere: the figure is null, never a filled zero.
	if r1.DeliveredW != nil || a.DeliveredW != nil || b.DeliveredW != nil || a.CopiesWithoutReading != 2 || b.CopiesWithoutReading != 1 {
		t.Errorf("delivery row %v A %v (%d without) B %v (%d without), want null with every copy unread", r1.DeliveredW, a.DeliveredW, a.CopiesWithoutReading, b.DeliveredW, b.CopiesWithoutReading)
	}
}

// A block in its run reads active, and one past its end reads completed, by
// the status rule the Controls tab uses. The record is moved in time because
// a request cannot start in the past.
func TestGridBlocks_ActiveThenCompletedStatus(t *testing.T) {
	g := newGridFixture(t)
	created := decodeGridCreated(t, g.post(gridBody(gridRow("discharge", 40, 3, soon(), 600))))
	ctx := context.Background()
	move := func(start int64, duration uint32) {
		for _, s := range created.Rows[0].Shares {
			rec, err := g.f.h.AdminFSAs.FSAControl(ctx, s.FSA, s.Program, s.Control.ID)
			if err != nil {
				t.Fatal(err)
			}
			// An update cannot change creationTime, so the record is replaced.
			rec.Start, rec.Duration, rec.CreationTime = start, duration, start-60
			if err := g.f.h.AdminFSAs.DeleteFSAControl(ctx, s.FSA, s.Program, s.Control.ID); err != nil {
				t.Fatal(err)
			}
			if err := g.f.h.AdminFSAs.CreateFSAControl(ctx, s.FSA, rec); err != nil {
				t.Fatal(err)
			}
		}
	}
	now := sep2time.Now().Unix()
	move(now-300, 600)
	if blk := g.block(1); blk.Status != "active" || blk.Shares[0].Status != "active" || blk.Shares[1].Status != "active" {
		t.Errorf("block %q shares %q %q, want active", blk.Status, blk.Shares[0].Status, blk.Shares[1].Status)
	}
	move(now-1200, 600)
	if blk := g.block(1); blk.Status != "completed" {
		t.Errorf("block past its end reads %q, want completed", blk.Status)
	}
}

// An empty store lists no batches as an array, not null.
func TestGridBlocks_EmptyListIsAnArray(t *testing.T) {
	g := newGridFixture(t)
	req := httptest.NewRequest(http.MethodGet, "/api/derms/grid-requests", nil)
	w := httptest.NewRecorder()
	g.mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK || strings.TrimSpace(w.Body.String()) != `{"batches":[]}` {
		t.Errorf("empty list = %d %s, want {\"batches\":[]}", w.Code, w.Body.String())
	}
}

// A removed aggregator reads "aggregator gone" and its control stays live:
// its copies are not cancelled and its record is not. The block reads "basis
// changed" with the current split beside, and a batch cancel still reaches
// the gone aggregator's share.
func TestGridBlocks_AggregatorGoneKeepsItsControlLive(t *testing.T) {
	g := newGridFixture(t)
	created := decodeGridCreated(t, g.post(gridBody(gridRow("discharge", 40, 3, soon(), 600))))
	ctx := context.Background()
	for _, m := range []string{gridA1, gridA2} {
		if err := g.dc.managers.Unassign(ctx, m); err != nil {
			t.Fatal(err)
		}
	}

	blk := g.block(1)
	a, b := blk.Shares[0], blk.Shares[1]
	if !a.AggregatorGone || b.AggregatorGone {
		t.Errorf("gone flags A %v B %v, want A gone and B present", a.AggregatorGone, b.AggregatorGone)
	}
	if a.Status != "scheduled" || blk.Status != "scheduled" {
		t.Errorf("gone share status %q block %q, want its control still live (scheduled)", a.Status, blk.Status)
	}
	if got := g.servedStatus("a1", a.MRID); got != "scheduled" {
		t.Errorf("a1 serves the gone aggregator's control as %q, want scheduled", got)
	}
	if !blk.BasisChanged || len(blk.CurrentSplit) != 1 {
		t.Fatalf("block = %+v, want basis changed with one current share", blk)
	}
	if c := blk.CurrentSplit[0]; c.AggregatorLFDI != gridAggB || c.WeightW != 5000 || c.Units != 40 || c.TargetW == nil || c.TargetW.Value != 40 || c.TargetW.Multiplier != 3 {
		t.Errorf("current split = %+v, want all 40 x 10^3 to aggregator B", c)
	}
	// The stored basis is untouched: the fixed split is still shown.
	if a.WeightW != 15000 || a.TargetW.Value != 30 || b.TargetW.Value != 10 {
		t.Errorf("stored shares A %+v B %+v changed with the fleet", a, b)
	}

	w := g.cancel("/api/derms/grid-requests/"+created.Batch+"/cancel", `{}`)
	if w.Code != http.StatusOK {
		t.Fatalf("batch cancel = %d %s", w.Code, w.Body.String())
	}
	if got := g.servedStatus("a1", a.MRID); got != "cancelled" {
		t.Errorf("a1 serves the gone aggregator's control as %q after the batch cancel, want cancelled", got)
	}
}

// A changed nameplate reads "basis changed" with the weights and the split
// the current nameplates give; no aggregator is gone.
func TestGridBlocks_ChangedNameplateReadsBasisChanged(t *testing.T) {
	g := newGridFixture(t)
	decodeGridCreated(t, g.post(gridBody(gridRow("discharge", 40, 3, soon(), 600))))
	if g.block(1).BasisChanged {
		t.Fatal("basis changed before any change")
	}
	ctx := context.Background()
	if err := g.caps.Update(ctx, "a1/1", "default", sep2.DERCapability{RTGMaxW: &sep2.ActivePower{Value: 20, Multiplier: 3}}); err != nil {
		t.Fatal(err)
	}

	blk := g.block(1)
	if !blk.BasisChanged || blk.Shares[0].AggregatorGone || blk.Shares[1].AggregatorGone {
		t.Fatalf("block = %+v, want basis changed and nobody gone", blk)
	}
	want := []struct {
		lfdi          string
		weight        float64
		units, target int64
	}{{gridAggA, 25000, 33, 33}, {gridAggB, 5000, 7, 7}}
	if len(blk.CurrentSplit) != len(want) {
		t.Fatalf("current split = %+v, want two shares", blk.CurrentSplit)
	}
	for n, w := range want {
		c := blk.CurrentSplit[n]
		if c.AggregatorLFDI != w.lfdi || c.WeightW != w.weight || c.Units != w.units || c.TargetW == nil || c.TargetW.Value != w.target || c.TargetW.Multiplier != 3 {
			t.Errorf("current share %d = %+v, want %+v x 10^3", n, c, w)
		}
	}
	// The fixed split stays beside it.
	if blk.Shares[0].WeightW != 15000 || blk.Shares[0].TargetW.Value != 30 {
		t.Errorf("fixed share A = %+v, want the split at creation", blk.Shares[0])
	}
}

// Delivered sums the copies' average watts. A copy with no reading adds
// nothing, and a share with no reading anywhere is null, not zero.
func TestGridBlocks_DeliveredSumsCopiesAndSkipsUnread(t *testing.T) {
	g := newGridFixture(t)
	created := decodeGridCreated(t, g.post(gridBody(gridRow("discharge", 40, 3, soon(), 900))))
	ctx := context.Background()
	// Put the block 600 s into its run so a reading has a window to cover.
	now := sep2time.Now().Unix()
	for _, s := range created.Rows[0].Shares {
		rec, err := g.f.h.AdminFSAs.FSAControl(ctx, s.FSA, s.Program, s.Control.ID)
		if err != nil {
			t.Fatal(err)
		}
		rec.Start = now - 600
		if err := g.f.h.AdminFSAs.UpdateFSAControl(ctx, s.FSA, rec); err != nil {
			t.Fatal(err)
		}
	}
	mups, mmrs := memory.NewStore[sep2.MirrorUsagePoint](), memory.NewScopedStore[sep2.MirrorMeterReading]()
	g.dc.h.MirrorUsagePoints, g.dc.h.MirrorMeterReadings = mups, mmrs
	d := &deliveryHarness{dcHarness: g.dc, mups: mups, mmrs: mmrs}
	// a1 exports 3600 W, a2 has no reading, b1 exports 1800 W.
	d.seedReadings(t, "ra1", gridA1, roleIsDER, ptrU32(900), reading{mrid: "P", at: now - 500, value: 3600, dir: reverseDir()})
	d.seedReadings(t, "rb1", gridB1, roleIsDER, ptrU32(900), reading{mrid: "P", at: now - 500, value: 1800, dir: reverseDir()})

	blk := g.block(1)
	a, b := blk.Shares[0], blk.Shares[1]
	if a.DeliveredW == nil || *a.DeliveredW != 3600 || a.CopiesWithoutReading != 1 || a.Copies != 2 || a.CoveredSeconds <= 0 {
		t.Errorf("share A delivered %v with %d of %d copies unread, covered %d s, want 3600 W from a1 alone", a.DeliveredW, a.CopiesWithoutReading, a.Copies, a.CoveredSeconds)
	}
	if b.DeliveredW == nil || *b.DeliveredW != 1800 || b.CopiesWithoutReading != 0 {
		t.Errorf("share B delivered %v with %d unread, want 1800 W", b.DeliveredW, b.CopiesWithoutReading)
	}
	if blk.DeliveredW == nil || *blk.DeliveredW != 5400 {
		t.Errorf("block delivered %v, want the shares' sum 5400 W", blk.DeliveredW)
	}

	// Only B reports: A is null rather than a filled zero, and the block is B's alone.
	if _, err := mmrs.DeleteParent(ctx, "ra1"); err != nil {
		t.Fatal(err)
	}
	blk = g.block(1)
	if blk.Shares[0].DeliveredW != nil || blk.Shares[0].CopiesWithoutReading != 2 || blk.DeliveredW == nil || *blk.DeliveredW != 1800 {
		t.Errorf("after A's readings go: share A %v (%d unread), block %v, want null and 1800", blk.Shares[0].DeliveredW, blk.Shares[0].CopiesWithoutReading, blk.DeliveredW)
	}
}

// Cancel per block reaches every share through the FSA cancel core: each
// aggregator's device serves its copy cancelled, the record is cancelled, and
// the other block of the batch is untouched.
func TestGridBlocks_CancelRowReachesEveryShare(t *testing.T) {
	g := newGridFixture(t)
	created := decodeGridCreated(t, g.post(gridBody(gridRow("discharge", 40, 3, soon(), 600), `{"direction":"charge","x":20,"y":3,"durationSeconds":300,"followsPrevious":true}`)))
	r1, r2 := created.Rows[0], created.Rows[1]

	w := g.cancel("/api/derms/grid-requests/"+created.Batch+"/rows/1/cancel", `{"reason":"test stop"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("cancel = %d %s", w.Code, w.Body.String())
	}
	var got handler.GridCancelled
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Batch != created.Batch || got.Row == nil || *got.Row != 1 || len(got.Cancelled) != 2 {
		t.Fatalf("reply = %+v, want row 1 of the batch with two shares cancelled", got)
	}
	if c := got.Cancelled[0]; c.AggregatorLFDI != gridAggA || c.MRID != r1.Shares[0].Control.MRID || c.Copies != 2 {
		t.Errorf("cancelled A = %+v, want its mRID over 2 copies", c)
	}
	if c := got.Cancelled[1]; c.AggregatorLFDI != gridAggB || c.MRID != r1.Shares[1].Control.MRID || c.Copies != 1 {
		t.Errorf("cancelled B = %+v, want its mRID over 1 copy", c)
	}
	for dev, mrid := range map[string]string{"a1": r1.Shares[0].Control.MRID, "a2": r1.Shares[0].Control.MRID, "b1": r1.Shares[1].Control.MRID} {
		if st := g.servedStatus(dev, mrid); st != "cancelled" {
			t.Errorf("%s serves block 1's control as %q, want cancelled", dev, st)
		}
	}
	for dev, mrid := range map[string]string{"a1": r2.Shares[0].Control.MRID, "b1": r2.Shares[1].Control.MRID} {
		if st := g.servedStatus(dev, mrid); st != "scheduled" {
			t.Errorf("%s serves block 2's control as %q, want scheduled", dev, st)
		}
	}
	rec, err := g.f.h.AdminFSAs.FSAControl(context.Background(), r1.Shares[0].FSA, r1.Shares[0].Program, r1.Shares[0].Control.ID)
	if err != nil || rec.CancelledAt == nil || rec.CancelReason != "test stop" {
		t.Errorf("record = %+v err %v, want cancelled with the reason", rec, err)
	}
	if blk := g.block(1); blk.Status != "cancelled" || blk.Shares[0].Status != "cancelled" {
		t.Errorf("block 1 status %q share %q, want cancelled", blk.Status, blk.Shares[0].Status)
	}
	if blk := g.block(2); blk.Status != "scheduled" {
		t.Errorf("block 2 status %q, want scheduled", blk.Status)
	}
	if !strings.Contains(g.dc.logs.String(), "der_grid_request_cancelled") {
		t.Errorf("no cancel audit line in %s", g.dc.logs.String())
	}

	// A second cancel of the same block is refused, as a repeated cancel is.
	if w := g.cancel("/api/derms/grid-requests/"+created.Batch+"/rows/1/cancel", `{}`); w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "already cancelled") {
		t.Errorf("repeat cancel = %d %s, want 409 already cancelled", w.Code, w.Body.String())
	}
}

// Cancel per batch reaches every block not yet cancelled, leaving the cancelled
// one as it was, then refuses.
func TestGridBlocks_CancelBatchReachesEveryBlock(t *testing.T) {
	g := newGridFixture(t)
	created := decodeGridCreated(t, g.post(gridBody(gridRow("discharge", 40, 3, soon(), 600), `{"direction":"charge","x":20,"y":3,"durationSeconds":300,"followsPrevious":true}`)))
	if w := g.cancel("/api/derms/grid-requests/"+created.Batch+"/rows/1/cancel", `{}`); w.Code != http.StatusOK {
		t.Fatalf("row cancel = %d %s", w.Code, w.Body.String())
	}

	w := g.cancel("/api/derms/grid-requests/"+created.Batch+"/cancel", "")
	var got handler.GridCancelled
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil || w.Code != http.StatusOK {
		t.Fatalf("batch cancel = %d %s", w.Code, w.Body.String())
	}
	if got.Row != nil || len(got.Cancelled) != 2 || got.Cancelled[0].Row != 2 || got.Cancelled[1].Row != 2 {
		t.Errorf("reply = %+v, want only block 2's two shares: block 1 was already cancelled", got)
	}
	for dev, mrid := range map[string]string{"a1": created.Rows[1].Shares[0].Control.MRID, "b1": created.Rows[1].Shares[1].Control.MRID} {
		if st := g.servedStatus(dev, mrid); st != "cancelled" {
			t.Errorf("%s serves block 2's control as %q, want cancelled", dev, st)
		}
	}
	for _, r := range g.get().Batches[0].Rows {
		if r.Status != "cancelled" {
			t.Errorf("row %d status %q, want cancelled", r.Row, r.Status)
		}
	}
	if w := g.cancel("/api/derms/grid-requests/"+created.Batch+"/cancel", ""); w.Code != http.StatusConflict {
		t.Errorf("repeat batch cancel = %d %s, want 409", w.Code, w.Body.String())
	}
}

// A share cancelled from the Controls tab shows on the block as partly
// cancelled; the other share is live.
func TestGridBlocks_OneShareCancelledReadsPartly(t *testing.T) {
	g := newGridFixture(t)
	created := decodeGridCreated(t, g.post(gridBody(gridRow("discharge", 40, 3, soon(), 600))))
	s := created.Rows[0].Shares[0]
	if w := g.f.do(http.MethodPost, "/api/fsas/"+s.FSA+"/der-programs/"+s.Program+"/controls/"+s.Control.ID+"/cancel", `{}`); w.Code != http.StatusOK {
		t.Fatalf("controls tab cancel = %d %s", w.Code, w.Body.String())
	}
	blk := g.block(1)
	if blk.Status != "partly cancelled" || blk.Shares[0].Status != "cancelled" || blk.Shares[1].Status != "scheduled" {
		t.Errorf("block %q shares %q and %q, want partly cancelled with A cancelled and B scheduled", blk.Status, blk.Shares[0].Status, blk.Shares[1].Status)
	}
	// Cancelling the block finishes the live share and skips the cancelled one.
	w := g.cancel("/api/derms/grid-requests/"+created.Batch+"/rows/1/cancel", `{}`)
	var got handler.GridCancelled
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil || w.Code != http.StatusOK || len(got.Cancelled) != 1 || got.Cancelled[0].AggregatorLFDI != gridAggB {
		t.Errorf("block cancel = %d %s, want only B's share cancelled", w.Code, w.Body.String())
	}
}

// Nothing unknown is cancelled, and an ended block is refused as ended.
func TestGridBlocks_CancelRefusals(t *testing.T) {
	g := newGridFixture(t)
	created := decodeGridCreated(t, g.post(gridBody(gridRow("discharge", 40, 3, soon(), 600))))
	before, _ := g.dc.storedCounts(t)
	for name, path := range map[string]string{
		"unknown batch":  "/api/derms/grid-requests/nosuchbatch/cancel",
		"unknown row":    "/api/derms/grid-requests/" + created.Batch + "/rows/2/cancel",
		"row zero":       "/api/derms/grid-requests/" + created.Batch + "/rows/0/cancel",
		"row not number": "/api/derms/grid-requests/" + created.Batch + "/rows/x/cancel",
		"newline batch":  "/api/derms/grid-requests/a%0Ab/cancel",
	} {
		if w := g.cancel(path, `{}`); w.Code != http.StatusNotFound {
			t.Errorf("%s: cancel = %d %s, want 404", name, w.Code, w.Body.String())
		}
	}
	if w := g.cancel("/api/derms/grid-requests/"+created.Batch+"/cancel", `{"reason":`+fmt.Sprintf("%q", strings.Repeat("x", 300))+`}`); w.Code != http.StatusUnprocessableEntity && w.Code != http.StatusBadRequest {
		t.Errorf("over-long reason = %d %s, want a refusal", w.Code, w.Body.String())
	}
	if blk := g.block(1); blk.Status != "scheduled" {
		t.Errorf("block status %q after refusals, want scheduled", blk.Status)
	}
	if strings.Contains(g.dc.logs.String(), "\na b") {
		t.Errorf("a path value reached the log unsanitised: %s", g.dc.logs.String())
	}

	// Move the block into the past: ended, so it cannot be cancelled.
	ctx := context.Background()
	for _, s := range created.Rows[0].Shares {
		rec, err := g.f.h.AdminFSAs.FSAControl(ctx, s.FSA, s.Program, s.Control.ID)
		if err != nil {
			t.Fatal(err)
		}
		rec.Start = sep2time.Now().Unix() - 1200
		if err := g.f.h.AdminFSAs.UpdateFSAControl(ctx, s.FSA, rec); err != nil {
			t.Fatal(err)
		}
	}
	if w := g.cancel("/api/derms/grid-requests/"+created.Batch+"/rows/1/cancel", `{}`); w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "ended") {
		t.Errorf("cancel of an ended block = %d %s, want 409 ended", w.Code, w.Body.String())
	}
	if after, _ := g.dc.storedCounts(t); after != before {
		t.Errorf("stored controls %d after refusals, want %d", after, before)
	}
}
