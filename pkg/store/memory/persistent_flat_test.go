package memory_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/storetest"
)

func TestPersistentStore_ConformanceSuite(t *testing.T) {
	storetest.RunResourceStoreSuite(t, func(t *testing.T) store.ResourceStore[storetest.Resource] {
		s, err := memory.NewPersistentStore[storetest.Resource](filepath.Join(t.TempDir(), "r.json"), "resources")
		if err != nil {
			t.Fatalf("NewPersistentStore: %v", err)
		}
		return s
	})
}

func TestPersistentStore_MirrorSurvivesARestartFieldForField(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "mirrorusagepoints.json")
	rate := uint32(60)
	mup := sep2.MirrorUsagePoint{
		Resource:            sep2.Resource{Href: "/mup/ABCDEF"},
		MRID:                "client-mrid-1",
		Description:         "grid power",
		RoleFlags:           sep2.RoleFlagsValue(0x0B),
		ServiceCategoryKind: 0,
		Status:              1,
		DeviceLFDI:          "3E4F45AB31EDFE5B67E343E5E4562E3100000000",
		PostRate:            &rate,
	}

	s1, err := memory.NewPersistentStore[sep2.MirrorUsagePoint](path, "MirrorUsagePoint")
	if err != nil {
		t.Fatal(err)
	}
	if err := s1.Create(ctx, "ABCDEF", mup); err != nil {
		t.Fatal(err)
	}
	if err := s1.Create(ctx, "123456", sep2.MirrorUsagePoint{MRID: "second"}); err != nil {
		t.Fatal(err)
	}
	if err := s1.Delete(ctx, "123456"); err != nil {
		t.Fatal(err)
	}

	s2, err := memory.NewPersistentStore[sep2.MirrorUsagePoint](path, "MirrorUsagePoint")
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	got, err := s2.Get(ctx, "ABCDEF")
	if err != nil {
		t.Fatalf("Get after restart: %v", err)
	}
	if got.Href != "/mup/ABCDEF" || got.MRID != "client-mrid-1" || got.Description != "grid power" ||
		got.RoleFlags != sep2.RoleFlagsValue(0x0B) || got.Status != 1 ||
		got.DeviceLFDI != mup.DeviceLFDI || got.PostRate == nil || *got.PostRate != 60 {
		t.Errorf("mirror after restart = %+v, want the stored fields", got)
	}
	if _, err := s2.Get(ctx, "123456"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("deleted mirror after restart: err = %v, want ErrNotFound", err)
	}
	if n, err := s2.Count(ctx); err != nil || n != 1 {
		t.Errorf("Count after restart = %d, %v, want 1", n, err)
	}
	// A re-POST of the same mirror finds it, as the create handler relies on.
	if err := s2.Create(ctx, "ABCDEF", mup); !errors.Is(err, store.ErrAlreadyExists) {
		t.Errorf("Create of an existing mirror after restart: err = %v, want ErrAlreadyExists", err)
	}
}

func TestPersistentStore_CorruptFileFailsTheConstructor(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mirrorusagepoints.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := memory.NewPersistentStore[sep2.MirrorUsagePoint](path, "MirrorUsagePoint")
	if err == nil || s != nil {
		t.Fatalf("NewPersistentStore on a corrupt file = %v, %v, want a nil store and an error", s, err)
	}
}

func TestPersistentStore_EmptyPathIsPlainMemory(t *testing.T) {
	s, err := memory.NewPersistentStore[sep2.MirrorUsagePoint]("", "MirrorUsagePoint")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := s.(*memory.Store[sep2.MirrorUsagePoint]); !ok {
		t.Errorf("empty path built %T, want *memory.Store", s)
	}
}
