package dercontrol

import (
	"context"
	"errors"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/handlers/sep2time"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

// getFails makes one lifecycle record unreadable.
type getFails struct {
	*memory.ScopedStore[LifecycleRecord]
	id  string
	err error
}

func (g *getFails) Get(ctx context.Context, parent, id string) (LifecycleRecord, error) {
	if id == g.id {
		return LifecycleRecord{}, g.err
	}
	return g.ScopedStore.Get(ctx, parent, id)
}

// Replicate reads the newer events' lifecycles before it runs the fleet
// check, so a failure in both reports the read.
func TestReplicate_NewerLifecycleReadPrecedesFleetCheck(t *testing.T) {
	controls := memory.NewScopedStore[sep2.DERControl]()
	issuer, _, lifecycles, targets := acrossHarness(t, controls, "d1")
	ctx := context.Background()
	readErr, checkErr := errors.New("lifecycle read failed"), errors.New("fleet check failed")
	issuer.lifecycles = &getFails{ScopedStore: lifecycles, id: "newer", err: readErr}
	targets[0].Fleet.Check = func(context.Context, Proposal) error { return checkErr }

	start := sep2time.Now().Unix() + 600
	mk := func(mrid string, created int64) sep2.DERControl {
		c := sep2.DERControl{DERControlBase: &sep2.DERControlBase{OpModMaxLimW: (*sep2.PerCent)(uint16ptr(1000))}}
		c.MRID, c.CreationTime = mrid, created
		c.Interval = &sep2.DateTimeInterval{Start: start, Duration: 600}
		return c
	}
	newer := mk("NEWER", start)
	newer.Href = controlListHref("d1", "fsa", "tmpl") + "/newer"
	if err := controls.Create(ctx, "d1/fsa/tmpl", "newer", newer); err != nil {
		t.Fatalf("seed: %v", err)
	}

	_, err := issuer.Replicate(ctx, []Replica{{Target: targets[0], ID: "r1", Control: mk("R1", start-60)}})
	if !errors.Is(err, readErr) || errors.Is(err, checkErr) {
		t.Fatalf("Replicate error = %v, want the lifecycle read failure only", err)
	}

	issuer.lifecycles = lifecycles
	_, err = issuer.Replicate(ctx, []Replica{{Target: targets[0], ID: "r1", Control: mk("R1", start-60)}})
	if !errors.Is(err, checkErr) {
		t.Fatalf("Replicate error = %v, want the fleet check failure once the read succeeds", err)
	}
}
