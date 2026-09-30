//go:build csip_test_hooks

package der_test

import (
	"context"
	"testing"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/dercontrol"
	coreder "github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/handlers/der"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/handlers/sep2time"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

// TestDerivedStatusControlStore_DerivationFollowsTestHookOffset proves Get
// (and by the same code path, List) reads the clock through sep2time.Now,
// including its csip_test_hooks offset, mirroring
// internal/dercontrol/clock_hooks_test.go for the serve side (#564 fix
// round 1). If status_store.go called time.Now() directly, the control
// below would still read Scheduled after the 48-hour advance, since the
// unshifted wall clock has not reached its start.
func TestDerivedStatusControlStore_DerivationFollowsTestHookOffset(t *testing.T) {
	sep2time.ResetClockOffset()
	defer sep2time.ResetClockOffset()

	const scope = "dev1/1/1"
	const id = "c1"
	creationTime := sep2time.Now().Unix()
	start := creationTime + 24*3600 // 24h out: Scheduled under the unshifted clock

	controls := memory.NewScopedStore[sep2.DERControl]()
	ctrl := sep2.DERControl{DERControlBase: &sep2.DERControlBase{OpModTargetW: &sep2.ActivePower{Value: 1}}}
	ctrl.Href = "/edev/dev1/fsa/1/derp/1/derc/" + id
	ctrl.CreationTime = creationTime
	ctrl.Interval = &sep2.DateTimeInterval{Start: start, Duration: 900}
	if err := controls.Create(context.Background(), scope, id, ctrl); err != nil {
		t.Fatalf("seed control: %v", err)
	}
	lifecycles := memory.NewScopedStore[dercontrol.LifecycleRecord]()
	if err := lifecycles.Create(context.Background(), scope, id, dercontrol.LifecycleRecord{}); err != nil {
		t.Fatalf("seed lifecycle record: %v", err)
	}

	decorated := coreder.NewDerivedStatusControlStore(controls, lifecycles)

	before, err := decorated.Get(context.Background(), scope, id)
	if err != nil {
		t.Fatalf("Get before advance: %v", err)
	}
	if before.EventStatus == nil || before.EventStatus.CurrentStatus != sep2.EventStatusScheduled {
		t.Fatalf("before advance: EventStatus = %+v, want Scheduled (premise of this test)", before.EventStatus)
	}

	sep2time.AdvanceClock(48 * time.Hour) // now 24h past start, on the offset clock only

	after, err := decorated.Get(context.Background(), scope, id)
	if err != nil {
		t.Fatalf("Get after advance: %v", err)
	}
	if after.EventStatus == nil || after.EventStatus.CurrentStatus != sep2.EventStatusActive {
		t.Fatalf("after a 48h test-hook advance: EventStatus = %+v, want Active; status_store.go may be reading time.Now() directly", after.EventStatus)
	}

	// Same proof for List, over the identical scope and clock state.
	result, err := decorated.List(context.Background(), scope, store.ListOptions{Unbounded: true})
	if err != nil {
		t.Fatalf("List after advance: %v", err)
	}
	if len(result.Items) != 1 || result.Items[0].EventStatus == nil || result.Items[0].EventStatus.CurrentStatus != sep2.EventStatusActive {
		t.Fatalf("List after a 48h test-hook advance: got %+v, want one Active member", result.Items)
	}
}
