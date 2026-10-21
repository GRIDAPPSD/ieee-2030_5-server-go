package memory

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
)

const (
	dpLFDIA = "AAAA000000000000000000000000000000000001"
	dpLFDIB = "BBBB000000000000000000000000000000000002"
)

func TestDeviceProfileStore_NameSurvivesRestart(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "deviceprofiles.json")

	s, err := NewDeviceProfileStoreWithPersistence(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Put(ctx, store.DeviceProfile{LFDI: dpLFDIB, Name: "Meter 9", Kind: store.DeviceKindDevice}); err != nil {
		t.Fatal(err)
	}
	if err := s.Put(ctx, store.DeviceProfile{LFDI: dpLFDIA, Name: "Gateway", Kind: store.DeviceKindAggregator}); err != nil {
		t.Fatal(err)
	}

	reopened, err := NewDeviceProfileStoreWithPersistence(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	got, err := reopened.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := []store.DeviceProfile{
		{LFDI: dpLFDIA, Name: "Gateway", Kind: "aggregator"},
		{LFDI: dpLFDIB, Name: "Meter 9", Kind: "device"},
	}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("after restart = %+v, want %+v", got, want)
	}

	if err := reopened.Delete(ctx, dpLFDIA); err != nil {
		t.Fatal(err)
	}
	again, err := NewDeviceProfileStoreWithPersistence(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := again.Get(ctx, dpLFDIA); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("deleted profile came back: err = %v", err)
	}
}

func TestDeviceProfileStore_NamesUniqueIgnoringCase(t *testing.T) {
	ctx := context.Background()
	s := NewDeviceProfileStore()
	if err := s.Put(ctx, store.DeviceProfile{LFDI: dpLFDIA, Name: "Roof"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Put(ctx, store.DeviceProfile{LFDI: dpLFDIB, Name: "rOOF"}); !errors.Is(err, store.ErrAlreadyExists) {
		t.Fatalf("duplicate name: err = %v, want ErrAlreadyExists", err)
	}
	if _, err := s.Get(ctx, dpLFDIB); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("a refused Put left a profile: err = %v", err)
	}
	// Control: unnamed devices do not collide, and a device keeps its own name.
	if err := s.Put(ctx, store.DeviceProfile{LFDI: dpLFDIB, Kind: store.DeviceKindDevice}); err != nil {
		t.Errorf("unnamed profile refused: %v", err)
	}
	if err := s.Put(ctx, store.DeviceProfile{LFDI: dpLFDIA, Name: "ROOF"}); err != nil {
		t.Errorf("rewrite under own name refused: %v", err)
	}
}

func TestDeviceProfileStore_RefusesInvalid(t *testing.T) {
	ctx := context.Background()
	s := NewDeviceProfileStore()
	for name, p := range map[string]store.DeviceProfile{
		"lower-case lfdi": {LFDI: strings.ToLower(dpLFDIA)},
		"short lfdi":      {LFDI: "ABC"},
		"empty lfdi":      {},
		"unknown kind":    {LFDI: dpLFDIA, Kind: "gateway"},
	} {
		if err := s.Put(ctx, p); !errors.Is(err, store.ErrInvalidDeviceProfile) {
			t.Errorf("%s: err = %v, want ErrInvalidDeviceProfile", name, err)
		}
	}
	if err := s.Delete(ctx, dpLFDIA); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("Delete of absent: err = %v, want ErrNotFound", err)
	}
}

func TestDeviceProfileStore_FailedWriteIsNotLive(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	s, err := NewDeviceProfileStoreWithPersistence(filepath.Join(dir, "deviceprofiles.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	// Block the directory with a file so the atomic write cannot create its temp file.
	if err := os.WriteFile(dir, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := s.Put(ctx, store.DeviceProfile{LFDI: dpLFDIA, Name: "Roof"}); err == nil {
		t.Fatal("Put succeeded although the snapshot could not be written")
	}
	if _, err := s.Get(ctx, dpLFDIA); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("a profile whose write failed is live: err = %v", err)
	}
}

func TestDeviceProfileStore_LoadRefusesBadFiles(t *testing.T) {
	for name, content := range map[string]string{
		"duplicate name": `{"version":1,"records":[{"lfdi":"` + dpLFDIA + `","name":"X","kind":""},{"lfdi":"` + dpLFDIB + `","name":"x","kind":""}]}`,
		"duplicate lfdi": `{"version":1,"records":[{"lfdi":"` + dpLFDIA + `","name":"X","kind":""},{"lfdi":"` + dpLFDIA + `","name":"Y","kind":""}]}`,
		"bad kind":       `{"version":1,"records":[{"lfdi":"` + dpLFDIA + `","name":"X","kind":"zzz"}]}`,
		"bad lfdi":       `{"version":1,"records":[{"lfdi":"abc","name":"X","kind":""}]}`,
		"bad version":    `{"version":99,"records":[]}`,
	} {
		path := filepath.Join(t.TempDir(), "deviceprofiles.json")
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := NewDeviceProfileStoreWithPersistence(path); err == nil {
			t.Errorf("%s: load succeeded, want an error", name)
		}
	}
}
