package assembly_test

import (
	"context"
	"errors"
	"net/http"
	"sync/atomic"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/assembly"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
)

// failingPrefixReadings is a Readings store whose prefix delete fails while
// failing is set. Its parent delete and
// reads go to the real store, so the cascade steps before it do remove records.
type failingPrefixReadings struct {
	store.ScopedStore[sep2.Reading]
	failing atomic.Bool
}

func (f *failingPrefixReadings) DeleteParent(ctx context.Context, parentID string) (uint32, error) {
	return store.CascadeParent(ctx, f.ScopedStore, parentID)
}

func (f *failingPrefixReadings) DeleteParentsWithPrefix(ctx context.Context, prefix string) (uint32, error) {
	if f.failing.Load() {
		return 0, errors.New("backend gone part-way")
	}
	return store.CascadePrefix(ctx, f.ScopedStore, prefix)
}

func TestMeteringWrites_PartialCascadeKeepsTheRecordAndRetryCompletes(t *testing.T) {
	t.Parallel()
	flaky := &failingPrefixReadings{}
	flaky.failing.Store(true)
	do, stores := meteringWriteServerWith(t, func(s *assembly.Stores) {
		flaky.ScopedStore = s.Readings
		s.Readings = flaky
	})
	ctx := context.Background()

	if status, _ := do(http.MethodDelete, "/upt/u1", managerLFDI, ""); status != http.StatusInternalServerError {
		t.Fatalf("DELETE /upt/u1 with a failing prefix delete = %d, want 500", status)
	}
	if _, err := stores.UsagePoints.Get(ctx, "u1"); err != nil {
		t.Errorf("UsagePoint u1 after the failed cascade: %v, want it kept so the delete can be retried", err)
	}
	if n, err := stores.MeterReadings.Count(ctx, "u1"); err != nil || n != 0 {
		t.Errorf("MeterReadings under u1 = %d, %v, want 0: the steps before the failure had already run", n, err)
	}

	flaky.failing.Store(false)
	if status, raw := do(http.MethodDelete, "/upt/u1", managerLFDI, ""); status != http.StatusNoContent {
		t.Fatalf("retried DELETE /upt/u1 = %d %s, want 204", status, raw)
	}
	if _, err := stores.UsagePoints.Get(ctx, "u1"); !isNotFound(err) {
		t.Errorf("UsagePoint u1 after the retry: %v, want not found", err)
	}
	if sets, inSet, direct := hierarchyCounts(t, stores, "u1", "m1", "s2"); sets != 0 || inSet != 0 || direct != 0 {
		t.Errorf("u1/m1 holds %d sets, %d s2 readings, %d direct readings after the retry, want 0, 0, 0", sets, inSet, direct)
	}
	if sets, inSet, direct := hierarchyCounts(t, stores, "u2", "m1", "s1"); sets != 1 || inSet != 1 || direct != 1 {
		t.Errorf("u2/m1 holds %d sets, %d readings, %d direct after the retry, want 1, 1, 1 untouched", sets, inSet, direct)
	}
}
