package server

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/commitment"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/config"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/assembly"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
)

type controlGetSpy struct {
	store.ScopedStore[sep2.DERControl]
	gets atomic.Int32
}

func (s *controlGetSpy) Get(ctx context.Context, parentID, id string) (sep2.DERControl, error) {
	s.gets.Add(1)
	return s.ScopedStore.Get(ctx, parentID, id)
}

// The recovery writers cancel executions through the Stores' issuer. That
// issuer reads through a spy the Stores do not hold, so a read through the
// spy can only come from it; and without an issuer there are no writers.
func TestRecoveryWriters_UseTheStoresIssuer(t *testing.T) {
	s, _, err := newRunStores(&config.Config{})
	if err != nil {
		t.Fatal(err)
	}
	spy := &controlGetSpy{ScopedStore: s.DERControls}
	s.DERControlIssuer, err = assembly.NewDERControlIssuer(s.DERPrograms, spy, s.DERControlLifecycles, nil)
	if err != nil {
		t.Fatal(err)
	}

	w := recoveryWriters(s, nil)
	if w.Executions == nil || w.Grants == nil {
		t.Fatalf("recoveryWriters = %+v, want both writers", w)
	}
	_ = w.Executions.CancelExecution(context.Background(), commitment.Control{MRID: "C1", ID: "c1", Scope: "e/f/p"}, "test")
	if spy.gets.Load() == 0 {
		t.Error("the execution writer never read through the Stores' issuer")
	}

	s.DERControlIssuer = nil
	if w := recoveryWriters(s, nil); w.Executions != nil || w.Grants != nil {
		t.Errorf("recoveryWriters with no issuer = %+v, want the zero Writers", w)
	}
}
