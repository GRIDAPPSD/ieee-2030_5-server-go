package metering_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/handlers/metering"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

// ctxProbe records the context error each cascade call sees.
type ctxProbe[T store.Copier[T]] struct {
	store.ScopedStore[T]
	errs []error
}

func (p *ctxProbe[T]) DeleteParent(ctx context.Context, id string) (uint32, error) {
	p.errs = append(p.errs, ctx.Err())
	return store.CascadeParent(ctx, p.ScopedStore, id)
}

func (p *ctxProbe[T]) DeleteParentsWithPrefix(ctx context.Context, prefix string) (uint32, error) {
	p.errs = append(p.errs, ctx.Err())
	return store.CascadePrefix(ctx, p.ScopedStore, prefix)
}

// A client that disconnects cancels the request context. The cascade and the
// final delete must still run on a live one, or the delete stops part-way.
func TestDeleteUsagePoint_CascadeRunsAfterTheClientDisconnects(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	readings := &ctxProbe[sep2.Reading]{ScopedStore: memory.NewScopedStore[sep2.Reading]()}
	tree := metering.UsagePointTree{
		UsagePoints:   memory.NewStore[sep2.UsagePoint](),
		MeterReadings: memory.NewScopedStore[sep2.MeterReading](),
		ReadingSets:   memory.NewScopedStore[sep2.ReadingSet](),
		Readings:      readings,
		ReadingTypes:  memory.NewScopedStore[sep2.ReadingType](),
	}
	if err := tree.UsagePoints.Create(ctx, "u1", sep2.UsagePoint{}); err != nil {
		t.Fatal(err)
	}
	val := int64(1)
	if err := tree.Readings.Create(ctx, "u1/m1/s1", "r1", sep2.Reading{Value: &val}); err != nil {
		t.Fatal(err)
	}

	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	req := httptest.NewRequestWithContext(cancelled, http.MethodDelete, "/upt/u1", nil)
	req.SetPathValue("uptId", "u1")
	rec := httptest.NewRecorder()
	metering.HandleDeleteUsagePoint(tree)(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("DELETE with a cancelled request context = %d, want 204", rec.Code)
	}
	if len(readings.errs) == 0 {
		t.Fatal("control: the readings cascade never ran")
	}
	for i, err := range readings.errs {
		if err != nil {
			t.Errorf("readings cascade call %d saw context error %v", i, err)
		}
	}
	if _, err := tree.UsagePoints.Get(ctx, "u1"); err == nil {
		t.Error("UsagePoint u1 survived the delete")
	}
	if n, err := tree.Readings.Count(ctx, "u1/m1/s1"); err != nil || n != 0 {
		t.Errorf("readings under u1/m1/s1 = %d, %v, want 0", n, err)
	}
}
