package sources_test

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/commitment"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/commitment/sources"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/dercontrol"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

// countingLifecycles counts walks of the control lifecycle store: every
// walk starts with Parents.
type countingLifecycles struct {
	lifecycleStore
	walks *atomic.Int32
}

func (c countingLifecycles) Parents(ctx context.Context) ([]string, error) {
	c.walks.Add(1)
	return c.lifecycleStore.Parents(ctx)
}

type countingResponses struct {
	*memory.ScopedStore[sep2.FlowReservationResponse]
	walks *atomic.Int32
}

func (c countingResponses) Parents(ctx context.Context) ([]string, error) {
	c.walks.Add(1)
	return c.ScopedStore.Parents(ctx)
}

// A commitments read walks each store once, however many live grants the
// fleet holds, and still counts an execution stored in another fleet
// against its grant, as an execution check does.
func TestLedgerCommitmentsWalksEachStoreOnce(t *testing.T) {
	t.Parallel()
	for _, n := range []int{1, 5, 25} {
		t.Run(fmt.Sprintf("%d grants", n), func(t *testing.T) {
			t.Parallel()
			f := newFixture(t)
			for i := range n {
				f.addResponse(t, fmt.Sprintf("G%d", i), &sep2.DateTimeInterval{Start: 100000 + int64(i)*1000, Duration: 600}, nil)
			}
			f.addControl(t, standScope, "X1", 100000, 600, &dercontrol.LifecycleRecord{GrantMRID: "MRID-G0", FleetKey: standaloneLFDI, Reach: 1})

			var responseWalks, controlWalks atomic.Int32
			l := commitment.NewLedger(
				sources.NewGrants(countingResponses{f.responses, &responseWalks}, f.responseLifecycles, f.resolver()),
				sources.NewControls(f.controls, countingLifecycles{f.controlLifecycles, &controlWalks}, f.resolver()),
			)
			got, err := l.Commitments(context.Background(), aggLFDI, 50000)
			must(t, err)
			if r, c := responseWalks.Load(), controlWalks.Load(); r != 1 || c != 1 {
				t.Errorf("walks: responses %d, controls %d; want 1 and 1", r, c)
			}
			if len(got.Grants) != n {
				t.Fatalf("grants = %d, want %d", len(got.Grants), n)
			}
			g0 := got.Grants[0]
			if g0.MRID != "MRID-G0" || len(g0.Executions) != 1 || g0.Executions[0].MRID != "MRID-X1" || g0.Executions[0].FleetKey != standaloneLFDI {
				t.Errorf("G0 = %s with executions %+v, want MRID-G0 with MRID-X1 from the standalone fleet", g0.MRID, g0.Executions)
			}
		})
	}
}

// #1156: the copies of one DERMS share are one fleet total, listed once at
// its value; the copies of an operator's FSA control are one per device.
func TestLedgerCommitmentsCountsAFleetTotalOnce(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	second := strings.Repeat("D", 40)
	must(t, f.devices.Create(ctx, "m2", sep2.EndDevice{LFDI: second}))
	must(t, f.managers.Assign(ctx, aggLFDI, second))
	derms := &dercontrol.LifecycleRecord{FleetKey: aggLFDI, Reach: 1, Origin: dercontrol.OriginDERMS}
	admin := &dercontrol.LifecycleRecord{FleetKey: aggLFDI, Reach: 1}
	for _, dev := range []string{managedID, "m2"} {
		f.addControl(t, dev+"/fsa2/derp2", "S1", 20000, 600, derms)
		f.addControl(t, dev+"/fsa3/derp3", "A1", 30000, 600, admin)
	}
	l := commitment.NewLedger(f.grants(), f.controlSource())
	got, err := l.Commitments(ctx, aggLFDI, 10000)
	must(t, err)
	counts := map[string]int{}
	for _, c := range got.Plain {
		counts[c.MRID]++
		if c.MRID == "MRID-S1" && (c.TargetW == nil || c.TargetW.Value != -500 || !c.FleetTotal) {
			t.Errorf("share = %+v, want its own value -500 marked a fleet total", c)
		}
	}
	if counts["MRID-S1"] != 1 {
		t.Errorf("DERMS share listed %d times, want once across its 2 copies", counts["MRID-S1"])
	}
	if counts["MRID-A1"] != 2 {
		t.Errorf("operator FSA control listed %d times, want once per device copy", counts["MRID-A1"])
	}
}
