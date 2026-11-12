package assembly_test

import (
	"context"
	"encoding/xml"
	"errors"
	"net/http"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/assembly"
	coremetering "github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/handlers/metering"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
)

const (
	rdPath = "/upt/u1/mr/m1/rs/s2/r/r2"
	rtPath = "/upt/u1/mr/m1/rt"

	readingBody = `<Reading xmlns="urn:ieee:std:2030.5:ns" href="/forged"><value>555</value></Reading>`
	typeBody    = `<ReadingType xmlns="urn:ieee:std:2030.5:ns" href="/forged"><uom>38</uom></ReadingType>`
)

// meteringWriteServer serves the management fleet with the reading hierarchy
// seeded under the UsagePoints u1 and u2. The manager manages the device
// victimLFDI names, which is the plain device in these tests.
func meteringWriteServer(t *testing.T) (do func(method, path, as, body string) (int, []byte), stores *assembly.Stores) {
	t.Helper()
	fleet := newManagementFleet(t)
	seedHierarchy(t, fleet.stores)
	for _, id := range []string{"u1", "u2"} {
		upt := sep2.UsagePoint{}
		upt.Href = "/upt/" + id
		if err := fleet.stores.UsagePoints.Create(context.Background(), id, upt); err != nil {
			t.Fatal(err)
		}
	}
	srv := gateServer(t, fleet.stores, gateTestPolicy())
	return func(method, path, as, body string) (int, []byte) {
		return gateRequest(t, srv, method, path, as, body)
	}, fleet.stores
}

