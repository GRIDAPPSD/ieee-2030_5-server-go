package assembly

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/commitment"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/dercontrol"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

// controlGetSpy counts the reads an issuer makes of its control store.
type controlGetSpy struct {
	store.ScopedStore[sep2.DERControl]
	gets atomic.Int32
}

func (s *controlGetSpy) Get(ctx context.Context, parentID, id string) (sep2.DERControl, error) {
	s.gets.Add(1)
	return s.ScopedStore.Get(ctx, parentID, id)
}

// The cancel writers use the issuer the Stores carry, not one built from the
// stores' own handles: the issuer here reads through a spy the Stores do not
// hold, so a read through the spy can only come from the supplied issuer.
func TestCommitmentWriters_UseTheIssuerTheStoresCarry(t *testing.T) {
	controls := memory.NewScopedStore[sep2.DERControl]()
	spy := &controlGetSpy{ScopedStore: controls}
	programs := memory.NewScopedStore[sep2.DERProgram]()
	lifecycles := memory.NewScopedStore[dercontrol.LifecycleRecord]()
	issuer, err := NewDERControlIssuer(programs, spy, lifecycles, nil)
	if err != nil {
		t.Fatal(err)
	}
	stores := &Stores{
		DERPrograms:                       programs,
		DERControls:                       controls,
		DERControlLifecycles:              lifecycles,
		FlowReservationResponseLifecycles: memory.NewScopedStore[dercontrol.LifecycleRecord](),
		DERControlIssuer:                  issuer,
	}

	w := commitmentWriters(stores)
	if w.Executions == nil {
		t.Fatal("commitmentWriters returned no execution writer")
	}
	_ = w.Executions.CancelExecution(context.Background(), commitment.Control{MRID: "C1", ID: "c1", Scope: "e/f/p"}, "test")
	if got := spy.gets.Load(); got == 0 {
		t.Error("the execution writer never read through the supplied issuer's control store")
	}
}
