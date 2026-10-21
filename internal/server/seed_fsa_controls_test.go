package server

import (
	"context"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/config"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/handlers/sep2time"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

// #972: an FSA control whose copy on an assigned device is missing at boot,
// as a stop between the record write and the copy leaves it, is written to
// that copy by the boot rebuild.
func TestSeedStoresCompletesAMissingFSAControlReplica(t *testing.T) {
	pen := uint32(0xA0B1)
	cfg := &config.Config{DataDir: t.TempDir(), PEN: &pen}
	ctx := context.Background()
	s, _, err := newRunStores(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.EndDevices.Create(ctx, "1", sep2.EndDevice{LFDI: "65DE1159BA8C8897D5A7F94997D22544EB90A2B7", SFDI: "1"}); err != nil {
		t.Fatal(err)
	}
	if err := s.AdminFSAs.Create(ctx, "fsa-A", sep2.FunctionSetAssignments{MRID: "fsa-A"}); err != nil {
		t.Fatal(err)
	}
	tmpl := memory.ProgramTemplate{ID: "derp-1", MRID: "0000000000000000000000000000A001", DefaultMRID: "0000000000000000000000000000D001"}
	if err := s.AdminFSAs.CreateTemplate(ctx, "fsa-A", tmpl); err != nil {
		t.Fatal(err)
	}
	if err := s.AdminFSAs.AssignDevice(ctx, "fsa-A", "1"); err != nil {
		t.Fatal(err)
	}
	lim := sep2.PerCent(4000)
	start := sep2time.Now().Unix() + 3600
	rec := memory.FSAControl{TemplateID: "derp-1", ID: "c1", MRID: "0000000000000000000000000000C001",
		CreationTime: start - 10, Start: start, Duration: 900, Base: sep2.DERControlBase{OpModMaxLimW: &lim}}
	if err := s.AdminFSAs.CreateFSAControl(ctx, "fsa-A", rec); err != nil {
		t.Fatal(err)
	}

	// A restart over the same data dir.
	r, _, err := newRunStores(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := seedStores(ctx, cfg, r); err != nil {
		t.Fatalf("seedStores: %v", err)
	}
	got, err := r.DERControls.Get(ctx, "1/fsa-A/derp-1", "c1")
	if err != nil {
		t.Fatalf("replica after boot: %v", err)
	}
	if got.MRID != rec.MRID || got.CreationTime != rec.CreationTime || got.Href != "/edev/1/fsa/fsa-A/derp/derp-1/derc/c1" ||
		got.Interval == nil || got.Interval.Start != start || got.DERControlBase == nil || got.DERControlBase.OpModMaxLimW == nil || *got.DERControlBase.OpModMaxLimW != lim {
		t.Errorf("replica after boot = %+v, want the record's mRID, creationTime, interval and maxLimW 4000", got)
	}
	if _, err := r.DERControlLifecycles.Get(ctx, "1/fsa-A/derp-1", "c1"); err != nil {
		t.Errorf("replica lifecycle after boot: %v", err)
	}
}

// A copy the rebuild skips, because the device already holds another program
// under the template's id, is skipped for its FSA controls too: the boot
// completes, and the other device still gets its replica.
func TestSeedStoresSkipsAReplicaForASkippedCopy(t *testing.T) {
	pen := uint32(0xA0B1)
	cfg := &config.Config{DataDir: t.TempDir(), PEN: &pen}
	ctx := context.Background()
	s, _, err := newRunStores(cfg)
	if err != nil {
		t.Fatal(err)
	}
	for id, lfdi := range map[string]string{"1": "65DE1159BA8C8897D5A7F94997D22544EB90A2B7", "2": "0000000000000000000000000000000000000002"} {
		if err := s.EndDevices.Create(ctx, id, sep2.EndDevice{LFDI: lfdi, SFDI: id}); err != nil {
			t.Fatal(err)
		}
	}
	own := sep2.DERProgram{MRID: "0000000000000000000000000000B002", Primacy: 5}
	own.Href = "/edev/2/fsa/other/derp/derp-1"
	if err := s.DERPrograms.Create(ctx, "2", "derp-1", own); err != nil {
		t.Fatal(err)
	}
	if err := s.AdminFSAs.Create(ctx, "fsa-A", sep2.FunctionSetAssignments{MRID: "fsa-A"}); err != nil {
		t.Fatal(err)
	}
	for _, tmpl := range []memory.ProgramTemplate{
		{ID: "derp-1", MRID: "0000000000000000000000000000A001", DefaultMRID: "0000000000000000000000000000D001"},
		{ID: "derp-2", MRID: "0000000000000000000000000000A002", DefaultMRID: "0000000000000000000000000000D002"},
	} {
		if err := s.AdminFSAs.CreateTemplate(ctx, "fsa-A", tmpl); err != nil {
			t.Fatal(err)
		}
	}
	// Device 3 has no LFDI, so its fleet cannot be resolved for the check.
	if err := s.EndDevices.Create(ctx, "3", sep2.EndDevice{SFDI: "3"}); err != nil {
		t.Fatal(err)
	}
	for _, dev := range []string{"1", "2", "3"} {
		if err := s.AdminFSAs.AssignDevice(ctx, "fsa-A", dev); err != nil {
			t.Fatal(err)
		}
	}
	lim := sep2.PerCent(4000)
	start := sep2time.Now().Unix() + 3600
	rec := memory.FSAControl{TemplateID: "derp-1", ID: "c1", MRID: "0000000000000000000000000000C001",
		CreationTime: start - 10, Start: start, Duration: 900, Base: sep2.DERControlBase{OpModMaxLimW: &lim}}
	if err := s.AdminFSAs.CreateFSAControl(ctx, "fsa-A", rec); err != nil {
		t.Fatal(err)
	}
	other := rec
	other.TemplateID, other.ID, other.MRID = "derp-2", "c2", "0000000000000000000000000000C002"
	if err := s.AdminFSAs.CreateFSAControl(ctx, "fsa-A", other); err != nil {
		t.Fatal(err)
	}

	r, _, err := newRunStores(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := seedStores(ctx, cfg, r); err != nil {
		t.Fatalf("seedStores with one skipped copy and one device without a fleet: %v, want the boot to complete", err)
	}
	if got, err := r.DERControls.Get(ctx, "1/fsa-A/derp-1", "c1"); err != nil || got.MRID != rec.MRID {
		t.Errorf("device 1 replica = %+v, %v; want mRID %s", got, err, rec.MRID)
	}
	if _, err := r.DERControls.Get(ctx, "2/fsa-A/derp-1", "c1"); err == nil {
		t.Error("device 2, whose copy was skipped, holds a replica")
	}
	// The skipped copy does not keep the device's other copy from its control.
	if got, err := r.DERControls.Get(ctx, "2/fsa-A/derp-2", "c2"); err != nil || got.MRID != other.MRID {
		t.Errorf("device 2 replica in its other copy = %+v, %v; want mRID %s", got, err, other.MRID)
	}
	if p, err := r.DERPrograms.Get(ctx, "2", "derp-1"); err != nil || p.Href != own.Href {
		t.Errorf("device 2's own program = %+v, %v; want it untouched", p, err)
	}
}
