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
