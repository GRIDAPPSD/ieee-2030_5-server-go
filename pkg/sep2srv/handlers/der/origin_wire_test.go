package der_test

import (
	"context"
	"encoding/xml"
	"strings"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/dercontrol"
	coreder "github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/handlers/der"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

// #1155: a control whose record says it came from DERMS is served to a
// client exactly as one an operator issued: the origin never reaches the wire.
func TestDerivedStatusControlStore_OriginNeverServed(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	controls := memory.NewScopedStore[sep2.DERControl]()
	lifecycles := memory.NewScopedStore[dercontrol.LifecycleRecord]()
	const scope = "dev1/1/1"
	for id, origin := range map[string]string{"c1": "", "c2": dercontrol.OriginDERMS} {
		seedFaultTestControl(t, controls, scope, id)
		if err := lifecycles.Create(ctx, scope, id, dercontrol.LifecycleRecord{Origin: origin}); err != nil {
			t.Fatal(err)
		}
	}
	decorated := coreder.NewDerivedStatusControlStoreFor(controls, lifecycles, false)
	wire := map[string]string{}
	for _, id := range []string{"c1", "c2"} {
		got, err := decorated.Get(ctx, scope, id)
		if err != nil {
			t.Fatal(err)
		}
		b, err := xml.Marshal(&got)
		if err != nil {
			t.Fatal(err)
		}
		wire[id] = string(b)
		if low := strings.ToLower(wire[id]); strings.Contains(low, "origin") {
			t.Errorf("%s: wire names the origin: %s", id, wire[id])
		}
		// Control: the status is derived from the same record, so the
		// decorator did read it.
		if !strings.Contains(wire[id], "<currentStatus>") {
			t.Errorf("%s: wire carries no derived status, so the record was not read: %s", id, wire[id])
		}
	}
	// The two differ only in their own ids.
	if strings.ReplaceAll(wire["c1"], "c1", "X") != strings.ReplaceAll(wire["c2"], "c2", "X") {
		t.Errorf("a derms-origin control is served differently from an admin one:\n%s\n%s", wire["c1"], wire["c2"])
	}
}
