package server

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/config"
)

// #1205: a mirror created before a restart resolves at the same id after it,
// so POST /mup/{id}/mr does not answer 404.
func TestMirrorUsagePoints_SurviveARestart(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	mup := sep2.MirrorUsagePoint{
		Resource:    sep2.Resource{Href: "/mup/0A1B2C"},
		MRID:        "pv-1",
		Description: "pv power",
		DeviceLFDI:  "3E4F45AB31EDFE5B67E343E5E4562E3100000000",
	}

	s1, _, err := newRunStores(&config.Config{DataDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if err := s1.MirrorUsagePoints.Create(ctx, "0A1B2C", mup); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "mirrorusagepoints.json")); err != nil {
		t.Fatalf("mirror snapshot after the write: %v", err)
	}

	s2, _, err := newRunStores(&config.Config{DataDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	got, err := s2.MirrorUsagePoints.Get(ctx, "0A1B2C")
	if err != nil {
		t.Fatalf("mirror after a restart: %v", err)
	}
	if got.Href != mup.Href || got.MRID != "pv-1" || got.Description != "pv power" || got.DeviceLFDI != mup.DeviceLFDI {
		t.Errorf("mirror after a restart = %+v, want %+v", got, mup)
	}
}

func TestMirrorUsagePoints_CorruptSnapshotFailsStart(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "mirrorusagepoints.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := newRunStores(&config.Config{DataDir: dir}); err == nil {
		t.Fatal("newRunStores accepted a corrupt mirror snapshot")
	}
}

func TestMirrorUsagePoints_NoDataDirStaysInMemory(t *testing.T) {
	s, _, err := newRunStores(&config.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.MirrorUsagePoints.Create(context.Background(), "X", sep2.MirrorUsagePoint{MRID: "m"}); err != nil {
		t.Fatal(err)
	}
}
