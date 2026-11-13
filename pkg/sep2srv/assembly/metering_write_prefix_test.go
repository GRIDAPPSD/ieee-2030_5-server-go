package assembly_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/assembly"
	coremetering "github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/handlers/metering"
)

// Ids where one prefixes another (u1 and u10, m1 and m10, s1 and s10) pin the
// "/" the cascade appends to a prefix: without it a delete of u1 also removes
// every record whose key starts with "u1".

func seedMeterReadingTree(t *testing.T, s *assembly.Stores, upt, mr string) {
	t.Helper()
	ctx := context.Background()
	m := sep2.MeterReading{}
	m.Href = "/upt/" + upt + "/mr/" + mr
	rt := sep2.ReadingType{}
	rt.Href = m.Href + "/rt"
	direct := int64(5)
	for _, err := range []error{
		s.MeterReadings.Create(ctx, upt, mr, m),
		s.ReadingTypes.Create(ctx, upt, mr, rt),
		s.Readings.Create(ctx, scopeOf(coremetering.ReadingSetScopeKey(upt, mr)), "d1", sep2.Reading{Value: &direct}),
	} {
		if err != nil {
			t.Fatal(err)
		}
	}
}

func seedSetTree(t *testing.T, s *assembly.Stores, upt, mr, rs string) {
	t.Helper()
	ctx := context.Background()
	set := sep2.ReadingSet{}
	set.Href = "/upt/" + upt + "/mr/" + mr + "/rs/" + rs
	val := int64(6)
	if err := s.ReadingSets.Create(ctx, scopeOf(coremetering.ReadingSetScopeKey(upt, mr)), rs, set); err != nil {
		t.Fatal(err)
	}
	if err := s.Readings.Create(ctx, scopeOf(coremetering.ReadingScopeKey(upt, mr, rs)), "r1", sep2.Reading{Value: &val}); err != nil {
		t.Fatal(err)
	}
}

// requireTreeIntact asserts the ReadingType, the direct reading, one set and
// its reading survive under (upt, mr, rs).
func requireTreeIntact(t *testing.T, s *assembly.Stores, upt, mr, rs string) {
	t.Helper()
	ctx := context.Background()
	if _, err := s.MeterReadings.Get(ctx, upt, mr); err != nil {
		t.Errorf("MeterReading %s/%s was removed: %v", upt, mr, err)
	}
	if _, err := s.ReadingTypes.Get(ctx, upt, mr); err != nil {
		t.Errorf("ReadingType %s/%s was removed: %v", upt, mr, err)
	}
	if sets, inSet, direct := hierarchyCounts(t, s, upt, mr, rs); sets != 1 || inSet != 1 || direct != 1 {
		t.Errorf("%s/%s holds %d sets, %d set readings, %d direct readings, want 1, 1, 1 untouched", upt, mr, sets, inSet, direct)
	}
}

func TestMeteringWrites_DeleteStopsAtTheIdBoundary(t *testing.T) {
	t.Parallel()

	t.Run("u1 leaves u10", func(t *testing.T) {
		t.Parallel()
		do, stores := meteringWriteServer(t)
		upt := sep2.UsagePoint{}
		upt.Href = "/upt/u10"
		if err := stores.UsagePoints.Create(context.Background(), "u10", upt); err != nil {
			t.Fatal(err)
		}
		seedMeterReadingTree(t, stores, "u10", "m10")
		seedSetTree(t, stores, "u10", "m10", "s10")
		if status, raw := do(http.MethodDelete, "/upt/u1", managerLFDI, ""); status != http.StatusNoContent {
			t.Fatalf("DELETE /upt/u1 = %d %s, want 204", status, raw)
		}
		if _, err := stores.UsagePoints.Get(context.Background(), "u10"); err != nil {
			t.Errorf("UsagePoint u10 was removed with u1: %v", err)
		}
		requireTreeIntact(t, stores, "u10", "m10", "s10")
	})

	t.Run("m1 leaves m10", func(t *testing.T) {
		t.Parallel()
		do, stores := meteringWriteServer(t)
		seedMeterReadingTree(t, stores, "u1", "m10")
		seedSetTree(t, stores, "u1", "m10", "s10")
		if status, raw := do(http.MethodDelete, "/upt/u1/mr/m1", managerLFDI, ""); status != http.StatusNoContent {
			t.Fatalf("DELETE /upt/u1/mr/m1 = %d %s, want 204", status, raw)
		}
		requireTreeIntact(t, stores, "u1", "m10", "s10")
	})

	t.Run("s1 leaves s10", func(t *testing.T) {
		t.Parallel()
		do, stores := meteringWriteServer(t)
		seedSetTree(t, stores, "u1", "m1", "s10")
		if status, raw := do(http.MethodDelete, "/upt/u1/mr/m1/rs/s1", managerLFDI, ""); status != http.StatusNoContent {
			t.Fatalf("DELETE /upt/u1/mr/m1/rs/s1 = %d %s, want 204", status, raw)
		}
		if n, err := stores.Readings.Count(context.Background(), scopeOf(coremetering.ReadingScopeKey("u1", "m1", "s10"))); err != nil || n != 1 {
			t.Errorf("readings under s10 = %d, %v, want 1 untouched", n, err)
		}
		if _, err := stores.ReadingSets.Get(context.Background(), scopeOf(coremetering.ReadingSetScopeKey("u1", "m1")), "s10"); err != nil {
			t.Errorf("ReadingSet s10 was removed with s1: %v", err)
		}
	})
}
