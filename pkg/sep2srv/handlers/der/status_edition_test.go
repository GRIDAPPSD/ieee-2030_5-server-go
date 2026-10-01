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

// #798 fix round: a control with no lifecycle record (a boot fixture or the
// CSIP loader's) is served as stored, in both editions: no EventStatus is
// derived or invented for it. Where it carries its own EventStatus, 2023
// serves potentiallySuperseded true on it and 2018 serves it as stored.
func TestDerivedStatusControlStore_NoLifecycleRecordServesStoredStatus(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	for _, tc := range []struct {
		name        string
		edition2023 bool
		stored      *sep2.EventStatus
		wantStatus  string
		wantFlag    string
	}{
		{"2018 no status stays absent", false, nil, "", ""},
		{"2023 no status stays absent", true, nil, "", ""},
		{"2018 stored status as stored", false, &sep2.EventStatus{CurrentStatus: 0, DateTime: 900}, "<currentStatus>0</currentStatus>", "<potentiallySuperseded>false</potentiallySuperseded>"},
		{"2023 stored status flagged true", true, &sep2.EventStatus{CurrentStatus: 0, DateTime: 900}, "<currentStatus>0</currentStatus>", "<potentiallySuperseded>true</potentiallySuperseded>"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			controls := memory.NewScopedStore[sep2.DERControl]()
			ctrl := seedFaultTestControl(t, controls, "dev1/1/1", "c1")
			ctrl.EventStatus = tc.stored
			if err := controls.Update(ctx, "dev1/1/1", "c1", ctrl); err != nil {
				t.Fatal(err)
			}
			lifecycles := memory.NewScopedStore[dercontrol.LifecycleRecord]()
			decorated := coreder.NewDerivedStatusControlStoreFor(controls, lifecycles, tc.edition2023)
			got, err := decorated.Get(ctx, "dev1/1/1", "c1")
			if err != nil {
				t.Fatal(err)
			}
			wire, err := xml.Marshal(&got)
			if err != nil {
				t.Fatal(err)
			}
			x := string(wire)
			if tc.stored == nil {
				if strings.Contains(x, "EventStatus") {
					t.Errorf("an EventStatus appeared on a control with none: %s", x)
				}
				return
			}
			if !strings.Contains(x, tc.wantStatus) || !strings.Contains(x, tc.wantFlag) {
				t.Errorf("wire lacks %s and %s: %s", tc.wantStatus, tc.wantFlag, x)
			}
			// The stored copy must not be edited by serving.
			again, err := controls.Get(ctx, "dev1/1/1", "c1")
			if err != nil {
				t.Fatal(err)
			}
			if again.EventStatus == nil || again.EventStatus.PotentiallySuperseded {
				t.Errorf("serving edited the stored EventStatus: %+v", again.EventStatus)
			}
		})
	}
}