// hierarchyCounts reads how many ReadingSets, ReadingSet readings and
// readings listed directly under the MeterReading a UsagePoint holds.
func hierarchyCounts(t *testing.T, s *assembly.Stores, upt, mr, rs string) (sets, setReadings, directReadings uint32) {
	t.Helper()
	ctx := context.Background()
	count := func(c uint32, err error) uint32 {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	setKey := scopeOf(coremetering.ReadingSetScopeKey(upt, mr))
	readKey := scopeOf(coremetering.ReadingScopeKey(upt, mr, rs))
	return count(s.ReadingSets.Count(ctx, setKey)), count(s.Readings.Count(ctx, readKey)), count(s.Readings.Count(ctx, setKey))
}

func TestMeteringWrites_PlainDeviceIsRefusedAndNothingChanges(t *testing.T) {
	t.Parallel()
	do, stores := meteringWriteServer(t)
	for _, tc := range []struct{ method, path, body string }{
		{http.MethodDelete, "/upt/u1", ""},
		{http.MethodDelete, "/upt/u1/mr/m1", ""},
		{http.MethodDelete, "/upt/u1/mr/m1/rs/s2", ""},
		{http.MethodDelete, rdPath, ""},
		{http.MethodPut, rdPath, readingBody},
		{http.MethodPut, rtPath, typeBody},
	} {
		for _, caller := range []string{victimLFDI, callerLFDI} {
			if status, _ := do(tc.method, tc.path, caller, tc.body); status != http.StatusForbidden {
				t.Errorf("%s %s as a plain device = %d, want 403", tc.method, tc.path, status)
			}
		}
	}

	ctx := context.Background()
	rd, err := stores.Readings.Get(ctx, scopeOf(coremetering.ReadingScopeKey("u1", "m1", "s2")), "r2")
	if err != nil || rd.Value == nil || *rd.Value != 20 {
		t.Errorf("Reading r2 after refused writes = %+v, %v; want value 20 untouched", rd, err)
	}
	rt, err := stores.ReadingTypes.Get(ctx, "u1", "m1")
	if err != nil || rt.Uom == nil || *rt.Uom != sep2.UomWattHours {
		t.Errorf("ReadingType after refused writes = %+v, %v; want uom %d untouched", rt, err, sep2.UomWattHours)
	}
	if sets, inSet, direct := hierarchyCounts(t, stores, "u1", "m1", "s2"); sets != 3 || inSet != 3 || direct != 3 {
		t.Errorf("u1/m1 holds %d sets, %d set readings, %d direct readings after refused writes, want 3, 3, 3", sets, inSet, direct)
	}
	if _, err := stores.UsagePoints.Get(ctx, "u1"); err != nil {
		t.Errorf("UsagePoint u1 after a refused DELETE: %v", err)
	}
}

func TestMeteringWrites_ManagerPutReplacesAndReadsBack(t *testing.T) {
	t.Parallel()
	do, stores := meteringWriteServer(t)

	if status, raw := do(http.MethodPut, rdPath, managerLFDI, readingBody); status != http.StatusNoContent {
		t.Fatalf("PUT Reading = %d %s, want 204", status, raw)
	}
	status, raw := do(http.MethodGet, rdPath, managerLFDI, "")
	var rd sep2.Reading
	if status != http.StatusOK || xml.Unmarshal(raw, &rd) != nil {
		t.Fatalf("GET Reading = %d %s", status, raw)
	}
	if rd.Value == nil || *rd.Value != 555 || rd.Href != rdPath {
		t.Errorf("Reading after PUT = value %v href %q, want 555 at %q (the client's href is not kept)", rd.Value, rd.Href, rdPath)
	}
	if rd.LocalID != nil {
		t.Errorf("Reading after PUT kept localID %v: a PUT replaces the record", *rd.LocalID)
	}

	if status, raw := do(http.MethodPut, rtPath, managerLFDI, typeBody); status != http.StatusNoContent {
		t.Fatalf("PUT ReadingType = %d %s, want 204", status, raw)
	}
	status, raw = do(http.MethodGet, rtPath, managerLFDI, "")
	var rt sep2.ReadingType
	if status != http.StatusOK || xml.Unmarshal(raw, &rt) != nil {
		t.Fatalf("GET ReadingType = %d %s", status, raw)
	}
	if rt.Uom == nil || *rt.Uom != 38 || rt.Href != rtPath {
		t.Errorf("ReadingType after PUT = uom %v href %q, want 38 at %q", rt.Uom, rt.Href, rtPath)
	}

	// A PUT names an existing record: a missing one is a 404 and creates nothing.
	missing := "/upt/u1/mr/m1/rs/s2/r/r9"
	if status, _ := do(http.MethodPut, missing, managerLFDI, readingBody); status != http.StatusNotFound {
		t.Errorf("PUT of a missing Reading = %d, want 404", status)
	}
	if _, err := stores.Readings.Get(context.Background(), scopeOf(coremetering.ReadingScopeKey("u1", "m1", "s2")), "r9"); err == nil {
		t.Error("PUT of a missing Reading created it")
	}
	if status, _ := do(http.MethodPut, "/upt/u1/mr/m2/rt", managerLFDI, typeBody); status != http.StatusNotFound {
		t.Errorf("PUT of a missing ReadingType = %d, want 404", status)
	}
	if status, _ := do(http.MethodPut, rdPath, managerLFDI, "<not-xml"); status != http.StatusBadRequest {
		t.Errorf("PUT of a malformed Reading = %d, want 400", status)
	}
}

func TestMeteringWrites_ManagerDeleteRemovesRecordAndEverythingBeneath(t *testing.T) {
	t.Parallel()

	t.Run("Reading", func(t *testing.T) {
		t.Parallel()
		do, stores := meteringWriteServer(t)
		if status, raw := do(http.MethodDelete, rdPath, managerLFDI, ""); status != http.StatusNoContent {
			t.Fatalf("DELETE Reading = %d %s, want 204", status, raw)
		}
		if status, _ := do(http.MethodGet, rdPath, managerLFDI, ""); status != http.StatusNotFound {
			t.Errorf("GET of the deleted Reading = %d, want 404", status)
		}
		_, raw := do(http.MethodGet, "/upt/u1/mr/m1/rs/s2/r", managerLFDI, "")
		var list sep2.ReadingList
		if err := xml.Unmarshal(raw, &list); err != nil || list.All != 2 || len(list.Reading) != 2 {
			t.Errorf("Reading list after DELETE = all %d, %d items (%v), want 2 and 2", list.All, len(list.Reading), err)
		}
		if status, _ := do(http.MethodDelete, rdPath, managerLFDI, ""); status != http.StatusNotFound {
			t.Errorf("second DELETE = %d, want 404", status)
		}
		if _, inSet, _ := hierarchyCounts(t, stores, "u2", "m1", "s1"); inSet != 1 {
			t.Errorf("u2 set readings = %d, want the other UsagePoint's 1 untouched", inSet)
		}
	})

	t.Run("ReadingSet", func(t *testing.T) {
		t.Parallel()
		do, stores := meteringWriteServer(t)
		if status, raw := do(http.MethodDelete, "/upt/u1/mr/m1/rs/s2", managerLFDI, ""); status != http.StatusNoContent {
			t.Fatalf("DELETE ReadingSet = %d %s, want 204", status, raw)
		}
		_, raw := do(http.MethodGet, "/upt/u1/mr/m1/rs", managerLFDI, "")
		var list sep2.ReadingSetList
		if err := xml.Unmarshal(raw, &list); err != nil || list.All != 2 || len(list.ReadingSet) != 2 {
			t.Errorf("ReadingSet list after DELETE = all %d, %d items (%v), want 2 and 2", list.All, len(list.ReadingSet), err)
		}
		if status, _ := do(http.MethodGet, "/upt/u1/mr/m1/rs/s2/r", managerLFDI, ""); status != http.StatusNotFound {
			t.Errorf("GET of the deleted set's readings = %d, want 404", status)
		}
		if sets, inSet, direct := hierarchyCounts(t, stores, "u1", "m1", "s2"); sets != 2 || inSet != 0 || direct != 3 {
			t.Errorf("u1/m1 holds %d sets, %d s2 readings, %d direct readings, want 2, 0, 3", sets, inSet, direct)
		}
		if sets, inSet, _ := hierarchyCounts(t, stores, "u2", "m1", "s1"); sets != 1 || inSet != 1 {
			t.Errorf("u2/m1 holds %d sets and %d readings, want the other UsagePoint's 1 and 1 untouched", sets, inSet)
		}
	})

	t.Run("MeterReading", func(t *testing.T) {
		t.Parallel()
		do, stores := meteringWriteServer(t)
		if status, raw := do(http.MethodDelete, "/upt/u1/mr/m1", managerLFDI, ""); status != http.StatusNoContent {
			t.Fatalf("DELETE MeterReading = %d %s, want 204", status, raw)
		}
		_, raw := do(http.MethodGet, "/upt/u1/mr", managerLFDI, "")
		var list sep2.MeterReadingList
		if err := xml.Unmarshal(raw, &list); err != nil || list.All != 1 || len(list.MeterReading) != 1 || list.MeterReading[0].Href != "/upt/u1/mr/m2" {
			t.Errorf("MeterReading list after DELETE = all %d, %+v (%v), want only m2", list.All, list.MeterReading, err)
		}
		ctx := context.Background()
		if _, err := stores.ReadingTypes.Get(ctx, "u1", "m1"); err == nil {
			t.Error("the MeterReading's ReadingType survived its DELETE")
		}
		if sets, inSet, direct := hierarchyCounts(t, stores, "u1", "m1", "s2"); sets != 0 || inSet != 0 || direct != 0 {
			t.Errorf("u1/m1 holds %d sets, %d s2 readings, %d direct readings, want 0, 0, 0", sets, inSet, direct)
		}
		if n, err := stores.Readings.Count(ctx, scopeOf(coremetering.ReadingScopeKey("u1", "m1", "s1"))); err != nil || n != 0 {
			t.Errorf("u1/m1/s1 readings = %d, %v, want 0", n, err)
		}
		if sets, inSet, direct := hierarchyCounts(t, stores, "u2", "m1", "s1"); sets != 1 || inSet != 1 || direct != 1 {
			t.Errorf("u2/m1 holds %d sets, %d readings, %d direct, want 1, 1, 1 untouched", sets, inSet, direct)
		}
		if _, err := stores.MeterReadings.Get(ctx, "u1", "m2"); err != nil {
			t.Errorf("the sibling MeterReading m2 was removed: %v", err)
		}
	})

	t.Run("UsagePoint", func(t *testing.T) {
		t.Parallel()
		do, stores := meteringWriteServer(t)
		if status, raw := do(http.MethodDelete, "/upt/u1", managerLFDI, ""); status != http.StatusNoContent {
			t.Fatalf("DELETE UsagePoint = %d %s, want 204", status, raw)
		}
		if status, _ := do(http.MethodGet, "/upt/u1", managerLFDI, ""); status != http.StatusNotFound {
			t.Errorf("GET of the deleted UsagePoint = %d, want 404", status)
		}
		_, raw := do(http.MethodGet, "/upt", managerLFDI, "")
		var list sep2.UsagePointList
		if err := xml.Unmarshal(raw, &list); err != nil || list.All != 1 || len(list.UsagePoint) != 1 || list.UsagePoint[0].Href != "/upt/u2" {
			t.Errorf("UsagePoint list after DELETE = all %d, %+v (%v), want only u2", list.All, list.UsagePoint, err)
		}
		ctx := context.Background()
		for _, mr := range []string{"m1", "m2"} {
			if _, err := stores.MeterReadings.Get(ctx, "u1", mr); !isNotFound(err) {
				t.Errorf("MeterReading u1/%s after its UsagePoint was deleted: %v, want not found", mr, err)
			}
		}
		if n, err := stores.MeterReadings.Count(ctx, "u1"); err != nil || n != 0 {
			t.Errorf("MeterReadings under u1 = %d, %v, want 0", n, err)
		}
		if sets, inSet, direct := hierarchyCounts(t, stores, "u1", "m1", "s2"); sets != 0 || inSet != 0 || direct != 0 {
			t.Errorf("u1/m1 holds %d sets, %d s2 readings, %d direct readings, want 0, 0, 0", sets, inSet, direct)
		}
		if _, err := stores.ReadingTypes.Get(ctx, "u1", "m1"); !isNotFound(err) {
			t.Errorf("ReadingType u1/m1 survived: %v", err)
		}
		if n, err := stores.MeterReadings.Count(ctx, "u2"); err != nil || n != 2 {
			t.Errorf("MeterReadings under u2 = %d, %v, want 2 untouched", n, err)
		}
		if sets, inSet, direct := hierarchyCounts(t, stores, "u2", "m1", "s1"); sets != 1 || inSet != 1 || direct != 1 {
			t.Errorf("u2/m1 holds %d sets, %d readings, %d direct, want 1, 1, 1 untouched", sets, inSet, direct)
		}
		if status, _ := do(http.MethodDelete, "/upt/u1", managerLFDI, ""); status != http.StatusNotFound {
			t.Errorf("second DELETE = %d, want 404", status)
		}
	})

	t.Run("a missing parent is a 404", func(t *testing.T) {
		t.Parallel()
		do, _ := meteringWriteServer(t)
		for _, path := range []string{"/upt/none", "/upt/u1/mr/none", "/upt/u1/mr/m1/rs/none", "/upt/u1/mr/m1/rs/s2/r/none"} {
			if status, _ := do(http.MethodDelete, path, managerLFDI, ""); status != http.StatusNotFound {
				t.Errorf("DELETE %s = %d, want 404", path, status)
			}
		}
	})
}

func isNotFound(err error) bool { return errors.Is(err, store.ErrNotFound) }
