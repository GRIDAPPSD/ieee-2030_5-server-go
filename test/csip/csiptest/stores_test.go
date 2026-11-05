package csiptest_test

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/commitment"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/dercontrol"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/server"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2server"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/test/csip/csiptest"
)

// TestNewFreshStores_BuildsTheLedgerLikeRun: the harness stores carry the
// commitment ledger and both lifecycle stores Run builds, and the ledger
// reads the harness's own response and cancel-mark stores. Without them a
// windowed flow reservation grant is refused and a "denied when committed"
// test would pass vacuously.
func TestNewFreshStores_BuildsTheLedgerLikeRun(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := csiptest.NewFreshStores()
	if s.CommitmentLedger == nil || s.DERControlLifecycles == nil || s.FlowReservationResponseLifecycles == nil {
		t.Fatalf("ledger %v, control lifecycles %v, response lifecycles %v; want all three",
			s.CommitmentLedger != nil, s.DERControlLifecycles != nil, s.FlowReservationResponseLifecycles != nil)
	}

	lfdi := strings.Repeat("A", 40)
	if err := s.EndDevices.Create(ctx, "e1", sep2.EndDevice{LFDI: lfdi}); err != nil {
		t.Fatal(err)
	}
	frp := sep2.FlowReservationResponse{}
	frp.Href = "/edev/e1/frp/R1"
	frp.MRID = "MRID-R1"
	frp.Interval = &sep2.DateTimeInterval{Start: 1000, Duration: 600}
	if err := s.FlowReservationResponses.Create(ctx, "e1", "R1", frp); err != nil {
		t.Fatal(err)
	}
	check := func() error {
		w := commitment.Window{Start: 1200, Duration: 10}
		return s.CommitmentLedger.Within(ctx, []string{lfdi}, func(v commitment.View) error {
			return v.CheckGrant(ctx, lfdi, &w, "")
		})
	}
	var ce *commitment.ConflictError
	if err := check(); !errors.As(err, &ce) || ce.MRID != "MRID-R1" {
		t.Fatalf("CheckGrant = %v, want a conflict naming MRID-R1", err)
	}
	at := int64(1100)
	if err := s.FlowReservationResponseLifecycles.Create(ctx, "e1", "R1", dercontrol.LifecycleRecord{CancelledAt: &at}); err != nil {
		t.Fatal(err)
	}
	if err := check(); err != nil {
		t.Fatalf("after the cancel mark: CheckGrant = %v, want the window free", err)
	}
}

// TestNewFreshStores_IsTheProductionSet: the harness serves the stores Run
// serves, so a route Run mounts is mounted under test. The three fields the
// old hand-listed copy lacked are the ones that hid the Registration route
// from the WADL sweep (#1244).
func TestNewFreshStores_IsTheProductionSet(t *testing.T) {
	t.Parallel()
	s := csiptest.NewFreshStores()
	if s.Registrations == nil || s.EndDeviceIndexes == nil || s.AdminFSAs == nil || s.DeviceProfiles == nil || s.ProgramOrigins == nil {
		t.Fatalf("Registrations %v, EndDeviceIndexes %v, AdminFSAs %v, DeviceProfiles %v, ProgramOrigins %v; want all five wired",
			s.Registrations != nil, s.EndDeviceIndexes != nil, s.AdminFSAs != nil, s.DeviceProfiles != nil, s.ProgramOrigins != nil)
	}
	other := csiptest.NewFreshStores()
	if err := s.Registrations.Create(context.Background(), "1", sep2.Registration{PIN: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := other.Registrations.Get(context.Background(), "1"); err == nil {
		t.Fatal("a Registration written to one set is visible in another: the sets share state")
	}
}

// TestNewFreshStores_CoversEveryFieldNewStoresFills: whatever field the
// embeddable constructor fills, the harness set fills too once projected for
// core, so a store added to one is not missing from the other.
func TestNewFreshStores_CoversEveryFieldNewStoresFills(t *testing.T) {
	t.Parallel()
	want := reflect.ValueOf(sep2server.NewStores()).Elem()
	got := reflect.ValueOf(server.NewCoreStores(csiptest.NewFreshStores())).Elem()
	for i := 0; i < want.NumField(); i++ {
		name := want.Type().Field(i).Name
		switch want.Field(i).Kind() {
		case reflect.Pointer, reflect.Interface, reflect.Map, reflect.Slice:
			if !want.Field(i).IsNil() && got.Field(i).IsNil() {
				t.Errorf("assembly.Stores.%s is filled by sep2server.NewStores and nil in the harness set", name)
			}
		}
	}
}
