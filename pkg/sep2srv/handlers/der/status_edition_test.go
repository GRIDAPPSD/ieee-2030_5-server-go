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

// #798: a served DERControl's potentiallySuperseded follows the edition,
// read from its wire XML, for a cancelled and a live control alike.
func TestDerivedStatusControlStore_PotentiallySupersededFollowsEdition(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	for _, tc := range []struct {
		name        string
		edition2023 bool
		want        string
	}{
		{"2018 keeps false", false, "<potentiallySuperseded>false</potentiallySuperseded>"},
		{"2023 serves true", true, "<potentiallySuperseded>true</potentiallySuperseded>"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			controls := memory.NewScopedStore[sep2.DERControl]()
			seedFaultTestControl(t, controls, "dev1/1/1", "c1")
			seedFaultTestControl(t, controls, "dev1/1/1", "c2")
			lifecycles := memory.NewScopedStore[dercontrol.LifecycleRecord]()
			cancelledAt := int64(1200)
			if err := lifecycles.Create(ctx, "dev1/1/1", "c1", dercontrol.LifecycleRecord{CancelledAt: &cancelledAt}); err != nil {
				t.Fatal(err)
			}
			if err := lifecycles.Create(ctx, "dev1/1/1", "c2", dercontrol.LifecycleRecord{}); err != nil {
				t.Fatal(err)
			}
			decorated := coreder.NewDerivedStatusControlStoreFor(controls, lifecycles, tc.edition2023)
			for id, status := range map[string]string{"c1": "<currentStatus>2</currentStatus>", "c2": "<currentStatus>1</currentStatus>"} {
				got, err := decorated.Get(ctx, "dev1/1/1", id)
				if err != nil {
					t.Fatal(err)
				}
				wire, err := xml.Marshal(&got)
				if err != nil {
					t.Fatal(err)
				}
				x := string(wire)
				if !strings.Contains(x, status) || !strings.Contains(x, tc.want) {
					t.Errorf("%s: wire lacks %s and %s: %s", id, status, tc.want, x)
				}
			}
		})
	}
}
